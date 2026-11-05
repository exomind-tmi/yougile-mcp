// Package fakeapi is a local stand-in for the YouGile REST API, for tests. It records every request.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Key is the only API key the fake accepts.
const Key = "test-key"

type obj = map[string]any

var tasks = []obj{
	{"id": "t1", "idTaskProject": "DEV-7", "idTaskCommon": "ID-107", "title": "Broken <b>login</b>", "columnId": "c1",
		"assigned": []string{"u1"}, "timestamp": 1760000000000, "subtasks": []string{"t2"},
		"description": `<p>Button &amp; form</p><p>fails</p><img src="/files/pic.png">`,
		"stickers":    obj{"s1": "st1", "s2": "ORB-42 order", "s3": []string{"sp1"}},
		"deadline":    obj{"deadline": 1760100000000, "startDate": 1760000000000, "withTime": true}},
	{"id": "t2", "idTaskProject": "DEV-8", "idTaskCommon": "ID-108", "title": "Other login", "columnId": "c1", "description": "",
		"stickers": obj{"s2": "ORB-43"}},
	{"id": "t3", "idTaskProject": "DEV-9", "idTaskCommon": "ID-109", "title": "In column two", "columnId": "c2",
		"description": "<p>" + strings.Repeat("long ", 100) + "</p>"},
}

var stickers = map[string][]obj{
	"/string-stickers": {
		{"id": "s1", "name": "Priority", "states": []obj{{"id": "st1", "name": "High"}, {"id": "st2", "name": "Low"}}},
		{"id": "s2", "name": "Order"},
	},
	"/sprint-stickers": {{"id": "s3", "name": "Sprint", "states": []obj{{"id": "sp1", "name": "Sprint 1"}}}},
}

var messages = []obj{
	{"id": 1760000002000, "fromUserId": "u1", "text": "second", "textHtml": "<p>second</p>"},
	{"id": 1760000001000, "fromUserId": "u1", "text": "first\n/root/#file:/files/report.pdf",
		"textHtml": `<p>first</p><p><a href="/files/report.pdf">report</a></p>`},
	{"id": 1760000003000, "fromUserId": "u1", "text": "gone", "deleted": true},
	{"id": 1760000004000, "fromUserId": "me", "text": "mine", "textHtml": "<p>mine</p>"},
}

type file struct {
	data []byte
	mime string
}

var files = map[string]file{
	"/files/pic.png":       {[]byte("\x89PNG-data"), "image/png"},
	"/files/report.pdf":    {[]byte("%PDF-1.4 data"), "application/pdf"},
	"/files/big.bin":       {make([]byte, 2000), "application/octet-stream"},
	"/files/Build log.txt": {[]byte("log"), "text/plain"},
}

// Request is one recorded call.
type Request struct {
	Method, Path, Query, Auth, Type string
	Body                            []byte
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []Request
	failures map[string][]int
	redirect string
	extra    []obj
}

