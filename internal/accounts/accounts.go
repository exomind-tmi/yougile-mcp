// Package accounts keeps the saved YouGile accounts: an account is an alias plus one company,
// with its own key. Passwords are asked for, used once and never stored.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/exomind-tmi/mcp-plugin-kit/home"
	"github.com/exomind-tmi/yougile-mcp/internal/yougile"
)

var aliasRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Account is one company reachable with one key.
type Account struct {
	Alias     string `json:"-"`
	CompanyID string `json:"companyId"`
	Company   string `json:"company"`
	Host      string `json:"host"`
	Key       string `json:"key"`
}

// Store is the accounts file.
type Store struct{ Path string }

// FileIn is the file of the saved accounts in the state folder h.
func FileIn(h home.Home) string { return filepath.Join(h.Dir, "accounts.json") }

// Load reads all accounts, keyed by alias. A missing file is no accounts.
func (s Store) Load() (map[string]Account, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Account{}, nil
	}
	if err != nil {
		return nil, err
	}
	accounts := map[string]Account{}
	if err := json.Unmarshal(raw, &accounts); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", s.Path, err)
	}
	for alias, a := range accounts {
		a.Alias = alias
		accounts[alias] = a
	}
	return accounts, nil
}

// Save writes the accounts file atomically, readable by the owner only.
func (s Store) Save(accounts map[string]Account) error {
	if err := home.EnsureDir(filepath.Dir(s.Path)); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Pick chooses the account a tool call is about: the named one, or the only one.
func Pick(accounts map[string]Account, alias string) (Account, error) {
	if alias != "" {
		a, ok := accounts[alias]
		if !ok {
			return Account{}, fmt.Errorf("no account %q; known: %s", alias, Aliases(accounts))
		}
		return a, nil
	}
	switch len(accounts) {
	case 0:
		return Account{}, errors.New("no YouGile accounts: run `yougile-mcp add` (see the README)")
	case 1:
		for _, a := range accounts {
			return a, nil
		}
	}
	return Account{}, fmt.Errorf("several accounts: pass `account`, one of %s", Aliases(accounts))
}

// Aliases lists the aliases, sorted, for messages.
func Aliases(accounts map[string]Account) string {
	names := make([]string, 0, len(accounts))
	for alias := range accounts {
		names = append(names, alias)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Prompter asks the user; Secret must not echo.
type Prompter interface {
	Ask(prompt string) (string, error)
	Secret(prompt string) (string, error)
}

func defaultAlias(company string) string {
	alias := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(company), "-"), "-")
	if len(alias) > 32 {
		alias = strings.Trim(alias[:32], "-")
	}
	if alias == "" {
		return "company"
	}
	return alias
}

// Add logs in once, lists the companies of the login, and saves a key per chosen company.
func Add(ctx context.Context, s Store, host string, p Prompter, out io.Writer) error {
	client, err := yougile.New(host, "")
	if err != nil {
		return err
	}
	login, err := p.Ask("YouGile login: ")
	if err != nil {
		return err
	}
	password, err := p.Secret("Password: ")
	if err != nil {
		return err
	}
	companies, err := client.Companies(ctx, login, password)
	if err != nil {
		return err
	}
	if len(companies) == 0 {
		return errors.New("no companies found: check the login and the password")
	}
	for i, c := range companies {
		fmt.Fprintf(out, "[%d] %s\n", i, c.Name)
	}
	chosen, err := choose(p, companies)
	if err != nil {
		return err
	}
	accounts, err := s.Load()
	if err != nil {
		return err
	}
	for _, c := range chosen {
		def := defaultAlias(c.Name)
		alias, err := p.Ask(fmt.Sprintf("Alias for %q [%s]: ", c.Name, def))
		if err != nil {
			return err
		}
		if alias = strings.TrimSpace(alias); alias == "" {
			alias = def
		}
		if !aliasRE.MatchString(alias) {
			return fmt.Errorf("bad alias %q: use 1-32 of a-z, 0-9, - and _", alias)
		}
		if _, taken := accounts[alias]; taken {
			return fmt.Errorf("alias %q exists: remove it first", alias)
		}
		key, err := client.NewKey(ctx, login, password, c.ID)
		if err != nil {
			return err
		}
		accounts[alias] = Account{Alias: alias, CompanyID: c.ID, Company: c.Name, Host: host, Key: key}
		if err := s.Save(accounts); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved %q (%s)\n", alias, c.Name)
	}
	return nil
}

func choose(p Prompter, companies []yougile.Company) ([]yougile.Company, error) {
	answer, err := p.Ask("Companies to save (numbers separated by commas, Enter = all): ")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(answer) == "" {
		return companies, nil
	}
	var chosen []yougile.Company
	for _, part := range strings.Split(answer, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n >= len(companies) {
			return nil, fmt.Errorf("no company number %q", part)
		}
		chosen = append(chosen, companies[n])
	}
	return chosen, nil
}

// List prints alias, company and host, never the key.
func List(s Store, out io.Writer) error {
	accounts, err := s.Load()
	if err != nil {
		return err
	}
	aliases := strings.Split(Aliases(accounts), ", ")
	for _, alias := range aliases {
		if a, ok := accounts[alias]; ok {
			fmt.Fprintf(out, "%s\t%s\t%s\n", a.Alias, a.Company, a.Host)
		}
	}
	return nil
}

// Remove forgets an account. Its key stays valid in YouGile until deleted there.
func Remove(s Store, alias string, out io.Writer) error {
	accounts, err := s.Load()
	if err != nil {
		return err
	}
	if _, ok := accounts[alias]; !ok {
		return fmt.Errorf("no account %q", alias)
	}
	delete(accounts, alias)
	if err := s.Save(accounts); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %q (the key itself stays valid in YouGile until you delete it there)\n", alias)
	return nil
}
