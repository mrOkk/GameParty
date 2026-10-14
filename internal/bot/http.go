package bot

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type User struct {
	ID    int64  `json:"id"`
	First string `json:"first_name"`
	Last  string `json:"last_name"`
	Bot   bool   `json:"is_bot"`
}
type Message struct {
	From *User  `json:"from"`
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}
type Update struct {
	ID       int64    `json:"update_id"`
	Message  *Message `json:"message"`
	Callback *struct {
		ID      string   `json:"id"`
		From    User     `json:"from"`
		Data    string   `json:"data"`
		Message *Message `json:"message"`
	} `json:"callback_query"`
}
type Server struct {
	Store         Store
	Token, Secret string
	Client        *http.Client
	APIBase       string
	Now           func() time.Time
}

func (s *Server) api(ctx context.Context, method string, data any) (json.RawMessage, error) {
	b, e := json.Marshal(data)
	if e != nil {
		return nil, e
	}
	base := s.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, e := http.NewRequestWithContext(ctx, "POST", base+"/bot"+s.Token+"/"+method, bytes.NewReader(b))
	if e != nil {
		return nil, fmt.Errorf("telegram request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := s.Client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("telegram connection failed")
	}
	defer res.Body.Close()
	var out struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); e != nil || !out.OK || res.StatusCode != 200 {
		return nil, fmt.Errorf("telegram API failed")
	}
	return out.Result, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path != "/webhook" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if s.Secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(s.Secret)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var u Update
	if e := json.NewDecoder(r.Body).Decode(&u); e != nil {
		http.Error(w, "invalid update", 400)
		return
	}
	m := u.Message
	callbackID := ""
	if u.Callback != nil {
		m = u.Callback.Message
		if m == nil {
			w.WriteHeader(200)
			return
		}
		copy := *m
		m = &copy
		m.From = &u.Callback.From
		m.Text = "/join " + u.Callback.Data
		callbackID = u.Callback.ID
	}
	if m == nil || m.From == nil || m.From.Bot {
		w.WriteHeader(200)
		return
	}
	if u.Callback != nil && strings.HasPrefix(u.Callback.Data, "page:") {
		parts := strings.Split(u.Callback.Data, ":")
		if len(parts) != 3 {
			w.WriteHeader(200)
			return
		}
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			w.WriteHeader(200)
			return
		}
		page, e := strconv.Atoi(parts[2])
		if e != nil {
			w.WriteHeader(200)
			return
		}
		state, _, e := s.Store.Load(r.Context(), m.Chat.ID)
		if e != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		receipt, ok := state.Seen[id]
		if !ok {
			s.reply(w, m.Chat.ID, Result{Text: "Карточка устарела. Повторите команду."}, 0, 0)
			return
		}
		_, _ = s.api(r.Context(), "answerCallbackQuery", map[string]any{"callback_query_id": callbackID})
		s.reply(w, m.Chat.ID, receipt.Result, id, page)
		return
	}
	if m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		s.reply(w, m.Chat.ID, Result{Text: "Добавьте меня в группу. Профили и сборы настраиваются командами в группе.\n" + Help}, 0, 0)
		return
	}
	if !strings.HasPrefix(m.Text, "/") {
		w.WriteHeader(200)
		return
	}
	// A command addressed to another bot must not mutate this bot's state.
	first := strings.Fields(m.Text)[0]
	if strings.Contains(first, "@") {
		v, e := s.api(r.Context(), "getMe", map[string]any{})
		if e != nil {
			http.Error(w, "telegram unavailable", 503)
			return
		}
		var me struct{ Username string }
		if json.Unmarshal(v, &me) != nil {
			http.Error(w, "telegram unavailable", 503)
			return
		}
		if !strings.EqualFold(strings.SplitN(first, "@", 2)[1], me.Username) {
			w.WriteHeader(200)
			return
		}
	}
	admin := false
	if strings.Split(first, "@")[0] == "/alias" {
		v, e := s.api(r.Context(), "getChatMember", map[string]any{"chat_id": m.Chat.ID, "user_id": m.From.ID})
		if e != nil {
			http.Error(w, "cannot verify administrator", 503)
			return
		}
		var member struct{ Status string }
		if json.Unmarshal(v, &member) != nil {
			http.Error(w, "invalid membership", 503)
			return
		}
		admin = member.Status == "creator" || member.Status == "administrator"
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	for attempt := 0; attempt < 8; attempt++ {
		state, old, e := s.Store.Load(r.Context(), m.Chat.ID)
		if e != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		if receipt, seen := state.Seen[u.ID]; seen {
			if receipt.Result.Notify {
				if e := s.deliver(r.Context(), m.Chat.ID, u.ID); e != nil {
					http.Error(w, "notification delivery failed", 503)
					return
				}
			}
			w.WriteHeader(200)
			return
		}
		text := m.Text
		var result Result
		if u.Callback != nil {
			p := state.active(u.Callback.Data, now)
			if p == nil || state.today(p.Game, now) != p {
				e = fmt.Errorf("Эта карточка сбора устарела. Используйте /parties")
			} else {
				text = "/join " + state.Games[p.Game]
			}
		}
		if e == nil {
			result, e = state.Apply(Input{u.ID, m.From.ID, strings.TrimSpace(m.From.First + " " + m.From.Last), text, admin}, now)
		}
		if e != nil {
			result = Result{Text: html.EscapeString(e.Error())}
		}
		state.Seen[u.ID] = Receipt{At: now, Result: result}
		for id, receipt := range state.Seen {
			if now.Sub(receipt.At) > 48*time.Hour {
				delete(state.Seen, id)
			}
		}
		saved, e := s.Store.Save(r.Context(), m.Chat.ID, old, state)
		if e != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		if !saved {
			continue
		}
		if callbackID != "" {
			_, _ = s.api(r.Context(), "answerCallbackQuery", map[string]any{"callback_query_id": callbackID})
		}
		if result.Notify {
			if e := s.deliver(r.Context(), m.Chat.ID, u.ID); e != nil {
				http.Error(w, "notification delivery failed", 503)
				return
			}
			w.WriteHeader(200)
		} else {
			s.reply(w, m.Chat.ID, result, u.ID, 0)
		}
		return
	}
	http.Error(w, "concurrent update; retry", 503)
}

