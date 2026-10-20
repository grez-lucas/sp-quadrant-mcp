# Install and recover

Installing `sp-quadrant-mcp` on the Linux laptop, registering it with Claude Code, checking that the
token stayed out of Claude Code's config, and what to do when a tool call fails. The decision behind
all of it is [ADR-0001](../adr/0001-drive-sp-desktop-through-its-local-rest-api-with-a-wrapper-we-own.md).

## Prerequisites

- **SP desktop at the server pin**, installed from SP's `.deb` and held with
  `apt-mark hold superproductivity`, as the supersync
  [desktop upgrade runbook](https://github.com/grez-lucas/supersync/blob/main/docs/runbooks/desktop-upgrade.md)
  does. The wrapper is written against SP `v19.2.0`'s Local REST API.
- **The Local REST API on**: SP desktop, Settings -> Misc -> Enable local REST API. Enabling it makes
  SP write the token to `~/.config/superProductivity/local-rest-api-token` (mode 0600).
- **"Create tags" pressed once per sync account**: open SP's Eisenhower board and press "Create tags".
  That creates the `EM_URGENT` and `EM_IMPORTANT` tag entities. Until then `create_task` and
  `move_task` refuse, and `list_tasks` lists without quadrants. Once pressed, the tags sync to every
  device on the account.
- **Go** 1.27.0 or later. With `GOTOOLCHAIN=auto` the install switches to go1.27.3 itself (below).

## Install

```sh
GOTOOLCHAIN=auto go install github.com/grez-lucas/sp-quadrant-mcp@v0.1.1
```

`go install pkg@version` runs outside any main module, so `go.mod`'s `go 1.27.3` line is the only one
it honours. With the laptop's `GOTOOLCHAIN=local` and a Go older than go1.27.3, it refuses with
`requires go >= 1.27.3` rather than build a binary with GO-2026-6617, GO-2026-6613 and GO-2026-6612 in
`net/http`; `GOTOOLCHAIN=auto` lets it switch to go1.27.3 instead. The binary lands in
`$(go env GOPATH)/bin/sp-quadrant-mcp`.

## Register

Enable the Local REST API in SP first, so the token file exists when Claude Code starts the server.

```sh
claude mcp add --scope user sp-quadrant -- "$(go env GOPATH)/bin/sp-quadrant-mcp"
```

No `-e`/`--env`: the server reads the token from SP's own file at startup and needs nothing else.
Restart Claude Code to pick the server up.

## Verify

Check which Go built the binary and which version of the module it is:

```sh
go version -m "$(go env GOPATH)/bin/sp-quadrant-mcp" | head -3
```

The first line must end in `go1.27.3` (or later), and the third (`mod`) must show
`github.com/grez-lucas/sp-quadrant-mcp` at the tag installed (`v0.1.1`); the second (`path`) only names
the package. A Go older than go1.27.3 on the first line means the binary is v0.1.0, whose `go.mod` named
go1.27.3 only in a `toolchain` line that `go install` ignores: install v0.1.1 as above.

Check the registration:

```sh
claude mcp get sp-quadrant
```

It must show the command as an absolute path to `sp-quadrant-mcp` (for example
`/home/luken/go/bin/sp-quadrant-mcp`) and no environment variables.

Check that the token is not in Claude Code's config. This reads the token as a pattern from SP's file,
so the token never reaches argv or the screen:

```sh
grep -qF -f ~/.config/superProductivity/local-rest-api-token ~/.claude.json
case $? in 0) echo FOUND;; 1) echo absent;; *) echo "check failed";; esac
```

Only `absent` passes. `check failed` means grep could not run the check (grep exits 2 when either file
is missing or unreadable), so it proves nothing: fix the cause and run it again. `FOUND` means the token
was copied into `~/.claude.json` (a registration with `--env`, for example): remove that registration,
regenerate the token in SP (Settings -> Misc -> Access Token), register again as above and restart
Claude Code.

Then ask Claude Code to list the tasks on the Eisenhower board. `list_tasks` answering with tasks
(and quadrants, once "Create tags" was pressed) is the end-to-end check.

## Error messages

Every failure comes back to Claude Code as a tool error. The server also logs each failed call to
stderr, which Claude Code keeps in its MCP server log; the token never appears in either.

| Message starts with | Meaning | Fix |
| --- | --- | --- |
| `SP desktop is not running, or its Local REST API is off (Settings -> Misc -> Enable local REST API)` | `GET /health` got no answer on `127.0.0.1:3876`. The server never starts SP. | Open SP desktop. If it is open, turn on Settings -> Misc -> Enable local REST API. |
| `SP desktop is still starting; retry in a moment` | SP answered, but its window has not finished loading (`rendererReady: false`, or `APP_NOT_READY`). | Wait a few seconds and retry. |
| `sp-quadrant-mcp found no SP Local REST API token when it started` | The token file was missing or empty when Claude Code started the server. The server reads it only once. | Enable the Local REST API in SP, then restart Claude Code. |
| `SP rejected the token: it may have been regenerated` | SP answered `401 UNAUTHORIZED`. The token was regenerated (Settings -> Misc -> Access Token) or the profile is new since Claude Code started. | Restart Claude Code so the server re-reads the token file. |
| `refused: the Eisenhower tags (EM_URGENT and EM_IMPORTANT) do not exist in this sync account yet` | `GET /tags` lacks one or both `EM_*` tags. Nothing was written. | Open SP's Eisenhower board, press "Create tags" once, retry. |
| `task ... is not a Task: subtasks never appear on the board` | `move_task` or `complete_task` got a subtask's id. Nothing was written. | Use the parent task's id, or change the subtask in SP. |
| `SP has no INBOX_PROJECT project to create the task in` | `GET /projects` lacks SP's default inbox. Nothing was written. | Check the profile in SP; the inbox project should always exist. |
| `SP created task ... but reads it back in ...`, `SP moved task ... but reads it back in ...` | The write went through but SP holds a different quadrant, for example because SP's data repair stripped tags it does not know. | Check the task on the Eisenhower board; check that both `EM_*` tags exist. |
| `SP dropped tag(s) ... from task ... during the move` | The move changed tags other than the two `EM_*` tags. | Re-add the listed tags to the task in SP. |
| `SP did not store task ... as done`, `task ... moved from ... while being completed` | The complete went through but SP holds a different state. | Check the task in SP. |
| `SP desktop failed while trying to create the task`, `... move task ...`, `... complete task ...; check SP before retrying, a change may already have been applied` | A write got no answer (SP went away, or took longer than 20 seconds) or a 5xx such as `RENDERER_TIMEOUT` (SP's renderer may still apply it after its 15-second budget) or `INTERNAL_ERROR`. The write may have landed. | Check the task in SP before retrying, so a retried create does not make a duplicate. |
| `SP created task <id> but did not answer when asked to confirm it; check it in SP before retrying, or a retry may create a duplicate` | `create_task` sent the task and SP accepted it, then the read that confirms it failed (no answer, or any error). The task `<id>` probably exists. | Find task `<id>` in SP and check its quadrant; do not retry the create unless it is missing. |
| `SP was sent the move of task ...` or `SP was sent the completion of task ... but did not answer when asked to confirm it; check SP before retrying, a change may already have been applied` | `move_task` or `complete_task` sent its change, then the read that confirms it failed. The change may have landed. | Check the task in SP before retrying. |
| `SP desktop stopped answering while trying to ...; retry once SP responds` | A read before any write (listing tasks, tags or projects, or reading the task to move or complete) got no answer after the health check passed. Nothing was written. | Retry once SP responds. |
| `could not ...: SP error CODE (HTTP n): message` | Any other SP error, with SP's own code and message, for example `TASK_NOT_FOUND` for an unknown id. | Act on SP's message; for `TASK_NOT_FOUND`, list the tasks again for a current id. |

## Limits

`move_task` reads the task's tags, then writes the full list back, because SP's `tagIds` is a full
replacement on every write: a tag added to the task in SP between that read and that write is
overwritten, and the move's read-back check cannot see it because it was never part of the list read.

## Upgrade the wrapper

```sh
GOTOOLCHAIN=auto go install github.com/grez-lucas/sp-quadrant-mcp@vX.Y.Z
```

`GOTOOLCHAIN=auto` for the same reason as the install: an older local Go switches to the Go the tag's
`go` line names, where `GOTOOLCHAIN=local` would refuse. Check it with the `go version -m` step under
Verify, then restart Claude Code. The registration points at the binary path, which does not change, so
it needs no update.

## After an SP update

Follow the supersync
[desktop upgrade runbook](https://github.com/grez-lucas/supersync/blob/main/docs/runbooks/desktop-upgrade.md)
for the update itself. What is specific to this wrapper:

- **Re-enable the Local REST API if the update turned it off.** SP PR
  [#10251](https://github.com/super-productivity/super-productivity/pull/10251), once released, makes
  the enable switch device-local and needs it turned on again after that upgrade. The symptom is the
  "not running, or its Local REST API is off" message with SP open.
- **Restart Claude Code if the token changed.** A fresh profile, or a regenerated token, means a new
  token file; the running server still holds the old one and gets the "SP rejected the token"
  message.
- **"Create tags" only if the sync account lacks the `EM_*` tags.** A fresh profile on this laptop
  syncs the tags down from the account if they were created on any device before. Press "Create tags"
  only when `list_tasks` says the tags are missing.
