// Package fakeapi is a local stand-in for the YouGile REST API, for tests. It records every request.
package fakeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Key is the only API key the fake accepts.
const Key = "test-key"

type obj = map[string]any

var tasks = []obj{
	{"id": "t1", "idTaskProject": 7, "title": "Broken <b>login</b>", "columnId": "c1", "assigned": []string{"u1"},
		"timestamp": 1760000000000, "description": `<p>Button &amp; form</p><p>fails</p><img src="/files/pic.png">`},
	{"id": "t2", "idTaskProject": 8, "title": "Other", "columnId": "c1", "description": ""},
	{"id": "t3", "idTaskProject": 9, "title": "In column two", "columnId": "c2", "description": ""},
}

var messages = []obj{
	{"id": 1760000002000, "fromUserId": "u1", "text": "second", "textHtml": "<p>second</p>"},
	{"id": 1760000001000, "fromUserId": "u1", "text": "first\n/root/#file:/files/report.pdf",
		"textHtml": `<p>first</p><p><a href="/files/report.pdf">report</a></p>`},
	{"id": 1760000003000, "fromUserId": "u1", "text": "gone", "deleted": true},
}

type file struct {
	data []byte
	mime string
}

var files = map[string]file{
	"/files/pic.png":    {[]byte("\x89PNG-data"), "image/png"},
	"/files/report.pdf": {[]byte("%PDF-1.4 data"), "application/pdf"},
	"/files/big.bin":    {make([]byte, 2000), "application/octet-stream"},
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
	case r.Header.Get("Authorization") != "Bearer "+Key:
		reply(w, 401, obj{"error": "Unauthorized"})
	default:
		s.route(w, r, route, body)
	}
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, route string, body []byte) {
	q := r.URL.Query()
	switch {
	case route == "/users":
		reply(w, 200, obj{"content": []obj{{"id": "u1", "realName": "Ann", "email": "ann@example.com"}}, "paging": obj{"next": false}})
	case route == "/boards":
		reply(w, 200, obj{"content": []obj{{"id": "b1", "title": "Board", "projectId": "p1"}}, "paging": obj{"next": false}})
	case route == "/columns":
		reply(w, 200, obj{"content": []obj{{"id": "c1", "title": "New"}, {"id": "c2", "title": "Done"}}, "paging": obj{"next": false}})
	case strings.HasPrefix(route, "/files/"):
		f, ok := files[route]
		if !ok {
			reply(w, 404, obj{"error": "no file"})
			return
		}
		w.Header().Set("Content-Type", f.mime)
		w.Write(f.data)
	case route == "/tasks" && r.Method == http.MethodGet:
		var items []obj
		for _, t := range tasks {
			if col := q.Get("columnId"); col == "" || col == t["columnId"] {
				items = append(items, t)
			}
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		end := min(len(items), offset+limit)
		reply(w, 200, obj{"content": items[min(offset, end):end], "paging": obj{"next": end < len(items)}})
	case strings.HasPrefix(route, "/tasks/") && r.Method == http.MethodGet:
		for _, t := range tasks {
			if t["id"] == strings.TrimPrefix(route, "/tasks/") {
				reply(w, 200, t)
				return
			}
		}
		reply(w, 404, obj{"error": "no task"})
	case strings.HasPrefix(route, "/chats/") && strings.HasSuffix(route, "/messages") && r.Method == http.MethodGet:
		reply(w, 200, obj{"content": messages})
	case route == "/upload-file":
		reply(w, 200, obj{"url": "/files/pic.png"})
	default:
		reply(w, 200, obj{"id": 1})
	}
}
