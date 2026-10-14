package bot

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

type Profile struct {
	Name, Zone string
	Games      map[string]bool
}
type Party struct {
	ID, Game string
	Proposed time.Time
	Members  map[int64]time.Time
}
type State struct {
	Profiles map[int64]Profile
	Games    map[string]string
	Aliases  map[string]string
	Parties  map[string]*Party
	Days     map[string]string
	Seen     map[int64]Receipt
}

type Receipt struct {
	At     time.Time
	Result Result
	Sent   int
}

func NewState() *State {
	s := &State{map[int64]Profile{}, map[string]string{}, map[string]string{}, map[string]*Party{}, map[string]string{}, map[int64]Receipt{}}
	for _, g := range []string{"Counter-Strike 2", "Dota 2", "Valorant", "League of Legends", "Minecraft", "Apex Legends", "Fortnite", "Overwatch 2", "Deadlock"} {
		s.Games[norm(g)] = g
	}
	for _, a := range []string{"cs", "cs2", "counter-strike", "контра"} {
		s.Aliases[a] = norm("Counter-Strike 2")
	}
	return s
}
func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
func (s *State) game(v string) string {
	k := norm(v)
	if a, ok := s.Aliases[k]; ok {
		k = a
	}
	if _, ok := s.Games[k]; !ok {
		return ""
	}
	return k
}
func (p *Party) expires() time.Time {
	t := time.Time{}
	for _, v := range p.Members {
		if v.After(t) {
			t = v
		}
	}
	return t.Add(2 * time.Hour)
}
func (s *State) active(id string, now time.Time) *Party {
	p := s.Parties[id]
	if p == nil || !now.Before(p.expires()) {
		return nil
	}
	return p
}
func ParseTime(v, zone string, now time.Time) (time.Time, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, fmt.Errorf("Неизвестный часовой пояс")
	}
	local := now.In(loc)
	if len(v) == 5 {
		clock, e := time.Parse("15:04", v)
		if e != nil {
			return time.Time{}, fmt.Errorf("Время: HH:MM или YYYY-MM-DD HH:MM")
		}
		for day := 0; day < 2; day++ {
			d := local.AddDate(0, 0, day)
			candidates := localTimes(d.Year(), d.Month(), d.Day(), clock.Hour(), clock.Minute(), loc)
			if len(candidates) == 0 {
				return time.Time{}, fmt.Errorf("Это время отсутствует из-за перевода часов; укажите другую дату и время")
			}
			for _, t := range candidates {
				if t.After(now) {
					return t, nil
				}
			}
		}
		return time.Time{}, fmt.Errorf("Укажите дату и время")
	}
	t, e := time.ParseInLocation("2006-01-02 15:04", v, loc)
	if e != nil || t.Format("2006-01-02 15:04") != v {
		return time.Time{}, fmt.Errorf("Время: HH:MM или YYYY-MM-DD HH:MM")
	}
	for _, candidate := range localTimes(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), loc) {
		if candidate.After(now) {
			return candidate, nil
		}
	}
	return time.Time{}, fmt.Errorf("Укажите будущее время")
}

// During a clock rollback a wall-clock minute can occur twice. Choose the
// nearest future occurrence, including the second one when the first has passed.
func localTimes(year int, month time.Month, day, hour, minute int, loc *time.Location) []time.Time {
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, offset := wall.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[offset] = true
	}
	var result []time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		v := candidate.In(loc)
		if v.Year() == year && v.Month() == month && v.Day() == day && v.Hour() == hour && v.Minute() == minute {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Before(result[j]) })
	return result
}
func mention(id int64, p Profile) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, id, html.EscapeString(p.Name))
}
func sortedKeys[V any](m map[string]V) []string {
	r := make([]string, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}
func (s *State) render(p *Party, viewer int64, all bool) string {
	zones := map[string]bool{}
	if all {
		for _, u := range s.Profiles {
			zones[u.Zone] = true
		}
	} else {
		zones[s.Profiles[viewer].Zone] = true
	}
	if len(zones) == 0 {
		zones["UTC"] = true
	}
	out := fmt.Sprintf("<b>%s</b> · сбор <code>%s</code>\n", html.EscapeString(s.Games[p.Game]), p.ID)
	ids := make([]int64, 0, len(p.Members))
	for id := range p.Members {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := p.Members[ids[i]], p.Members[ids[j]]
		if a.Equal(b) {
			return ids[i] < ids[j]
		}
		return a.Before(b)
	})
	for _, zone := range sortedKeys(zones) {
		loc, e := time.LoadLocation(zone)
		if e != nil {
			continue
		}
		out += "\n" + html.EscapeString(zone) + " · предложено " + p.Proposed.In(loc).Format("02.01 15:04") + "\n"
		for _, id := range ids {
			out += mention(id, s.Profiles[id]) + " — " + p.Members[id].In(loc).Format("02.01 15:04") + "\n"
		}
	}
	return out
}
