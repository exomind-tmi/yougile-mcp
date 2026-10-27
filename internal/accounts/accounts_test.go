package accounts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exomind-tmi/yougile-mcp/internal/fakeapi"
)

type script struct {
	answers []string
	secret  string
}

func (s *script) Ask(string) (string, error) {
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}
func (s *script) Secret(string) (string, error) { return s.secret, nil }

func TestAddSavesAKeyPerChosenCompanyUnderAliasesAndNeverThePassword(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	store := Store{Path: filepath.Join(t.TempDir(), "state", "accounts.json")}
	var out bytes.Buffer
	// login, Enter = all companies, default alias for Acme, explicit alias for Beta
	p := &script{answers: []string{"me@example.com", "", "", "beta"}, secret: "hunter2"}
	if err := Add(context.Background(), store, fake.URL, p, &out); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load()
	if len(saved) != 2 || saved["acme"].Company != "Acme" || saved["beta"].Key != "issued-key" || saved["beta"].Host != fake.URL {
		t.Fatalf("saved: %+v", saved)
	}
	raw, _ := os.ReadFile(store.Path)
	if strings.Contains(string(raw), "hunter2") || strings.Contains(out.String(), "issued-key") || strings.Contains(out.String(), "hunter2") {
		t.Error("a secret leaked")
	}
	if info, _ := os.Stat(store.Path); info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(store.Path)); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", info.Mode().Perm())
	}
}

func TestAddCanSaveOnlyTheChosenCompany(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	store := Store{Path: filepath.Join(t.TempDir(), "accounts.json")}
	p := &script{answers: []string{"me", "1", ""}, secret: "x"}
	if err := Add(context.Background(), store, fake.URL, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := store.Load(); len(saved) != 1 || saved["beta-corp"].Company != "Beta Corp" {
		t.Fatalf("saved: %+v", saved)
	}
}

func TestAddRefusesAUsedAliasAndABadAlias(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	store := Store{Path: filepath.Join(t.TempDir(), "accounts.json")}
	store.Save(map[string]Account{"main": {Alias: "main", Key: "k"}})
	for alias, want := range map[string]string{"main": "exists", "Bad Alias": "bad alias"} {
		p := &script{answers: []string{"me", "0", alias}, secret: "x"}
		if err := Add(context.Background(), store, fake.URL, p, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("alias %q: %v", alias, err)
		}
	}
}

func TestListAndRemove(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "accounts.json")}
	store.Save(map[string]Account{"b": {Company: "B", Host: "h", Key: "secret-b"}, "a": {Company: "A", Host: "h", Key: "secret-a"}})
	var out bytes.Buffer
	if err := List(store, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a\tA\th\nb\tB\th\n" {
		t.Errorf("list: %q", out.String())
	}
	if err := Remove(store, "a", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if left, _ := store.Load(); len(left) != 1 || left["b"].Key != "secret-b" {
		t.Errorf("left: %+v", left)
	}
	if err := Remove(store, "a", &bytes.Buffer{}); err == nil {
		t.Error("removing twice should fail")
	}
}

func TestPickExplainsEveryCase(t *testing.T) {
	one := map[string]Account{"x": {Alias: "x"}}
	two := map[string]Account{"x": {Alias: "x"}, "y": {Alias: "y"}}
	if a, err := Pick(one, ""); err != nil || a.Alias != "x" {
		t.Errorf("one: %v %v", a, err)
	}
	for _, c := range []struct {
		accounts map[string]Account
		alias    string
		want     string
	}{{nil, "", "yougile-mcp add"}, {two, "", "x, y"}, {two, "z", `no account "z"`}} {
		if _, err := Pick(c.accounts, c.alias); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.alias, err)
		}
	}
}

func TestMissingFileIsNoAccounts(t *testing.T) {
	if got, err := (Store{Path: filepath.Join(t.TempDir(), "none.json")}).Load(); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
