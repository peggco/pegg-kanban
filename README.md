<div align="center">

<img src="https://cdn.pegg.dev/logo/infinity.gif" alt="Pegg" width="420" />

# Pegg Kanban

### A board that runs your agent.

A five-column Kanban board with a local web UI that runs the Pegg agent. A card in the trigger column starts one `pegg run --auto` process; integration plugins add issue sync, worktrees, and change requests through hooks.

[Plugins docs](https://docs.pegg.dev/pegg/configurations/plugins/) · [Releases](https://github.com/peggco/pegg-kanban/releases) · [Website](https://pegg.dev) · [Discussions](https://github.com/peggco/pegg/discussions)

[![Latest release](https://img.shields.io/github/v/release/peggco/pegg-kanban?style=flat-square&sort=semver)](https://github.com/peggco/pegg-kanban/releases)
[![Go 1.26.5+](https://img.shields.io/badge/go-1.26.5%2B-00ADD8?style=flat-square&logo=go&logoColor=white)](https://github.com/peggco/pegg-kanban)
[![Plugin protocol v2](https://img.shields.io/badge/plugin%20protocol-v2-black?style=flat-square)](https://docs.pegg.dev/pegg/configurations/plugins/)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-black?style=flat-square)](https://github.com/peggco/pegg-kanban/blob/HEAD/LICENSE)

**Install**

```bash
pegg plugins install peggco/pegg-kanban
```

Then load the `kanban` skill with `/kanban` before the first board tool call.
</div>

## Features

- Five columns: `backlog`, `ready`, `in_progress` (trigger), `review`, `done` (terminal).
- Drag-and-drop with optimistic updates and toasts; accessible column select fallback.
- Card drawer with editable fields, run state, cancel/retry, and a live log tail.
- Bounded parallel agent runs (`max_parallel`), per-card logs, process-group cancel.
- Board stored at `<workdir>/.pegg/kanban/board.json` with a file lock and atomic rename.
- SSE live updates (`/api/events`) with automatic reconnect and polling fallback.
- Provider-neutral issue sync, feedback, transitions, and change requests through hooks.
- No provider names, credentials, or API clients inside the Kanban plugin.
- Dark/light through `prefers-color-scheme`, responsive down to 360 px, visible focus
  rings, `Escape` closes dialogs, `n` opens the new-card dialog.

## Build and install

```bash
cd pegg-kanban
go build -o bin/pegg-kanban .
mkdir -p ~/.pegg/plugins
cp bin/pegg-kanban ~/.pegg/plugins/pegg-kanban
chmod +x ~/.pegg/plugins/pegg-kanban
```

`go.mod` requires `github.com/peggco/pegg v0.2.4`.

Verify:

```bash
pegg plugins list --runtime   # kanban should load
pegg kanban status
```

The orchestrator installs the binary after verification; this repository does not
install itself.

## Quick start

```bash
cd /path/to/a/git/repo
pegg-kanban add "Fix the login redirect" --column ready --body "Reproduce and fix it"
pegg-kanban start
pegg-kanban list
pegg-kanban move <card-id> in_progress
pegg-kanban logs <card-id> --tail 100
pegg-kanban stop
```

Or through Pegg:

```bash
pegg kanban                  # detached server + browser
pegg kanban add "Smoke test" --column ready
pegg kanban list
pegg kanban stop
```

The board belongs to the current workspace (`--workdir`, default `$PWD`). Run the
commands from the repository you want the agent to edit, or pass `--workdir`.

The TUI and desktop settings pages start the board as an **embedded** server owned by the
Pegg window. Closing that window stops the server. If a healthy server for the same board
is already running, the window reuses it instead of starting another. The `pegg kanban
start` CLI command still starts a detached server that survives the CLI; stop it with
`pegg kanban stop`.

## Configuration

Options live under `plugins.options["kanban"]` in `~/.pegg/pegg.json`, or under the
plugin `Deps.Options`. Environment overrides are applied last.

```json
{
  "plugins": {
    "enabled": true,
    "options": {
      "kanban": {
        "enabled": true,
        "host": "127.0.0.1",
        "port": 7878,
        "api_token": "",
        "auto_open": true,
        "pegg_binary": "",
        "agent_args": [],
        "provider": "",
        "model": "",
        "max_parallel": 2,
        "trigger_column": "in_progress",
        "review_column": "review",
        "auto_move_on_success": true,
        "auto_answer": true,
        "auto_answer_value": "1",
        "base_branch": "",
        "sync_column": "backlog",
        "sync_minutes": 0,
        "sync_update_existing": true,
        "sync_labels": [],
        "sync_limit": 50,
        "sync_state": "",
        "feedback": true,
        "create_change_request": false,
        "transition_on_success": "",
        "hooks_timeout_seconds": 90
      }
    }
  }
}
```

| Option | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Disable tools, skills, and commands |
| `host` | `127.0.0.1` | Bind address; non-loopback requires a token |
| `port` | `7878` | HTTP port |
| `api_token` | empty | Require `X-PegG-Token` or `Authorization: Bearer` on `/api/*` |
| `auto_open` | `true` | Open the browser when starting detached |
| `pegg_binary` | resolved | `$PEGG_BIN`, `~/.pegg/bin/pegg`, then `pegg` on `PATH` |
| `agent_args` | `[]` | Extra arguments inserted before the prompt, e.g. `["--model","x"]` |
| `provider` / `model` | empty | Stored for host configuration; the runner does not invent flags for them |
| `max_parallel` | `2` | Concurrent agent runs |
| `trigger_column` | `in_progress` | Column that starts a run |
| `review_column` | `review` | Column for successful runs |
| `auto_move_on_success` | `true` | Move succeeded cards to `review` |
| `auto_answer` | `true` | Feed `auto_answer_value` to pegg's interactive prompts so autonomous runs never hang |
| `auto_answer_value` | `1` | Answer written to stdin (`1` = Allow / first option) |
| `base_branch` | empty | Base branch passed to the optional worktree hook |
| `sync_column` | `backlog` | Import destination column |
| `sync_minutes` | `0` | Scheduled sync interval; `0` disables. Providers that report `sync_minutes` in `kanban.providers` also start a loop |
| `sync_update_existing` | `true` | Refresh titles/bodies/labels for non-trigger, non-terminal cards |
| `sync_labels` | `[]` | Default issue label filter |
| `sync_limit` | `50` | Maximum issues per provider sync |
| `sync_state` | empty | Default issue state; the provider may override it |
| `feedback` | `true` | Ask integration plugins to comment after a successful run |
| `create_change_request` | `false` | Ask integration plugins to open a PR/MR after a successful run |
| `transition_on_success` | empty | Status to pass to integrations that support transitions |
| `hooks_timeout_seconds` | `90` | Timeout for hook dispatch calls |

Environment overrides:

| Variable | Meaning |
|---|---|
| `PEGG_KANBAN_HOST` | Bind host |
| `PEGG_KANBAN_PORT` | Bind port |
| `PEGG_KANBAN_TOKEN` | API token |
| `PEGG_KANBAN_MAX_PARALLEL` | Concurrency |
| `PEGG_KANBAN_PEGG_BIN` | Agent binary |
| `PEGG_HOOKS_TIMEOUT` | Hook timeout in seconds |
| `PEGG_KANBAN_BOARD` | Absolute board path override |
| `PEGG_KANBAN_SERVER` | Live server URL used by tools inside an agent run |
| `PEGG_KANBAN_SYNC_COLUMN` | Import destination column |
| `PEGG_KANBAN_SYNC_MINUTES` | Scheduled sync interval |
| `PEGG_KANBAN_SYNC_UPDATE_EXISTING` | `true`/`false` |
| `PEGG_KANBAN_SYNC_LABELS` | Comma-separated labels |
| `PEGG_KANBAN_SYNC_LIMIT` | Maximum issues per sync |
| `PEGG_KANBAN_SYNC_STATE` | Default issue state |
| `PEGG_KANBAN_FEEDBACK` | `true`/`false` |
| `PEGG_KANBAN_CREATE_CHANGE_REQUEST` | `true`/`false` |
| `PEGG_KANBAN_TRANSITION_ON_SUCCESS` | Status for integrations |
| `PEGG_KANBAN_BASE` | Base branch for the optional worktree hook |
| `PEGG_KANBAN_AUTO_OPEN` | `true`/`false` |
| `PEGG_KANBAN_AUTO_ANSWER` | `true`/`false` |
| `PEGG_KANBAN_AUTO_ANSWER_VALUE` | Answer written to stdin |
| `PEGG_KANBAN_OPTIONS` | Internal JSON settings snapshot used by detached start |

Direct `pegg-kanban` invocations load `~/.pegg/pegg.json` (`plugins.options.kanban`),
then apply environment, then flags. When commands run through the Pegg host, the
effective host options are forwarded to the detached server. The internal
`PEGG_KANBAN_OPTIONS` variable carries that JSON snapshot (without the token; the
token uses `PEGG_KANBAN_TOKEN`).

`GET /api/config` and `kanban_config` return a sanitized view: the token is replaced by
`token_configured: true`.

## Files on disk

```text
<workdir>/.pegg/kanban/
├── board.json          # board, columns, cards, run state, per-provider sync metadata
├── board.json.lock     # O_CREATE|O_EXCL lock, 25 ms retries, 2 s timeout, 10 s stale
├── events.jsonl        # append-only events (card.created, card.moved, run.*, sync.completed)
├── logs/<card-id>.log  # combined stdout/stderr of the agent run
├── server.json         # server metadata {pid,host,port,url,started_at,workdir,board,embedded}
└── server.log          # detached server stdout/stderr
```

`NewStore` creates the directory and `logs/`. Every write reloads under the lock,
applies the mutation, updates `updated_at`, writes `board.json.tmp`, and renames it over
`board.json`. Positions are zero-based and renumbered per column.

## Web UI

`http://127.0.0.1:7878/` serves `web/index.html`, `web/app.js`, and `web/style.css` from
`go:embed`. No fonts, scripts, or styles are fetched remotely.

- **Header**: board name, repo/workdir, live server state, search, issue sync, new card.
- **Columns**: counts, optional WIP limits (`count/wip`, red when over), drop targets.
- **Cards**: title, priority, source, issue link, branch, run badge.
- **Drag and drop**: drops POST `/api/cards/{id}/move`; the UI updates optimistically
  and reconciles with the server response. Dropping into the trigger column starts a run.
- **Drawer**: editable title, description, column, priority, labels, assignee; optional
  worktree/branch, issue, run metadata; Save, Run, Retry, Cancel, Delete; log viewer
  polling every 2 s while open.
- **New card**: title, description, column, priority, labels, optional issue repo/number.
- **Issue sync**: provider, target, labels, state, limit, query; shows imported/updated counts or per-provider errors.
- **Live updates**: `/api/events` SSE. After three failures the UI polls every 10 s and
  keeps retrying SSE with backoff.
- **Keyboard**: `Escape` closes the topmost dialog/drawer, `n` opens the new-card dialog
  when focus is not in a form. Cards open with Enter/Space.

If an API token is configured, `pegg kanban open` appends `?token=...` to the browser URL
so the page can authenticate its fetches and EventSource connection. The token is never
printed by commands or tools.

## Runner lifecycle

1. A card is moved into `trigger_column` (or `POST /run` is called).
2. The runner marks it `queued` and waits for the `max_parallel` semaphore.
3. When a slot is free it calls the `kanban.run.prepare` filter. If no handler changes
   the context, the run uses the repository root. It writes `logs/<card-id>.log` and
   marks the run `running` with `started_at` and `pid`.
4. It spawns:

   ```text
   <pegg_binary> run --auto [agent_args...] <prompt>
   ```

   with `cmd.Dir` set to the worktree or repository root, `SysProcAttr{Setpgid: true}`,
   and stdout+stderr
   redirected to the card log. The child environment includes the inherited environment
   plus:

   ```text
   PEGG_KANBAN_SERVER=http://127.0.0.1:<port>
   PEGG_KANBAN_CARD=<card id>
   PEGG_KANBAN_BOARD=<absolute board.json path>
   PEGG_KANBAN_TOKEN=<token, when configured>
   ```

   With `auto_answer` enabled, stdin is a pipe and the runner writes
   `auto_answer_value` (default `1` = Allow / first option) at most once every 200 ms.
   This keeps pegg's interactive permission and ask prompts from stalling or rejecting an
   autonomous run. The writer stops when the child exits.

5. Exit code `0` means success: the card moves to `review_column` (unless disabled),
   then the `kanban.card.succeeded` action runs. Non-zero keeps the card in the
   trigger column with `run.status=failed`, `exit_code`, and the last 20 log lines in
   `run.error`.
6. Cancel sends `SIGTERM` to the process group, waits 5 s, then `SIGKILL`, and marks the
   card `cancelled`. Retry re-queues it.
7. On server start, any `queued` or `running` card is marked `failed` with
   `error = "interrupted by server restart"`.

The prompt includes the card title/id, priority, labels, description, repo, optional run
environment/branch, and linked issue, followed by explicit instructions to run tests,
commit, push when a remote exists, and create a change request when appropriate.

## Optional run environments

Kanban core does not create worktrees or containers. By default the agent runs in the
repository root. Integrations can change the working directory, branch, environment, and
display metadata through the generic `kanban.run.prepare` filter:

```json
{
  "card": {"id": "card-...", "title": "Fix login"},
  "repo": "/abs/path/to/repo",
  "base_branch": "main",
  "context": {
    "kind": "repo",
    "path": "/abs/path/to/repo",
    "base": "main",
    "label": "repository"
  }
}
```

A handler returns the same value with a modified `context`. The GitHub plugin sets a
worktree context:

```json
{
  "context": {
    "kind": "worktree",
    "path": "/abs/path/.pegg-worktrees/card-...",
    "branch": "pegg/card-...",
    "base": "main",
    "label": "card-..."
  }
}
```

`kind` is free-form: `worktree`, `container`, `sandbox`, or anything else. Kanban uses
`path` as `cmd.Dir`, `branch` in the prompt, and merges `env` into the child process
environment. If no handler is registered, or `path` is empty, the run uses the repository
root. If a handler returns an error, the run fails with that error. Kanban stores a
sanitized `card.context` for display and for the success hook; `env` is never persisted.

## Integration hooks

Kanban contains no provider names, credentials, API clients, or bridge binaries. Issue
integrations register handlers for three hooks through the Pegg plugin SDK:

| Hook | Kind | Payload | Result |
|---|---|---|---|
| `kanban.providers` | filter | `[]ProviderInfo` | handlers append their provider |
| `kanban.issues.fetch` | filter | `IssueFetch` | handlers append normalized issues |
| `kanban.run.prepare` | filter | `RunPrepare` | handlers change the run context |
| `kanban.card.succeeded` | action | `CardSucceeded` | optional JSON output |

`ProviderInfo`:

```json
{
  "id": "provider-id",
  "label": "Display name",
  "enabled": true,
  "available": true,
  "authenticated": true,
  "detail": "actionable status",
  "target": "default repo or project",
  "sync_minutes": 0
}
```

`IssueFetch` / `HookIssue`:

```json
{
  "provider": "provider-id",
  "target": "acme/widgets",
  "labels": ["bug"],
  "state": "open",
  "limit": 50,
  "query": "",
  "issues": [
    {
      "external_id": "42",
      "number": 42,
      "key": "",
      "title": "Fix login",
      "body": "broken",
      "url": "https://example.test/issues/42",
      "labels": ["bug"],
      "assignees": ["dev"],
      "state": "open",
      "priority": "high",
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

`CardSucceeded`:

```json
{
  "card": {"id": "...", "title": "...", "issue": {"provider": "provider-id", "repo": "acme/widgets", "number": 42, "url": "..."}},
  "context": {"kind": "worktree", "path": "...", "branch": "pegg/...", "base": "main", "label": "..."},
  "summary": "truncated agent output",
  "feedback": true,
  "create_change_request": false,
  "transition_on_success": "",
  "base_branch": "main"
}
```

### Dispatch

Kanban invokes hooks through `github.com/peggco/pegg/pkg/plugin/hooks`. The client sends
each call to the Pegg host over the plugin RPC connection the host already serves, so no
extra `pegg` process is started and no hooks binary needs to be configured. The host
dispatches to every integration plugin that handles the hook and skips Kanban's own
handlers, which prevents hook recursion. Handler errors are returned per plugin in
`errors`; one broken integration does not stop the others.

Hook dispatch requires a Pegg host that serves the plugin hook dispatcher. Kanban reports
`hooks: no host dispatcher available` when it runs outside such a host (for example a
detached server started without the plugin runtime).

For manual debugging, the host CLI can still apply a single hook:

```text
pegg hooks apply <name> --kind filter|action --value - --json
```

### Behavior

- `kanban.providers` drives `/api/config`, the web provider picker, the TUI page, the
  sync loop, and `kanban_status`.
- `kanban.issues.fetch` runs once per selected provider. New issues become cards with
  `source=<provider>`, `issue.provider`, `issue.repo/target`, `issue.number` or
  `issue.key`, and `issue.url`. Existing cards are deduplicated by provider + target +
  number/key and refreshed when `sync_update_existing` is on and the card is not in the
  trigger or terminal column.
- `board.sync[<provider>]` stores `last_sync`, `last_status`, `last_error`, `imported`,
  `updated`, and `target`.
- After a successful run, `kanban.card.succeeded` receives the card, optional worktree, truncated
  log summary, and the generic `feedback`, `create_change_request`,
  `transition_on_success`, and `base_branch` settings. Each integration decides whether
  to comment, transition, or open a PR/MR.

### Without integration plugins

Kanban is fully usable with no integration plugin installed: manual cards, drag and drop,
optional worktrees, agent runs, logs, cancel/retry, SSE, and CLI/TUI actions all work offline. The
provider picker shows no providers and sync is disabled until a plugin registers
`kanban.providers`.

The integration plugins are separate hooks-only binaries.

**Issue providers**

- `pegg-kanban-github` — GitHub issues, worktree run prep, comment + pull request
- `pegg-kanban-gitlab` — GitLab issues, worktree run prep, comment + merge request
- `pegg-kanban-jira` — Jira issues, comment + status transition

**Code-host integrations**

- `pegg-kanban-bitbucket` — Bitbucket Cloud/Data Center worktree run prep and pull
  requests. It does not fetch issues; pair it with Jira, GitHub, or GitLab as the
  issue provider.

Each owns its credentials, repository/project defaults, labels where applicable,
state, API client, and redaction. They expose no agent tools or skills; kanban is the
only agent-facing surface and only sees normalized hook JSON.

## Tools

Bundle `kanban`, group `Kanban`. Read-only tools are permission `allow`; the rest are
`semi-judge`.

| Tool | Type | Purpose |
|---|---|---|
| `kanban_status` | read-only | Server/board status, columns, counts, active runs |
| `kanban_list` | read-only | List cards, optionally by column/run status |
| `kanban_add` | write | Create a card; optional issue reference |
| `kanban_update` | write | Edit title/body/priority/labels/assignee/column |
| `kanban_move` | write | Move to a column/position; entering the trigger starts a run |
| `kanban_run` | write | Start a card explicitly |
| `kanban_cancel` | write | Cancel a queued/running card |
| `kanban_log` | read-only | Tail a card log |
| `kanban_sync` | write | Import/update issues from hook providers |
| `kanban_config` | read-only | Sanitized configuration |

When `PEGG_KANBAN_SERVER` is set, all tools call the live HTTP API. Otherwise they use
the local store and runner directly (the plugin process is long-lived, so runs persist).
The skill `kanban` documents board semantics, trigger behavior, optional worktrees, logs,
hook-driven issue sync, and the one-card/one-agent-run rule.

## Commands

```text
pegg kanban                  Start detached, open the browser, print the URL
pegg kanban start            Start detached
pegg kanban stop             SIGTERM a detached server, wait up to 5 s; embedded servers are stopped by closing their Pegg window
pegg kanban status           Health + board summary
pegg kanban add <title>      --column --body --priority --labels --assignee
pegg kanban list             --column; lists all columns
pegg kanban move <id> <col>  Uses the live server when running, otherwise local + trigger
pegg kanban sync             --provider --target --query --labels --state --limit
pegg kanban logs <id>        --tail (default 200)
pegg kanban open             Start if needed and open the browser
```

Direct CLI for debugging:

```bash
./bin/pegg-kanban serve --foreground --host 127.0.0.1 --port 7878 \
  --workdir /path/to/repo --pegg-bin /path/to/pegg --max-parallel 2 --open
./bin/pegg-kanban add "Title" --column ready --workdir /path/to/repo
./bin/pegg-kanban status --workdir /path/to/repo
```

`pegg kanban start` spawns a detached `pegg-kanban serve --foreground` with `Setsid`,
writes `.pegg/kanban/server.log` and `.pegg/kanban/server.json`, then polls `/api/health`
for up to 5 seconds. A healthy server for the same board is reused. TUI/desktop starts
instead use an embedded server in the plugin process; `server.json` marks it
`embedded: true`, and the plugin stops it on close.

## HTTP API

All API responses are JSON unless noted. If `api_token` is set, requests need
`X-PegG-Token`, `Authorization: Bearer`, or (for EventSource) `?token=`.

| Method | Path | Result |
|---|---|---|
| GET | `/api/health` | `{ok,version,pid,url,board}` |
| GET | `/api/board` | full board |
| POST | `/api/cards` | create; `201` + card |
| GET | `/api/cards/{id}` | card or `404` |
| PATCH | `/api/cards/{id}` | update; column change can trigger |
| DELETE | `/api/cards/{id}` | `{ok:true}` |
| POST | `/api/cards/{id}/move` | `{column,position}`; `200` + card |
| POST | `/api/cards/{id}/run` | `202` + card |
| POST | `/api/cards/{id}/cancel` | `200`; `409` if not active |
| POST | `/api/cards/{id}/retry` | `202` + card |
| GET | `/api/cards/{id}/log?tail=200` | `text/plain` tail |
| POST | `/api/sync` | `{provider,target,labels,state,limit,query}` → stats or per-provider results |
| GET | `/api/events` | SSE `event: board` / `event: card`, 25 s heartbeat |
| GET | `/api/config` | sanitized settings |

Limits: 1 MiB request bodies (`413` when exceeded), panic recovery, `nosniff` and
`DENY` frame headers, and per-request logging to the plugin's stderr.

## Security

- Binds loopback by default. A non-loopback bind without `api_token` is refused.
- Tokens are never printed, logged, returned by `/api/config`, or included in tool output.
- Error text from hooks is redacted for `token`, `Authorization`, `Bearer`, `ghp_*`,
  `github_pat_*`, and `glpat-*`.
- Log filenames are sanitized; card IDs cannot escape the state directory.
- Integration plugins resolve their own credentials; Kanban never reads or passes them.

## Testing

```bash
gofmt -l .          # empty
go vet ./...
go test ./...
go test -race ./...
go build -o bin/pegg-kanban .
```

Tests never call a real model:

- `runner_test.go` uses a fake `pegg` shell script that writes marker files, exits `0`/`3`,
  or traps `TERM` for the cancel test.
- `hooks_test.go` uses a fake hook runner for provider discovery, import/update, error
  isolation, run-success payloads, and board migration.
- `server_test.go` uses `httptest` with a temporary board.

Coverage includes default board creation, position renumbering, lock contention between
two stores, atomic writes, event log entries, config precedence and sanitization, CRUD and
auth, SSE first event, oversized bodies, log tailing, optional worktree hook usage,
runner success/failure/cancel/interrupt, hook-driven import/dedupe/update, and legacy
  board migration.

## Limitations

- Linux/macOS process-group semantics are assumed (`Setpgid`, negative PID signals,
  `xdg-open`/`open`). Windows browser launching is present, but process-group cancel is
  POSIX-specific.
- `provider` and `model` are configuration passthroughs; pass provider/model choices via
  `agent_args` for the installed Pegg CLI.
- Scheduled sync runs only while the server process is alive; intervals come from `sync_minutes` or a provider's `sync_minutes`.
- The web UI deliberately has no build step, bundler, or external dependency. It favors a
  small, readable plain-JS client over a framework.

## License

Apache-2.0. See [LICENSE](https://github.com/peggco/pegg-kanban/blob/HEAD/LICENSE).
