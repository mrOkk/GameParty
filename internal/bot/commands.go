package bot

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

const Help = `GameParty — команды в группе:
/register Europe/Moscow | cs2, Dota 2 — регистрация или обновление своего профиля; список игр заменяется
/profile — мой профиль
/games — каталог
/addgame Название — добавить игру
/alias Название игры | алиас, другой алиас — только администратор
/ready Игра | 23:00 — создать сбор или указать готовность в сегодняшнем сборе UTC
/join ID — присоединиться с предложенным временем
/ready #ID | 01:00 — своё время в конкретном сборе
/party ID — состав и время в своём поясе
/party ID all — время во всех поясах группы
/parties — активные сборы
/invite ID — пинг отслеживающих игру
/ping ID — пора собираться, пинг всех участников
Вместо HH:MM можно указать YYYY-MM-DD HH:MM. Без даты — ближайшее будущее время в вашем поясе. ID указан в карточке сбора.`

type Input struct {
	UpdateID, UserID int64
	Name, Text       string
	Admin            bool
}
type Result struct {
	Text    string
	PartyID string
	Notify  bool
}

func (s *State) Apply(in Input, now time.Time) (Result, error) {
	fail := func(v string) (Result, error) { return Result{}, fmt.Errorf("%s", v) }
	if len(in.Text) > 3000 {
		return fail("Слишком длинная команда. Разделите её на несколько запросов.")
	}
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	fields := strings.Fields(in.Text)
	if len(fields) == 0 {
		return Result{}, nil
	}
	cmd := strings.Split(fields[0], "@")[0]
	arg := strings.TrimSpace(strings.TrimPrefix(in.Text, fields[0]))
	parts := strings.SplitN(arg, "|", 2)
	ok := func(v string) (Result, error) { return Result{Text: v}, nil }
	switch cmd {
	case "/start", "/help":
		return ok(Help)
	case "/games":
		var lines []string
		for _, k := range sortedKeys(s.Games) {
			line := s.Games[k]
			var aliases []string
			for _, a := range sortedKeys(s.Aliases) {
				if s.Aliases[a] == k {
					aliases = append(aliases, a)
				}
			}
			lines = append(lines, html.EscapeString(line))
			for _, alias := range aliases {
				lines = append(lines, "  · "+html.EscapeString(alias))
			}
		}
		return ok(strings.Join(lines, "\n"))
	case "/addgame":
		arg = strings.Join(strings.Fields(arg), " ")
		if len(arg) == 0 || len(arg) > 100 {
			return fail("Укажите название игры до 100 байт")
		}
		if s.game(arg) != "" {
			return fail("Игра или алиас уже существует")
		}
		s.Games[norm(arg)] = arg
		return ok("Игра добавлена: " + html.EscapeString(arg))
	case "/alias":
		if !in.Admin {
			return fail("Алиасы назначает администратор группы")
		}
		if len(parts) != 2 {
			return fail("/alias Игра | алиас, другой алиас")
		}
		g := s.game(parts[0])
		if g == "" {
			return fail("Сначала добавьте игру через /addgame")
		}
		aliases := strings.Split(parts[1], ",")
		for _, a := range aliases {
			k := norm(a)
			if k == "" || len(k) > 100 {
				return fail("Некорректный алиас")
			}
			if existing := s.game(k); existing != "" && existing != g {
				return fail("Алиас уже занят другой игрой: " + k)
			}
		}
		for _, a := range aliases {
			s.Aliases[norm(a)] = g
		}
		return ok("Алиасы сохранены")
	case "/register":
		if len(parts) != 2 {
			return fail("/register Europe/Moscow | cs2, Dota 2")
		}
		zone := strings.TrimSpace(parts[0])
		if _, e := time.LoadLocation(zone); e != nil || zone == "Local" {
			return fail("Укажите часовой пояс IANA, например Europe/Moscow")
		}
		games := map[string]bool{}
		for _, v := range strings.Split(parts[1], ",") {
			if strings.TrimSpace(v) == "" {
				continue
			}
			g := s.game(v)
			if g == "" {
				return fail("В списке есть неизвестная игра. Используйте /games или /addgame")
			}
			games[g] = true
		}
		s.Profiles[in.UserID] = Profile{in.Name, zone, games}
		for id, p := range s.Parties {
			if !now.Before(p.expires()) {
				delete(s.Parties, id)
			}
		}
		for key := range s.Days {
			if strings.SplitN(key, ":", 2)[0] < now.UTC().Format("2006-01-02") {
				delete(s.Days, key)
			}
		}
		return ok("Профиль сохранён. Часовой пояс: " + html.EscapeString(zone))
	}
	profile, registered := s.Profiles[in.UserID]
	if !registered {
		return fail("Сначала зарегистрируйтесь: /register Europe/Moscow | cs2")
	}
	switch cmd {
	case "/profile":
		var games []string
		for _, g := range sortedKeys(profile.Games) {
			games = append(games, s.Games[g])
		}
		return ok("Часовой пояс: " + html.EscapeString(profile.Zone) + "\nИгры: " + html.EscapeString(strings.Join(games, ", ")))
	case "/parties":
		var lines []string
		for _, id := range sortedKeys(s.Parties) {
			if p := s.active(id, now); p != nil {
				lines = append(lines, fmt.Sprintf("%s · <code>%s</code> · %d участников", html.EscapeString(s.Games[p.Game]), id, len(p.Members)))
			}
		}
		if len(lines) == 0 {
			return ok("Активных сборов нет")
		}
		return ok(strings.Join(lines, "\n"))
	case "/ready":
		if len(parts) != 2 {
			return fail("/ready Игра | HH:MM или /ready #ID | HH:MM")
		}
		t, e := ParseTime(strings.TrimSpace(parts[1]), profile.Zone, now)
		if e != nil {
			return Result{}, e
		}
		target := strings.TrimSpace(parts[0])
		var p *Party
		if strings.HasPrefix(target, "#") {
			p = s.active(strings.TrimPrefix(target, "#"), now)
			if p == nil {
				return fail("Сбор не найден или истёк")
			}
		} else {
			g := s.game(target)
			if g == "" {
				return fail("Неизвестная игра. Используйте /games или /addgame")
			}
			key := now.UTC().Format("2006-01-02") + ":" + g
			if id, exists := s.Days[key]; exists {
				p = s.active(id, now)
				if p == nil {
					return fail("Сегодняшний сбор уже завершён. Новый доступен после 00:00 UTC")
				}
			} else {
				id := strconv.FormatInt(in.UpdateID, 36)
				p = &Party{id, g, t, map[int64]time.Time{}}
				s.Parties[id] = p
				s.Days[key] = id
			}
		}
		p.Members[in.UserID] = t
		return Result{Text: s.render(p, in.UserID, false), PartyID: p.ID}, nil
	case "/join", "/party", "/invite", "/ping":
		args := strings.Fields(arg)
		if len(args) == 0 {
			return fail("Укажите ID сбора из /parties")
		}
		p := s.active(strings.TrimPrefix(args[0], "#"), now)
		if p == nil {
			return fail("Сбор не найден или истёк")
		}
		switch cmd {
		case "/join":
			if _, exists := p.Members[in.UserID]; !exists {
				p.Members[in.UserID] = p.Proposed
			}
			return Result{Text: s.render(p, in.UserID, false), PartyID: p.ID}, nil
		case "/party":
			return Result{Text: s.render(p, in.UserID, len(args) > 1 && args[1] == "all"), PartyID: p.ID}, nil
		case "/ping", "/invite":
			if _, member := p.Members[in.UserID]; !member {
				return fail("Пинг доступен участникам этого сбора")
			}
			text := "Пора собираться!"
			if cmd == "/invite" {
				text = "Собираем пати!"
			}
			var ids []string
			for id, u := range s.Profiles {
				if cmd == "/invite" && u.Games[p.Game] {
					ids = append(ids, mention(id, u))
				} else if cmd == "/ping" {
					if _, yes := p.Members[id]; yes {
						ids = append(ids, mention(id, u))
					}
				}
			}
			text += "\n" + strings.Join(ids, "\n") + "\n" + s.render(p, in.UserID, true)
			return Result{Text: text, PartyID: p.ID, Notify: true}, nil
		}
	}
	return fail("Неизвестная команда. /help")
}
