package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func date(v string) time.Time {
	t, e := time.Parse(time.RFC3339, v)
	if e != nil {
		panic(e)
	}
	return t
}
func apply(t *testing.T, s *State, id, user int64, text string, now time.Time) Result {
	t.Helper()
	r, e := s.Apply(Input{UpdateID: id, UserID: user, Name: fmt.Sprintf("User%d", user), Text: text}, now)
	if e != nil {
		t.Fatalf("%s: %v", text, e)
	}
	return r
}
func TestPartyLifecycle(t *testing.T) {
	s := NewState()
	now := date("2026-10-14T19:00:00Z")
	apply(t, s, 1, 1, "/register Europe/Moscow | cs", now)
	apply(t, s, 2, 2, "/register Europe/Berlin | cs2", now)
	first := apply(t, s, 3, 1, "/ready cs | 23:00", now)
	p := s.Parties[first.PartyID]
	apply(t, s, 4, 2, "/join "+p.ID, now)
	if !p.Members[2].Equal(p.Proposed) {
		t.Fatal("join must accept proposed time")
	}
	apply(t, s, 5, 2, "/ready #"+p.ID+" | 2026-10-15 03:00", now)
	if got := p.expires(); !got.Equal(date("2026-10-15T03:00:00Z")) {
		t.Fatalf("expiration: %s", got)
	}
	if len(s.Parties) != 1 {
		t.Fatal("aliases must share a party")
	}
	later := date("2026-10-15T00:05:00Z")
	second := apply(t, s, 6, 1, "/ready cs2 | 04:00", later)
	if second.PartyID == first.PartyID || s.active(first.PartyID, later) == nil {
		t.Fatal("old and new daily parties should coexist")
	}
	apply(t, s, 7, 1, "/ready #"+first.PartyID+" | 05:00", later)
	if !p.Members[1].Equal(date("2026-10-15T02:00:00Z")) {
		t.Fatal("explicit old party must be updated")
	}
	if s.active(first.PartyID, p.expires()) != nil {
		t.Fatal("expiration boundary must be exclusive")
	}
	if _, e := s.Apply(Input{UserID: 1, Text: "/join " + p.ID}, p.expires()); e == nil {
		t.Fatal("expired join accepted")
	}
	apply(t, s, 8, 1, "/register Europe/Moscow | cs2", p.expires())
	if s.Parties[first.PartyID] != nil {
		t.Fatal("registration should clean expired parties")
	}
	if len(s.Profiles) != 2 {
		t.Fatal("cleanup removed profiles")
	}
}
func TestNextLocalTime(t *testing.T) {
	for _, tc := range []struct{ now, input, zone, want string }{
		{"2026-10-14T19:00:00Z", "23:00", "Europe/Moscow", "2026-10-14T20:00:00Z"},
		{"2026-10-14T20:00:00Z", "01:00", "Europe/Moscow", "2026-10-14T22:00:00Z"},
		{"2026-10-14T22:30:00Z", "01:00", "Europe/Moscow", "2026-10-15T22:00:00Z"},
		{"2026-10-14T12:00:00Z", "2026-10-16 01:00", "Asia/Yekaterinburg", "2026-10-15T20:00:00Z"},
		{"2026-10-25T00:45:00Z", "02:30", "Europe/Berlin", "2026-10-25T01:30:00Z"},
	} {
		got, e := ParseTime(tc.input, tc.zone, date(tc.now))
		if e != nil || !got.Equal(date(tc.want)) {
			t.Errorf("%+v got %s %v", tc, got, e)
		}
	}
	for _, v := range []string{"25:00", "tomorrow", "2026-10-13 01:00"} {
		if _, e := ParseTime(v, "UTC", date("2026-10-14T12:00:00Z")); e == nil {
			t.Errorf("accepted %s", v)
		}
	}
	if _, e := ParseTime("2026-03-29 02:30", "Europe/Berlin", date("2026-03-28T12:00:00Z")); e == nil {
		t.Fatal("nonexistent DST time accepted")
	}
}
func TestPermissionsAndPings(t *testing.T) {
	s := NewState()
	now := date("2026-10-14T12:00:00Z")
	for i := int64(1); i <= 3; i++ {
		apply(t, s, i, i, "/register UTC | cs", now)
	}
	p := apply(t, s, 4, 1, "/ready cs | 23:00", now)
	apply(t, s, 5, 2, "/ready #"+p.PartyID+" | 23:30", now)
	if _, e := s.Apply(Input{UserID: 3, Text: "/ping " + p.PartyID}, now); e == nil {
		t.Fatal("nonmember ping accepted")
	}
	ping := apply(t, s, 6, 1, "/ping "+p.PartyID, now)
	if !ping.Notify || !strings.Contains(ping.Text, "id=2") || strings.Contains(ping.Text, "id=3") {
		t.Fatal("ping audience incorrect")
	}
	invite := apply(t, s, 7, 1, "/invite "+p.PartyID, now)
	if !strings.Contains(invite.Text, "id=3") {
		t.Fatal("watcher missing")
	}
	if _, e := s.Apply(Input{UserID: 1, Text: "/alias cs | кс"}, now); e == nil {
		t.Fatal("nonadmin alias accepted")
	}
	if _, e := s.Apply(Input{UserID: 1, Text: "/alias cs | кс", Admin: true}, now); e != nil {
		t.Fatal(e)
	}
	if s.game("КС") != s.game("cs") {
		t.Fatal("alias mismatch")
	}
	if _, e := s.Apply(Input{UserID: 1, Text: "/alias cs | available, Dota 2", Admin: true}, now); e == nil {
		t.Fatal("conflicting alias accepted")
	}
	if s.game("available") != "" {
		t.Fatal("partial alias update")
	}
}
func TestClosedDayCannotBeRecreated(t *testing.T) {
	s := NewState()
	now := date("2026-10-14T01:00:00Z")
	apply(t, s, 1, 1, "/register UTC | cs", now)
	apply(t, s, 2, 1, "/ready cs | 02:00", now)
	now = now.Add(5 * time.Hour)
	apply(t, s, 3, 1, "/register UTC | cs", now)
	if _, e := s.Apply(Input{UserID: 1, Text: "/ready cs | 23:00"}, now); e == nil {
		t.Fatal("second same-day party accepted after cleanup")
	}
}

