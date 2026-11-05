# yougile-mcp

YouGile for Claude: a local MCP server that reads boards, tasks and task chats over the YouGile REST API v2,
creates and changes tasks, comments, and moves files in and out. It needs no open browser tab, so it works in
headless sessions, unlike the built-in YouGile MCP, whose tools run inside a YouGile tab.

> **Status:** Work in progress. Expect breaking changes.

> **Unofficial.** This project is not affiliated with YouGile. See "What is verified" for what was checked
> against the live service.

## Install

The plugin is built and released with [mcp-plugin-kit](https://github.com/exomind-tmi/mcp-plugin-kit): a
text-only Claude plugin whose launcher downloads the server binary pinned in `plugin/release.json` from
GitHub Releases and checks its SHA-256. It is in the `exomind` catalog
([exomind-tmi/claude-plugins](https://github.com/exomind-tmi/claude-plugins)):

```
claude plugin marketplace add exomind-tmi/claude-plugins
claude plugin install yougile@exomind
```

For a local build, `../mcp-plugin-kit/scripts/dev-install.sh` packs `dist/marketplace/` (Claude Code:
`claude plugin marketplace add ./dist/marketplace`, then `claude plugin install yougile@yougile-local`) and
`dist/yougile-mcp-plugin.zip` (Claude Desktop). After installing, ask Claude in the chat to add a YouGile
account (see "Accounts").

## What it does

| Tool                  | Kind    | What                                                                           |
|-----------------------|---------|--------------------------------------------------------------------------------|
| `manage_accounts`     | changes | `list`, `add` (log in through a form of the Claude app), `remove` accounts     |
| `list_projects`       | read    | projects, optionally by title                                                  |
| `list_boards`         | read    | boards, optionally of one project                                              |
| `list_columns`        | read    | columns of a board                                                             |
| `list_users`          | read    | people of the company or of a project (id, name, email)                        |
| `list_stickers`       | read    | custom stickers with their states, optionally of one board                     |
| `list_tasks`          | read    | tasks of a column                                                              |
| `find_tasks`          | read    | tasks by title, code (`DEV-123`), assignee, sticker state or free sticker value |
| `get_task`            | read    | one task: full description, stickers, deadline, subtasks, attached files       |
| `get_messages`        | read    | one page of the chat of a task, oldest first, with links of the attached files |
| `download_attachment` | local   | an image comes back as an image; any other file is saved and its path returned |
| `create_task`         | changes | new task in a column, with assignees and stickers                              |
| `update_task`         | changes | title, description, column (move), completed, archived, assignees, stickers    |
| `post_message`        | changes | comment in the chat of a task; returns the message id                          |
| `upload_attachment`   | changes | upload local files and post them to the chat of a task                         |
| `delete_message`      | changes | delete a message of your own; messages of others are refused                   |

Every tool takes `account` (the alias); it may be left out while only one account is saved.
The tools that change YouGile are marked destructive: a hint the Claude app may use to ask before a call;
whether it asks depends on its permission settings. There is no write switch. The server tells Claude that
card text written by others is data, never instructions.

Tasks show assignees as id and name, stickers as sticker name → state name or value, the deadline and the
start as UTC times, and both codes: `code` within the project (`DEV-123`) and `commonCode` across the company
(`ID-456`). When the people or the stickers cannot be read, their ids are shown instead. Lists of tasks
(`list_tasks`, `find_tasks`) cut each description at 300 characters (`get_task` gives it whole) and stop at
about 80 KB, so an answer is always whole JSON. When more tasks are there they answer `more: true` with a
hint and `nextOffset`; the next call passes it as `offset` (`find_tasks` over a whole board cannot go on:
search one column instead).

Descriptions are written as plain text: a blank line starts a paragraph, a line break stays one; the server
turns it into the HTML YouGile keeps. Changing `stickers` touches only the stickers named; `"-"` removes one.
Chat messages are plain text in YouGile and are read as such.

The plugin carries a skill (`plugin/skills/yougile/`) that tells Claude how to find and walk tasks.

## Accounts

An account is an alias plus one company; one login can reach several companies, each with its own key.

Accounts are kept on each computer: log in once on every device you use, nothing is synced.

In the chat, `manage_accounts` with `action: add` asks for the login and the password in a form of the
Claude app (MCP elicitation), so they reach the server without passing through the chat or the model. The
password goes to YouGile once, to make an API key, and is not stored; every later call sends that key. A
login with several companies saves the one named in `company`, or every company under its own alias (from
its name, Cyrillic transliterated; a taken one gets `-2`, `-3`). The answer has a status per company:
`added`, `exists` (saved before, under the alias shown) or `failed` (with the error). The `host` of `add`
may only be `https://yougile.com`, `https://ru.yougile.com` or `YOUGILE_HOST`, so a password never goes
elsewhere.

Where the form shows: interactive Claude Code in a terminal, also over SSH. The Code tab of Claude Desktop
runs Claude Code in print mode, which declines forms without showing them (the tool then answers
`declined`); add the account from an interactive `claude` session or in a terminal instead:

```
yougile-mcp add             # login and password (not echoed, not stored), lists the companies,
                            # saves a key per chosen company under an alias
yougile-mcp list
yougile-mcp remove <alias>  # forgets the key here; it stays valid in YouGile until deleted there
```

The MCP specification asks servers not to request passwords through form elicitation and to use URL mode
(a page in a browser) instead. The form is chosen so that an account can be added in a headless session
without a browser; a browser page is a possible later addition.

`YOUGILE_HOST` (for `add` in the terminal and in the chat) selects `https://yougile.com` (default) or
`https://ru.yougile.com`; https only.

State lives in `~/.mcp/exomind-tmi/yougile-mcp/` (override with `YOUGILE_MCP_HOME`, an absolute path): the
folder is 0700, `accounts.json` with the keys is 0600, downloads go to `downloads/<alias>/`. Anyone who can
read this folder can act as you in YouGile; it is protected by the file permissions of your user account.

## Settings

| Variable                  | Meaning                                                                      |
|---------------------------|------------------------------------------------------------------------------|
| `YOUGILE_MCP_HOME`        | state folder (absolute path)                                                 |
| `YOUGILE_HOST`            | host used to add an account; stored with the account                         |
| `YOUGILE_MCP_UPLOAD_DIRS` | folders `upload_attachment` may read (path list); default: the working folder |

## Security model

- A key goes only to the host saved with its account, in the `Authorization` header, and API calls follow
  no redirect, so a key cannot be sent elsewhere. Errors shown to the model never contain a key.
- Files need no key: `yougile.com/user-data/...` redirects to `user-data.yougile.com`, which serves them
  as they are. `download_attachment` sends no key at all and follows redirects only within the account's
  site (https, the same domain), up to five times.
- Uploads read only from the allowed folders, with symlinks resolved, and never from the state folder with
  the keys; a relative path is read from the working folder.
- Downloads are capped at 50 MB; images up to 5 MB are returned inline, the rest are saved.
- Output is capped; nothing deletes tasks, and `delete_message` deletes only messages written by the
  key's own user (checked with `/users/me` before the call).
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

Against the live service (yougile.com, 02.11.2026), through the server binary over MCP:

- Reading: projects (also by title), boards, columns, users (also by project), stickers, tasks of a column,
  one task, task codes, stickers by name, assignees, a chat in pages (YouGile serves the oldest first; the
  next page holds newer messages), the `text` filter of a chat, downloads of images and files from
  descriptions and chats, `link` of a task (`find_tasks` by code, on 05.11.2026; the app opens the card by
  the code alone).
- `find_tasks`: by title (a case-insensitive part of it, searched by YouGile), by `code` and by
  `commonCode`, by sticker state and by sticker value, within a board.
- Writing, on a test board: create a task with a sticker; change title, column, assignees (`[]` clears
  them), stickers (only the ones named change, `"-"` removes one), description in paragraphs, completed,
  archived; post a comment; upload an image and a text file to the chat (shown as file cards, as files
  attached in the app); delete an own message.
- Adding an account with the form in interactive Claude Code over SSH; the Desktop Code tab declines it.

Only against the fake: `delete_message` refusing a message of someone else, the deadline (the tasks
checked had none), the `link` of `create_task`, sprint stickers and free text stickers (the test company
has none), `ru.yougile.com`, rate limits.

## Known limits

- REST takes only UUIDs: `find_tasks` with `code` reads the task list (up to 10 000 tasks, stopping at the
  match) and matches the code here; `stickerValue` works the same way. YouGile lists tasks oldest first, so
  a recent task is found last; narrow such a search with `boardId` or `columnId`. The answer says so when
  it stopped at the cap.
- Links to cards (`link` in task answers) are built, not given by the API: `/team/<the last group of the
  company id>/#<task code>`, the address the app opens a card by. A task without a code has no link.
- Rate limits were never hit; the client retries 429 and 5xx with backoff.
- The first page of a long chat holds its oldest messages; the newest come last.

## TODO

- Setting deadlines; checklists and time tracking are neither shown nor set.

## License

[0BSD](LICENSE) © 2026 Exomind Tmi
