# yougile-mcp

## Commits

- One line, Conventional Commits 1.0.0: `<type>(<optional scope>): <imperative, lowercase description>` with
  `feat`, `fix`, `docs`, `test`, `refactor`, `chore`. A `Co-Authored-By` trailer follows after a blank line
  (decision of the owner for this repository).

## Rules

- Go 1.27, no CGO, no daemon: a stateless stdio server. Every other dependency needs a reason.
- stdout is the MCP channel. Messages go to stderr; the API key and the password are never printed or logged,
  and never appear in an error shown to the model.
- A tool that changes YouGile is registered with `annotations(false, true)` (destructive), so hosts ask before
  the call. There is no write switch.
- The state folder is touched only through the kit's `home` package (`github.com/exomind-tmi/mcp-plugin-kit/home`).
- Build, launchers, packaging and CI come from mcp-plugin-kit; do not copy them here. After changing the kit's
  templates, run its `scripts/render.sh` and commit `plugin/scripts/`; after changing `go.mod`, its `scripts/notices.sh`.
- Tests use `internal/fakeapi` (httptest); they never call the real API. `go vet ./... && go test ./...`,
  and `CGO_ENABLED=1 go test -race ./...` before committing.
