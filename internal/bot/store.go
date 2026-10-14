package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Store interface {
	Load(context.Context, int64) (*State, string, error)
	Save(context.Context, int64, string, *State) (bool, error)
}
type Redis struct {
	URL, Token string
	Client     *http.Client
}

func (r *Redis) call(ctx context.Context, args ...any) (json.RawMessage, error) {
	b, e := json.Marshal(args)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", r.URL, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	res, e := r.Client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("storage connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("storage HTTP %d", res.StatusCode)
	}
	var out struct {
		Result json.RawMessage
		Error  string
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); e != nil {
		return nil, e
	}
	if out.Error != "" {
		return nil, fmt.Errorf("storage command failed")
	}
	return out.Result, nil
}
func key(chat int64) string { return fmt.Sprintf("gameparty:v1:%d", chat) }
func (r *Redis) Load(ctx context.Context, chat int64) (*State, string, error) {
	v, e := r.call(ctx, "GET", key(chat))
	if e != nil {
		return nil, "", e
	}
	s := NewState()
	if string(v) == "null" {
		return s, "", nil
	}
	var raw string
	if e = json.Unmarshal(v, &raw); e != nil {
		return nil, "", e
	}
	if e = json.Unmarshal([]byte(raw), s); e != nil {
		return nil, "", e
	}
	return s, raw, nil
}
func (r *Redis) Save(ctx context.Context, chat int64, old string, s *State) (bool, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return false, e
	}
	v, e := r.call(ctx, "EVAL", `local v=redis.call('GET',KEYS[1]) or ''; if v~=ARGV[1] then return 0 end; redis.call('SET',KEYS[1],ARGV[2]); return 1`, 1, key(chat), old, string(b))
	return string(v) == "1", e
}
