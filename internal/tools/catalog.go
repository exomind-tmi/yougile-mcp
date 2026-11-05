package tools

import (
	"context"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/exomind-tmi/yougile-mcp/internal/yougile"
)

// cached keeps one value of a company-wide list for catalogTTL.
type cached[T any] struct {
	mu     sync.Mutex
	value  T
	loaded time.Time
}

func (c *cached[T]) get(load func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded.IsZero() && time.Since(c.loaded) < catalogTTL {
		return c.value, nil
	}
	v, err := load()
	if err != nil {
		var zero T
		return zero, err
	}
	c.value, c.loaded = v, time.Now()
	return v, nil
}

type apiUser struct {
	ID       string `json:"id"`
	RealName string `json:"realName"`
	Email    string `json:"email"`
}

func (s *session) userNames(ctx context.Context) (map[string]string, error) {
	return s.users.get(func() (map[string]string, error) {
		list, err := yougile.Paged[apiUser](ctx, s.client, "/users", 1000, nil)
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		for _, u := range list {
			names[u.ID] = firstNonEmpty(u.RealName, u.Email, u.ID)
		}
		return names, nil
	})
}

// me is the id of the user the key belongs to.
func (s *session) me(ctx context.Context) (string, error) {
	return s.self.get(func() (string, error) {
		var u apiUser
		if err := s.client.Call(ctx, "GET", "/users/me", nil, nil, &u); err != nil {
			return "", err
		}
		return u.ID, nil
	})
}

type apiState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiSticker struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	States []apiState `json:"states"`
	Kind   string     `json:"kind"`
}

// fetchStickers reads the custom stickers of both kinds: string stickers (with states or a free field)
// and sprint stickers.
func fetchStickers(ctx context.Context, c *yougile.Client, query url.Values) ([]apiSticker, error) {
	out := []apiSticker{}
	for _, k := range []struct{ kind, path string }{{"string", "/string-stickers"}, {"sprint", "/sprint-stickers"}} {
		list, err := yougile.Paged[apiSticker](ctx, c, k.path, 1000, query)
		if err != nil {
			return nil, err
		}
		for _, st := range list {
			st.Kind = k.kind
			if st.States == nil {
				st.States = []apiState{}
			}
			out = append(out, st)
		}
	}
	return out, nil
}

// stickers is the company's custom stickers by id.
func (s *session) stickers(ctx context.Context) (map[string]apiSticker, error) {
	return s.stickerSet.get(func() (map[string]apiSticker, error) {
		list, err := fetchStickers(ctx, s.client, nil)
		if err != nil {
			return nil, err
		}
		all := map[string]apiSticker{}
		for _, st := range list {
			all[st.ID] = st
		}
		return all, nil
	})
}

// stickerValues turns the stickers of a task (sticker id -> state id, state ids or a free value) into
// sticker name -> state name(s) or the value. Unknown ids stay as they are; a name used by two stickers
// gets the id added.
func stickerValues(raw map[string]any, catalog map[string]apiSticker) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	ids := make([]string, 0, len(raw))
	for id := range raw {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := map[string]any{}
	for _, id := range ids {
		st, known := catalog[id]
		name := id
		if known {
			name = st.Name
		}
		if _, taken := out[name]; taken {
			name += " (" + id + ")"
		}
		switch v := raw[id].(type) {
		case string:
			out[name] = stateName(st, v)
		case []any:
			out[name] = stateNames(st, v)
		default:
			out[name] = v
		}
	}
	return out
}

func stateNames(st apiSticker, values []any) []string {
	names := []string{}
	for _, item := range values {
		if s, ok := item.(string); ok {
			names = append(names, stateName(st, s))
		}
	}
	return names
}

func stateName(st apiSticker, value string) string {
	if value == "empty" && len(st.States) > 0 {
		return ""
	}
	for _, state := range st.States {
		if state.ID == value {
			return state.Name
		}
	}
	return value
}
