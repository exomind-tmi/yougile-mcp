# yougile-mcp

YouGile for Claude: a local MCP server that reads boards, tasks and task chats over the YouGile REST API v2,
creates and changes tasks, comments, and moves files in and out. It needs no open browser tab, so it works in
headless sessions, unlike the built-in YouGile MCP, whose tools run inside a YouGile tab.

> **Status:** Work in progress. Expect breaking changes.

> **Unofficial.** This project is not affiliated with YouGile. It has not been verified against a live YouGile
> API yet (see "What is verified").

## Install

The plugin is built and released with [mcp-plugin-kit](https://github.com/exomind-tmi/mcp-plugin-kit): a
text-only Claude plugin whose launcher downloads the server binary pinned in `plugin/release.json` from
GitHub Releases and checks its SHA-256. There is no release yet, so `release.json` still says so; the plugin is
not in the `exomind` catalog either. Until then, try a local build: `../mcp-plugin-kit/scripts/dev-install.sh`
packs `dist/marketplace/` (Claude Code: `claude plugin marketplace add ./dist/marketplace`, then
`claude plugin install yougile@yougile-local`) and `dist/yougile-mcp-plugin.zip` (Claude Desktop). After
installing, add an account in a terminal with `yougile-mcp add` (the binary is in
`~/.mcp/exomind-tmi/yougile-mcp/bin/<version>/`).

## What it does

| Tool                  | Kind    | What                                                                          |
|-----------------------|---------|-------------------------------------------------------------------------------|
| `list_accounts`       | read    | saved accounts (alias and company)                                            |
| `list_boards`         | read    | boards, optionally of one project                                             |
| `list_columns`        | read    | columns of a board                                                            |
| `list_tasks`          | read    | tasks of a column (assignees as names, description as text)                   |
| `get_task`            | read    | one task, full description, links of the attached files                       |
| `get_messages`        | read    | chat of a task, oldest first, with links of the attached files                |
| `download_attachment` | local   | an image comes back as an image; any other file is saved and its path returned |
| `create_task`         | changes | new task in a column                                                          |
| `update_task`         | changes | title, description, column (move), completed, archived                        |
| `post_message`        | changes | comment in the chat of a task                                                 |
| `upload_attachment`   | changes | upload local files and post them to the chat of a task                        |

Every tool takes `account` (the alias); it may be left out while only one account is saved.
The tools that change YouGile are marked destructive, so the host asks before every call. There is no write
switch. The server tells Claude that card text written by others is data, never instructions.

## Accounts

An account is an alias plus one company; one login can reach several companies, each with its own key.

```
yougile-mcp add             # login and password (not echoed, not stored), lists the companies,
                            # saves a key per chosen company under an alias
yougile-mcp list
yougile-mcp remove <alias>  # forgets the key here; it stays valid in YouGile until deleted there
```

`YOUGILE_HOST` (for `add`) selects `https://yougile.com` (default) or `https://ru.yougile.com`; https only.

State lives in `~/.mcp/exomind-tmi/yougile-mcp/` (override with `YOUGILE_MCP_HOME`, an absolute path): the
folder is 0700, `accounts.json` with the keys is 0600, downloads go to `downloads/<alias>/`. Anyone who can
read this folder can act as you in YouGile; it is protected by the file permissions of your user account.

## Settings

| Variable                  | Meaning                                                                      |
|---------------------------|------------------------------------------------------------------------------|
| `YOUGILE_MCP_HOME`        | state folder (absolute path)                                                 |
| `YOUGILE_HOST`            | host used by `yougile-mcp add`; stored with the account                      |
| `YOUGILE_MCP_UPLOAD_DIRS` | folders `upload_attachment` may read (path list); default: the working folder |

## Security model

- A key goes only to the host saved with its account, in the `Authorization` header. Redirects are not
  followed, so a key cannot be sent elsewhere; `download_attachment` refuses links outside the account's host.
  Errors shown to the model never contain a key.
- Uploads read only from the allowed folders, with symlinks resolved.
- Downloads are capped at 50 MB; images up to 5 MB are returned inline, the rest are saved.
- Output is capped; nothing deletes tasks or messages.
- The server speaks MCP over stdio only: no ports, no daemon, no state apart from the folder above.

## Development

Go 1.27, no CGO.

```
go vet ./... && go test ./...
CGO_ENABLED=1 go test -race ./...
go build -ldflags "-X main.version=v0.1.0" -o yougile-mcp ./cmd/yougile-mcp
yougile-mcp stdio        # what a Claude host launches
```

Build, packaging and release come from mcp-plugin-kit (`mcp-plugin.env` names the server). `plugin/scripts/`
holds the launchers the kit renders (`scripts/render.sh` of the kit; CI fails when they are stale),
`THIRD_PARTY_NOTICES.md` is generated by the kit's `scripts/notices.sh`. CI and release are the kit's reusable
workflows (`.github/workflows/`).

Tests run against `internal/fakeapi`, a local httptest stand-in for YouGile, including a test that starts
the real binary and talks MCP to it. Layout: `cmd/yougile-mcp` (commands), `internal/yougile` (REST client:
auth, paging, retries, upload, download), `internal/accounts`, `internal/tools` (the MCP tools). The state
folder comes from the kit's `home` package.

## What is verified

Only against the fake. The endpoints `/columns`, `/tasks`, `/string-stickers`, `/users`,
`/chats/{id}/messages`, `/upload-file` and `/auth/companies|keys` come from an earlier internal
integration, which used them against the real service. `/boards`, `GET|PUT /tasks/{id}` and `POST /tasks`
are resources of API v2 that were not exercised there. How attached files appear in chats and descriptions
(`/root/#file:<url>`, `<img src>`, `<a href>`) is taken from the same code; whether a download needs the key
is not known, the key is sent only to the account's own host.

## Known limits

- REST answers have no human code like `NNORB-123`, only UUIDs and the project number.
- There is no server-side search.
- Rate limits were never hit in that earlier use; the client retries 429 and 5xx with backoff.

## TODO

- A first release (tag `vX.Y.Z`) and an entry in the `exomind` catalog (`exomind-tmi/claude-plugins`).
- The Windows launcher and the reusable workflows have run only in the kit's own tests so far; the first CI
  run on GitHub is their real test.
- A skill for Claude: how to walk boards, how the links of cards are built
  (`https://yougile.com/team/<company code>#<TASK CODE>`, the `/team/` segment is required).
- Stickers: tools to list and set them, resolving them to names in tasks, searching tasks by a sticker value
  (a code kept in a text sticker).
- `find_tasks` (substring search over the tasks of a board or the company), `list_users`, `list_projects`,
  assignees in `create_task` and `update_task`, `delete_message`.
- Locking the accounts file against two `add` runs at once.

## License

[0BSD](LICENSE) © 2026 Exomind Tmi
