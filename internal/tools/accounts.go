package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/exomind-tmi/yougile-mcp/internal/accounts"
	"github.com/exomind-tmi/yougile-mcp/internal/yougile"
)

// noForm is the way to add an account where the host cannot show a form.
func noForm() string {
	bin, err := os.Executable()
	if err != nil {
		bin = "yougile-mcp"
	}
	return "this Claude app cannot show a login form: run `" + bin + " add` in a terminal on this computer"
}

type accountsIn struct {
	Action  string `json:"action" jsonschema:"list, add or remove"`
	Account string `json:"account,omitempty" jsonschema:"add: alias for the new account, 1-32 of a-z, 0-9, - and _ (default: from the company name); remove: the alias to forget"`
	Company string `json:"company,omitempty" jsonschema:"add: name of the company when the login has several; without it every company is saved, each under its own alias"`
	Host    string `json:"host,omitempty" jsonschema:"add: https://yougile.com (default) or https://ru.yougile.com"`
}

func (t *server) manageAccounts(ctx context.Context, req *mcp.CallToolRequest, in accountsIn) (result, any, error) {
	switch in.Action {
	case "list":
		return t.listAccounts()
	case "add":
		return t.addAccount(ctx, req, in)
	case "remove":
		return t.removeAccount(in)
	}
	return nil, nil, fmt.Errorf("unknown action %q: use list, add or remove", in.Action)
}

func (t *server) listAccounts() (result, any, error) {
	all, err := t.store.Load()
	if err != nil {
		return nil, nil, err
	}
	rows := []accountOut{}
	for _, alias := range strings.Split(accounts.Aliases(all), ", ") {
		if a, ok := all[alias]; ok {
			rows = append(rows, accountOut{Account: a.Alias, Company: a.Company})
		}
	}
	return text(rows)
}

type accountOut struct {
	Status  string `json:"status,omitempty"`
	Account string `json:"account,omitempty"`
	Company string `json:"company,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ruHost is the other host YouGile serves the API on.
const ruHost = "https://ru.yougile.com"

// loginForm is the id of the form that asks for the login and the password.
const loginForm = "login"

// addAccount runs in two rounds (multi round-trip requests): the first asks the host to show the login
// form, the retry carries what the user entered. The password lives only in that retry.
func (t *server) addAccount(ctx context.Context, req *mcp.CallToolRequest, in accountsIn) (result, any, error) {
	p := req.Session.InitializeParams()
	if p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		return nil, nil, errors.New(noForm())
	}
	// the password goes to this host: only YouGile's own, never one taken from a card
	host := firstNonEmpty(t.deps.Host, yougile.DefaultHost)
	if in.Host != "" {
		host = strings.TrimRight(in.Host, "/")
		if !slices.Contains([]string{yougile.DefaultHost, ruHost, t.deps.Host}, host) {
			return nil, nil, fmt.Errorf("host must be %s or %s", yougile.DefaultHost, ruHost)
		}
	}
	client, err := yougile.New(host, "")
	if err != nil {
		return nil, nil, err
	}
	answer, ok := req.Params.InputResponses[loginForm].(*mcp.ElicitResult)
	if !ok {
		t.deps.Log.Info("add: asking for the login form", "host", host, "client", p.ClientInfo, "protocol", p.ProtocolVersion)
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{loginForm: &mcp.ElicitParams{
			Message: "Log in to YouGile (" + host + "). The password goes to YouGile once, to make an API key " +
				"for this computer; it is not stored.",
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"login":    map[string]any{"type": "string", "title": "Login (email)"},
					"password": map[string]any{"type": "string", "title": "Password"},
				},
				"required": []string{"login", "password"},
			},
		}}}, nil, nil
	}
	t.deps.Log.Info("add: login form answered", "action", answer.Action)
	if answer.Action != "accept" {
		// "decline" also comes from a Claude app that does not show the form at all
		status := "cancelled"
		if answer.Action == "decline" {
			status = "declined"
		}
		return text([]accountOut{{Status: status}})
	}
	login, _ := answer.Content["login"].(string)
	password, _ := answer.Content["password"].(string)
	companies, err := client.Companies(ctx, login, password)
	if err != nil {
		return nil, nil, err
	}
	if len(companies) == 0 {
		return nil, nil, errors.New("no companies found: check the login and the password, then add again")
	}
	chosen, err := pickCompanies(companies, in.Company)
	if err != nil {
		return nil, nil, err
	}
	if in.Account != "" && len(chosen) > 1 {
		return nil, nil, fmt.Errorf("account names one alias, and the login has several companies: pass company as well (%s)",
			strings.Join(companyNames(chosen), ", "))
	}
	saved, err := t.store.Load()
	if err != nil {
		return nil, nil, err
	}
	out := []accountOut{}
	for _, c := range chosen {
		if a, ok := savedFor(saved, c.ID, host); ok {
			out = append(out, accountOut{Status: "exists", Account: a.Alias, Company: c.Name})
			continue
		}
		alias := in.Account
		if alias == "" {
			alias = accounts.FreeAlias(saved, accounts.DefaultAlias(c.Name))
		}
		if err := accounts.SaveKey(ctx, t.store, client, host, login, password, c, alias); err != nil {
			out = append(out, accountOut{Status: "failed", Account: alias, Company: c.Name, Error: err.Error()})
			continue
		}
		saved[alias] = accounts.Account{Alias: alias, CompanyID: c.ID, Host: host}
		out = append(out, accountOut{Status: "added", Account: alias, Company: c.Name})
	}
	return text(out)
}

// savedFor is the saved account of a company on a host, if there is one.
func savedFor(saved map[string]accounts.Account, companyID, host string) (accounts.Account, bool) {
	for _, a := range saved {
		if a.CompanyID == companyID && a.Host == host {
			return a, true
		}
	}
	return accounts.Account{}, false
}

func companyNames(companies []yougile.Company) []string {
	names := make([]string, len(companies))
	for i, c := range companies {
		names[i] = c.Name
	}
	return names
}

// pickCompanies is the company named (case-insensitive, whole name or a part that fits one), or all of them.
func pickCompanies(companies []yougile.Company, name string) ([]yougile.Company, error) {
	if name == "" {
		return companies, nil
	}
	var parts []yougile.Company
	for _, c := range companies {
		if strings.EqualFold(c.Name, name) {
			return []yougile.Company{c}, nil
		}
		if strings.Contains(strings.ToLower(c.Name), strings.ToLower(name)) {
			parts = append(parts, c)
		}
	}
	if len(parts) == 1 {
		return parts, nil
	}
	return nil, fmt.Errorf("company %q does not name one of: %s", name, strings.Join(companyNames(companies), ", "))
}

func (t *server) removeAccount(in accountsIn) (result, any, error) {
	if err := require("account", in.Account); err != nil {
		return nil, nil, err
	}
	if err := accounts.Remove(t.store, in.Account, io.Discard); err != nil {
		return nil, nil, err
	}
	return text(accountOut{Status: "removed", Account: in.Account})
}