type memoryStore struct {
	mu       sync.Mutex
	data     map[int64]string
	conflict bool
}

func (m *memoryStore) Load(_ context.Context, id int64) (*State, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := NewState()
	raw := m.data[id]
	if raw != "" {
		if e := json.Unmarshal([]byte(raw), s); e != nil {
			return nil, "", e
		}
	}
	return s, raw, nil
}
func (m *memoryStore) Save(_ context.Context, id int64, old string, s *State) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflict {
		m.conflict = false
		return false, nil
	}
	if m.data[id] != old {
		return false, nil
	}
	b, e := json.Marshal(s)
	m.data[id] = string(b)
	return true, e
}
func request(s *Server, body, secret string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func update(id int, text string) string {
	b, _ := json.Marshal(map[string]any{"update_id": id, "message": map[string]any{"from": map[string]any{"id": 1, "first_name": "<Oleg>"}, "chat": map[string]any{"id": -1, "type": "supergroup"}, "text": text}})
	return string(b)
}
func TestWebhookSecurityIsolationAndRetry(t *testing.T) {
	store := &memoryStore{data: map[int64]string{}, conflict: true}
	s := &Server{Store: store, Secret: "secret", Now: func() time.Time { return date("2026-10-14T12:00:00Z") }}
	if w := request(s, update(1, "/register UTC | cs"), "bad"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if len(store.data) != 0 {
		t.Fatal("unauthorized write")
	}
	if w := request(s, update(1, "/register UTC | cs"), "secret"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	request(s, update(2, "/ready cs | 23:00"), "secret")
	before := store.data[-1]
	w := request(s, update(2, "/ready cs | 23:00"), "secret")
	if w.Code != 200 || w.Body.Len() != 0 || store.data[-1] != before {
		t.Fatal("duplicate update changed state")
	}
	other, _, _ := store.Load(context.Background(), -2)
	if len(other.Profiles) != 0 {
		t.Fatal("group profile leaked")
	}
	w = request(s, update(3, "/party 2"), "secret")
	if !strings.Contains(w.Body.String(), `\u0026lt;Oleg\u0026gt;`) {
		t.Fatal("user name not escaped", w.Body.String())
	}
}
func TestNotificationResumes(t *testing.T) {
	calls := 0
	failed := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !failed {
			failed = true
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer api.Close()
	store := &memoryStore{data: map[int64]string{}}
	s := &Server{Store: store, Secret: "secret", Client: api.Client(), APIBase: api.URL, Now: func() time.Time { return date("2026-10-14T12:00:00Z") }}
	request(s, update(1, "/register UTC | cs"), "secret")
	request(s, update(2, "/ready cs | 23:00"), "secret")
	if w := request(s, update(3, "/ping 2"), "secret"); w.Code != 503 {
		t.Fatal("expected retry on delivery failure", w.Code)
	}
	if w := request(s, update(3, "/ping 2"), "secret"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	request(s, update(3, "/ping 2"), "secret")
	if calls != 2 {
		t.Fatal("delivered ping repeated", calls)
	}
}
func TestRedisCASProtocol(t *testing.T) {
	raw := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing auth")
		}
		var args []any
		if e := json.NewDecoder(r.Body).Decode(&args); e != nil {
			t.Error(e)
		}
		var result any
		switch args[0] {
		case "GET":
			if raw != "" {
				result = raw
			}
		case "EVAL":
			if args[4] == raw {
				raw = args[5].(string)
				result = 1
			} else {
				result = 0
			}
		default:
			t.Error(args)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	defer server.Close()
	redis := &Redis{server.URL, "test", server.Client()}
	ctx := context.Background()
	s, old, e := redis.Load(ctx, -1)
	if e != nil || old != "" {
		t.Fatal(e)
	}
	if ok, e := redis.Save(ctx, -1, old, s); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, e := redis.Save(ctx, -1, old, s); ok || e != nil {
		t.Fatal("stale CAS accepted", e)
	}
	loaded, _, e := redis.Load(ctx, -1)
	if e != nil || loaded.game("cs") == "" {
		t.Fatal("roundtrip failed", e)
	}
}

func TestJoinButtonAndPagination(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":true}`) }))
	defer api.Close()
	store := &memoryStore{data: map[int64]string{}}
	s := &Server{Store: store, Secret: "secret", Client: api.Client(), APIBase: api.URL, Now: func() time.Time { return date("2026-10-14T12:00:00Z") }}
	request(s, update(1, "/register UTC | cs"), "secret")
	request(s, update(2, "/ready cs | 23:00"), "secret")
	b := `{"update_id":3,"callback_query":{"id":"cb","from":{"id":1,"first_name":"User"},"data":"2","message":{"from":{"id":999,"is_bot":true},"chat":{"id":-1,"type":"supergroup"}}}}`
	w := request(s, b, "secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Участвую") {
		t.Fatal(w.Body.String())
	}
	state, _, _ := store.Load(context.Background(), -1)
	if len(state.Parties["2"].Members) != 1 {
		t.Fatal("button changed author or duplicated membership")
	}
	text := strings.Repeat("<b>строка</b>\n", 800)
	pages := splitPages(text)
	if len(pages) < 2 {
		t.Fatal("pagination missing")
	}
	if strings.Join(pages, "") != text {
		t.Fatal("lost text")
	}
	for _, p := range pages {
		if len(p) > 3500 || strings.Count(p, "<b>") != strings.Count(p, "</b>") {
			t.Fatal("invalid page")
		}
	}
	_, old, _ := store.Load(context.Background(), -1)
	state.Seen[4] = Receipt{At: s.Now(), Result: Result{Text: text}}
	if ok, e := store.Save(context.Background(), -1, old, state); !ok || e != nil {
		t.Fatal(ok, e)
	}
	b = strings.ReplaceAll(b, `"update_id":3`, `"update_id":5`)
	b = strings.ReplaceAll(b, `"data":"2"`, `"data":"page:4:1"`)
	w = request(s, b, "secret")
	var payload struct{ Text string }
	if e := json.Unmarshal(w.Body.Bytes(), &payload); e != nil {
		t.Fatal(e)
	}
	if payload.Text != pages[1] {
		t.Fatal("wrong page")
	}
}
