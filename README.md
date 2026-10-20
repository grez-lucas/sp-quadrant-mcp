# sp-quadrant-mcp

An MCP server that lets Claude Code list, create, move and complete Tasks on Super Productivity's
Eisenhower board, through SP desktop's Local REST API on `127.0.0.1:3876`. Changes go through SP's
normal op-log, so they reach SuperSync and the iPhone like any edit made in the app.

The decision is [ADR-0001](docs/adr/0001-drive-sp-desktop-through-its-local-rest-api-with-a-wrapper-we-own.md),
the vocabulary is [CONTEXT.md](CONTEXT.md), and the build is
[grez-lucas/supersync#13](https://github.com/grez-lucas/supersync/issues/13). The ADR's
`bukenlabs-infra#29` and `bukenlabs-infra#31` references are stale: the walking skeleton is now
[grez-lucas/supersync#11](https://github.com/grez-lucas/supersync/issues/11) and the build is
[grez-lucas/supersync#13](https://github.com/grez-lucas/supersync/issues/13).

## Tools

A quadrant is two booleans, `urgent` (the task carries `EM_URGENT`) and `important` (it carries
`EM_IMPORTANT`). The panels are "Urgent and important", "Not urgent, important",
"Urgent, not important" and "Neither".

| Tool | Input | Does |
| --- | --- | --- |
| `list_tasks` | `query?: string`, `include_done?: bool` | Lists top-level, unarchived Tasks as `{id, title, done, urgent, important, quadrant}`. Subtasks are dropped. Without the `EM_*` tags it still lists, without the quadrant fields, and adds a `note` saying to press "Create tags". |
| `create_task` | `title: string`, `urgent: bool`, `important: bool` | Creates the Task in `INBOX_PROJECT` with the title stored literally (`isIgnoreShortSyntax`), then reads it back. |
| `move_task` | `task_id: string`, `urgent: bool`, `important: bool` | Changes only the two `EM_*` tags and sends the full tag list, then reads it back and checks the quadrant and every other tag. |
| `complete_task` | `task_id: string` | Marks the Task done, then reads it back and checks it is done in the same quadrant. Works without the `EM_*` tags. |

What every call enforces (ADR-0001):

- It starts with `GET /health` and never starts SP.
- `create_task` and `move_task` refuse to write unless `GET /tags` lists both `EM_URGENT` and
  `EM_IMPORTANT`, and say to press "Create tags" on SP's Eisenhower board. SP itself accepts unknown
  tag ids, so this refusal is the only thing that stops a misplaced task.
- `move_task` and `complete_task` refuse subtasks.
- After every write it reads the task back and fails if SP holds something other than what was asked.

The token is read once at startup from SP's own `local-rest-api-token` file under
`$(os.UserConfigDir)/superProductivity/` (`~/.config/superProductivity/` on Linux). There is no flag
or environment variable for it, and it never appears in logs, errors or argv. Requests never go
through a proxy. Logs go to stderr; stdout is the MCP protocol.

## Install

```sh
GOTOOLCHAIN=auto go install github.com/grez-lucas/sp-quadrant-mcp@v0.1.1
```

`go install pkg@version` runs outside any main module, so `go.mod`'s `go 1.27.3` line is the only one
it honours. Under `GOTOOLCHAIN=auto` an older local Go switches to go1.27.3 for the build; under
`GOTOOLCHAIN=local` it refuses with `requires go >= 1.27.3` instead of building a binary that still has
GO-2026-6617, GO-2026-6613 and GO-2026-6612. v0.1.0 named go1.27.3 only in a `toolchain` line, which
`go install` ignores, so a v0.1.0 binary may have been built with an older Go: install v0.1.1.

## Register with Claude Code

User scope, the absolute path to the binary as the command, and no `-e`/`--env`:

```sh
claude mcp add --scope user sp-quadrant -- "$(go env GOPATH)/bin/sp-quadrant-mcp"
```

The shell expands `$(go env GOPATH)` before `claude` runs, so the registered command is an absolute
path. Prerequisites, verification, error messages and upgrades are in
[docs/runbooks/install-and-recover.md](docs/runbooks/install-and-recover.md).

## Gates

```sh
export GOTOOLCHAIN=auto
go version   # go1.27.3
go mod tidy -diff
go build ./...
go vet ./...
go vet -tags sp_live ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --build-tags sp_live ./...
go test -race -count=1 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

CI runs the same gates (`.github/workflows/ci.yml`), on the go1.27.3 that `actions/setup-go` reads
from the `go` line (it falls back to the `go` directive when there is no `toolchain` directive). A CI
step also fails if `go.mod` gains a `toolchain` line or its `go` line falls below the Go CI ran, since
the `go` line is what `go install` builds with. `GOTOOLCHAIN=auto` lets an older local Go switch to
go1.27.3 for the gates; under `GOTOOLCHAIN=local` it refuses with `requires go >= 1.27.3`.
golangci-lint is v2.14.0 because v2.13.x cannot type-check against go1.27.2 or later (export data
version 5); it runs through `go run` so the pinned version is the one used, whatever is installed
globally.

The tests never reach a real SP: they run a real MCP client against the server over an in-memory
transport, through the real REST adapter, against a stateful fake of SP v19.2.0's Local REST API
(`internal/sptest`).

### Live contract suite

`live_test.go` (build tag `sp_live`) checks the real SP desktop on `127.0.0.1:3876` with the real token
file, to catch drift between SP's contract and the fake. Run it only on purpose, with SP open:

```sh
GOTOOLCHAIN=auto go test -tags sp_live -count=1 -run Live -v .
```

Every task it creates is titled `SS13 live ...`. When the test ends, whatever the outcome, it deletes
every `SS13 live` task that was not there when it started. It sends `EM_*` tag ids only when `GET /tags`
lists both, and logs which tag state it ran in. It creates no tag. To check that a move keeps other
tags, create a tag whose title starts with `ss13` in SP: the suite attaches that tag (and no other) to
its own task before `move_task`, and logs whether that branch ran.

## Layout

- `main.go`: wiring only (token path, SP client, tool registration, stdio).
- `internal/quadrant`: the domain. Quadrant, panel names, and Move as tag-set math.
- `internal/tools`: the four MCP tools over the `Desktop` port.
- `internal/sp`: the Local REST API adapter implementing `Desktop`.
- `internal/token`: reads the token file.
- `internal/sptest`: the fake SP used by the tests.
