package accounts

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	// Windows has no Unix modes.
	if info, _ := os.Stat(store.Path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(store.Path)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
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

func TestLockKeepsASecondChangeOut(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	store := Store{Path: filepath.Join(t.TempDir(), "state", "accounts.json")}
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("second lock: %v", err)
	}
	if err := Remove(store, "x", io.Discard); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("remove while locked: %v", err)
	}
	unlock()
	// a failed add lets go of the lock too
	p := &script{answers: []string{"me", "0", "Bad Alias!"}, secret: "pw"}
	if err := Add(context.Background(), store, fake.URL, p, io.Discard); err == nil {
		t.Fatal("a bad alias must fail")
	}
	again, err := store.Lock()
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	again()
}

func TestDefaultAliasTransliteratesCyrillic(t *testing.T) {
	for name, want := range map[string]string{
		"ООО «Ромашка»": "ooo-romashka",
		"Beta Corp":     "beta-corp",
		"Щёлково 2":     "shchelkovo-2",
		"«»":            "company",
	} {
		if got := DefaultAlias(name); got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
}

func TestFreeAliasNumbersATakenOne(t *testing.T) {
	saved := map[string]Account{"company": {}, "company-2": {}}
	if got := FreeAlias(saved, "company"); got != "company-3" {
		t.Errorf("got %s", got)
	}
	long := strings.Repeat("a", 32)
	if got := FreeAlias(map[string]Account{long: {}}, long); got != strings.Repeat("a", 28)+"-2" {
		t.Errorf("long: %s", got)
	}
}

func TestAddOffersAFreeAliasWhenTheNameIsTaken(t *testing.T) {
	fake := fakeapi.New()
	defer fake.Close()
	store := Store{Path: filepath.Join(t.TempDir(), "accounts.json")}
	if err := store.Save(map[string]Account{"acme": {CompanyID: "other", Company: "Acme Old", Host: fake.URL, Key: "k"}}); err != nil {
		t.Fatal(err)
	}
	p := &script{answers: []string{"me", "0", ""}, secret: "x"}
	if err := Add(context.Background(), store, fake.URL, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := store.Load(); saved["acme-2"].CompanyID != "co1" || saved["acme"].CompanyID != "other" {
		t.Errorf("saved: %+v", saved)
	}
}