// Persist progress so a Telegram retry resumes a partially delivered notification.
// A crash between sendMessage and saving progress can repeat the last message.
func (s *Server) deliver(ctx context.Context, chat, id int64) error {
	for {
		state, _, e := s.Store.Load(ctx, chat)
		if e != nil {
			return e
		}
		receipt, ok := state.Seen[id]
		if !ok {
			return fmt.Errorf("missing notification")
		}
		pages := splitPages(receipt.Result.Text)
		if receipt.Sent >= len(pages) {
			return nil
		}
		payload := map[string]any{"chat_id": chat, "text": pages[receipt.Sent], "parse_mode": "HTML"}
		if receipt.Result.PartyID != "" {
			payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Участвую", "callback_data": receipt.Result.PartyID}}}}
		}
		if _, e = s.api(ctx, "sendMessage", payload); e != nil {
			return e
		}
		sent := receipt.Sent
		saved := false
		for attempt := 0; attempt < 8; attempt++ {
			state, old, e := s.Store.Load(ctx, chat)
			if e != nil {
				return e
			}
			r := state.Seen[id]
			if r.Sent > sent {
				saved = true
				break
			}
			r.Sent = sent + 1
			state.Seen[id] = r
			saved, e = s.Store.Save(ctx, chat, old, state)
			if e != nil {
				return e
			}
			if saved {
				break
			}
		}
		if !saved {
			return fmt.Errorf("notification progress contention")
		}
	}
}
func (s *Server) reply(w http.ResponseWriter, chat int64, result Result, updateID int64, page int) {
	if result.Text == "" {
		w.WriteHeader(200)
		return
	}
	// Reply through the webhook response: Telegram executes sendMessage itself.
	pages := splitPages(result.Text)
	if page < 0 || page >= len(pages) {
		page = 0
	}
	payload := map[string]any{"method": "sendMessage", "chat_id": chat, "text": pages[page], "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true}}
	buttons := [][]map[string]string{}
	if result.PartyID != "" {
		buttons = append(buttons, []map[string]string{{"text": "Участвую", "callback_data": result.PartyID}})
	}
	if len(pages) > 1 {
		var row []map[string]string
		if page > 0 {
			row = append(row, map[string]string{"text": "←", "callback_data": fmt.Sprintf("page:%d:%d", updateID, page-1)})
		}
		if page+1 < len(pages) {
			row = append(row, map[string]string{"text": "→", "callback_data": fmt.Sprintf("page:%d:%d", updateID, page+1)})
		}
		buttons = append(buttons, row)
	}
	if len(buttons) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": buttons}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// Every generated line has balanced HTML tags. Keep lines intact when paginating.
func splitPages(text string) []string {
	pages := []string{}
	current := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		if len(current)+len(line) > 3500 && current != "" {
			pages = append(pages, current)
			current = ""
		}
		current += line
	}
	if current != "" {
		pages = append(pages, current)
	}
	return pages
}