// New starts a fake API; call Close when done.
func New() *Server {
	s := &Server{failures: map[string][]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Fail makes the next calls of a route answer with the given statuses.
func (s *Server) Fail(route string, statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[route] = statuses
}

// ManyTasks adds n tasks with the given description to a column, for size limits.
func (s *Server) ManyTasks(column string, n int, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range n {
		s.extra = append(s.extra, obj{"id": fmt.Sprintf("m%d", i), "title": "Many", "columnId": column, "description": description})
	}
}

// RedirectUsers makes /users answer with a redirect.
func (s *Server) RedirectUsers(to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redirect = to
}

// Requests returns the recorded calls with this method (empty method: all).
func (s *Server) Requests(method string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests {
		if method == "" || r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// Count is the number of recorded calls to a path ending with suffix.
func (s *Server) Count(suffix string) int {
	n := 0
	for _, r := range s.Requests("") {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

func reply(w http.ResponseWriter, status int, v any, headers ...string) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		w.Header().Set(headers[i], headers[i+1])
	}
	w.WriteHeader(status)
	w.Write(raw)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"),
		r.Header.Get("Content-Type"), body})
	route := strings.TrimPrefix(r.URL.Path, "/api-v2")
	redirect := s.redirect
	var failure int
	if queue := s.failures[route]; len(queue) > 0 {
		failure, s.failures[route] = queue[0], queue[1:]
	}
	s.mu.Unlock()

	switch {
	case redirect != "" && route == "/users":
		w.Header().Set("Location", redirect)
		w.WriteHeader(http.StatusFound)
	case failure != 0:
		reply(w, failure, obj{"error": "boom"}, "Retry-After", "0")
	case route == "/auth/companies":
		reply(w, 200, obj{"content": []obj{{"id": "co1", "name": "Acme"}, {"id": "co2", "name": "Beta Corp"}}})
	case route == "/auth/keys":
		reply(w, 200, obj{"key": "issued-key"})
	case strings.HasPrefix(r.URL.Path, "/user-data/away/"):
		// like YouGile, a file link redirects; this one leaves the site
		w.Header().Set("Location", "https://elsewhere.example/x.png")
		w.WriteHeader(http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/user-data/"):
		w.Header().Set("Location", "/files/"+strings.TrimPrefix(r.URL.Path, "/user-data/"))
		w.WriteHeader(http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/files/"):
		// files are served without the key, as user-data.yougile.com does
		f, ok := files[r.URL.Path]
		if !ok {
			reply(w, 404, obj{"error": "no file"})
			return
		}
		w.Header().Set("Content-Type", f.mime)
		w.Write(f.data)
	case r.Header.Get("Authorization") != "Bearer "+Key:
		reply(w, 401, obj{"error": "Unauthorized"})
	default:
		s.route(w, r, route, body)
	}
}

// page answers one page of items as a list endpoint does.
func page(w http.ResponseWriter, q url.Values, items []obj) {
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit == 0 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	end := min(len(items), offset+limit)
	reply(w, 200, obj{"content": items[min(offset, end):end], "paging": obj{"next": end < len(items)}})
}

func matches(t obj, q url.Values) bool {
	if col := q.Get("columnId"); col != "" && col != t["columnId"] {
		return false
	}
	if title := q.Get("title"); title != "" && !strings.Contains(strings.ToLower(t["title"].(string)), strings.ToLower(title)) {
		return false
	}
	if who := q.Get("assignedTo"); who != "" {
		assigned, _ := t["assigned"].([]string)
		if !slices.Contains(assigned, who) {
			return false
		}
	}
	if id := q.Get("stickerId"); id != "" {
		set, _ := t["stickers"].(obj)
		value, ok := set[id]
		if !ok || (q.Get("stickerStateId") != "" && value != q.Get("stickerStateId")) {
			return false
		}
	}
	return true
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, route string, body []byte) {
	q := r.URL.Query()
	switch {
	case route == "/users/me":
		reply(w, 200, obj{"id": "me", "realName": "Me", "email": "me@example.com"})
	case route == "/users":
		page(w, q, []obj{{"id": "u1", "realName": "Ann", "email": "ann@example.com"}, {"id": "me", "realName": "Me", "email": "me@example.com"}})
	case route == "/projects":
		page(w, q, []obj{{"id": "p1", "title": "Project"}})
	case route == "/string-stickers" || route == "/sprint-stickers":
		page(w, q, stickers[route])
	case route == "/boards":
		reply(w, 200, obj{"content": []obj{{"id": "b1", "title": "Board", "projectId": "p1"}}, "paging": obj{"next": false}})
	case route == "/columns":
		reply(w, 200, obj{"content": []obj{{"id": "c1", "title": "New"}, {"id": "c2", "title": "Done"}}, "paging": obj{"next": false}})
	case route == "/task-list":
		var items []obj
		s.mu.Lock()
		all := append(slices.Clone(tasks), s.extra...)
		s.mu.Unlock()
		for _, t := range all {
			if matches(t, q) {
				items = append(items, t)
			}
		}
		page(w, q, items)
	case strings.HasPrefix(route, "/tasks/") && r.Method == http.MethodGet:
		for _, t := range tasks {
			if t["id"] == strings.TrimPrefix(route, "/tasks/") {
				reply(w, 200, t)
				return
			}
		}
		reply(w, 404, obj{"error": "no task"})
	case strings.HasPrefix(route, "/chats/") && strings.HasSuffix(route, "/messages") && r.Method == http.MethodGet:
		page(w, q, messages)
	case strings.HasPrefix(route, "/chats/") && strings.Contains(route, "/messages/") && r.Method == http.MethodGet:
		for _, m := range messages {
			if strings.HasSuffix(route, "/"+strconv.FormatInt(int64(m["id"].(int)), 10)) {
				reply(w, 200, m)
				return
			}
		}
		reply(w, 404, obj{"error": "no message"})
	case route == "/tasks" && r.Method == http.MethodPost:
		reply(w, 201, obj{"id": "t1"})
	case route == "/upload-file":
		reply(w, 200, obj{"url": "/files/pic.png"})
	default:
		reply(w, 200, obj{"id": 1})
	}
}
