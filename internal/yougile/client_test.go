package yougile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/exomind-tmi/yougile-mcp/internal/fakeapi"
)

func newClient(t *testing.T, fake *fakeapi.Server, key string) *Client {
	t.Helper()
	c, err := New(fake.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	c.Sleep = func(time.Duration) {}
	return c
}

func TestPagedFollowsNextAndStopsAtTheLimit(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	old := pageSize
	pageSize = 1
	defer func() { pageSize = old }()
	c := newClient(t, fake, fakeapi.Key)

	type task struct{ ID string }
	all, err := Paged[task](context.Background(), c, "/tasks", 10, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("got %v %v", all, err)
	}
	if got := fake.Count("/tasks"); got != 3 {
		t.Errorf("three pages of one task: %d requests", got)
	}
	two, _ := Paged[task](context.Background(), c, "/tasks", 2, nil)
	if len(two) != 2 {
		t.Errorf("limit 2 gave %d", len(two))
	}
}

func TestRetriesOn429And5xxThenSucceeds(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	fake.Fail("/users", 429, 503)
	var out any
	if err := newClient(t, fake, fakeapi.Key).Call(context.Background(), "GET", "/users", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if fake.Count("/users") != 3 {
		t.Errorf("%d requests", fake.Count("/users"))
	}
}

func TestGivesUpAfterTheRetriesWithAReadableError(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	fake.Fail("/users", 500, 500, 500, 500, 500)
	err := newClient(t, fake, fakeapi.Key).Call(context.Background(), "GET", "/users", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || fake.Count("/users") != 4 {
		t.Fatalf("err=%v requests=%d", err, fake.Count("/users"))
	}
}

func TestAWrongKeyTellsHowToGetANewOneAndNeverEchoesIt(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	err := newClient(t, fake, "wrong-key").Call(context.Background(), "GET", "/users", nil, nil, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 401 || !strings.Contains(err.Error(), "yougile-mcp add") || strings.Contains(err.Error(), "wrong-key") {
		t.Fatalf("err=%v", err)
	}
}

func TestARedirectIsNotFollowedSoTheKeyCannotLeave(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	fake.RedirectUsers("http://127.0.0.1:1/steal")
	err := newClient(t, fake, fakeapi.Key).Call(context.Background(), "GET", "/users", nil, nil, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 302 {
		t.Fatalf("err=%v", err)
	}
}

func TestPlainHTTPToARemoteHostIsRejected(t *testing.T) {
	if _, err := New("http://example.com", "k"); err == nil {
		t.Fatal("accepted http://example.com")
	}
	if _, err := New("https://ru.yougile.com", "k"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchStaysOnTheHostAndHonoursTheLimit(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	c := newClient(t, fake, fakeapi.Key)
	if data, kind, err := c.Fetch(context.Background(), "/files/pic.png", 1000); err != nil || kind != "image/png" || len(data) == 0 {
		t.Fatalf("%v %q %v", data, kind, err)
	}
	if _, _, err := c.Fetch(context.Background(), "/files/big.bin", 1000); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("limit not enforced: %v", err)
	}
	if _, _, err := c.Fetch(context.Background(), "https://evil.example/x", 1000); err == nil {
		t.Fatal("foreign host accepted")
	}
}

func TestLoginCallsCarryNoAuthorization(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	c := newClient(t, fake, "")
	companies, err := c.Companies(context.Background(), "me", "pw")
	if err != nil || len(companies) != 2 {
		t.Fatalf("%v %v", companies, err)
	}
	if key, err := c.NewKey(context.Background(), "me", "pw", "co1"); err != nil || key != "issued-key" {
		t.Fatalf("%q %v", key, err)
	}
	for _, r := range fake.Requests("") {
		if r.Auth != "" {
			t.Errorf("%s carried an Authorization header", r.Path)
		}
	}
}
