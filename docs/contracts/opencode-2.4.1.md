# OpenCode 2.4.1 integration contract

This document records the OpenCode behavior the plugin relies on and the limits of the verification. It is not a compatibility guarantee for other OpenCode versions or platforms.

## Verification baseline

- **Live:** Homebrew `opencode-v2` 2.4.1 on macOS arm64, using tmux `next-3.9` and plugin commit `525ea5b`. The checks used an isolated tmux server, temporary home and XDG directories, a real OpenCode shared service bound to loopback, and a local mock provider. No tmux client was attached. No real login, remote machine connection, or external model was used.
- **Source:** [OpenCode v2.4.1 source](https://github.com/anomalyco/opencode/tree/v2.4.1) and its [tagged OpenAPI schema](https://github.com/anomalyco/opencode/blob/v2.4.1/packages/protocol/openapi.json). The live OpenAPI schema matched the tagged schemas for the plugin's endpoints after resolving references.
- **Regression fixtures:** [`testdata/opencode-2.3.0/`](../../testdata/opencode-2.3.0/) remains the original 2.3.0 recording. The 2.4.1 live checks are separate; fixture replay is not evidence of live 2.4.1 compatibility.

The plugin only reads local state and uses GET requests. It never starts or restarts OpenCode, marks a session as viewed, or changes a session. Tests that generate writes must use a separate service and synthetic data. Do not record or publish credentials, real session IDs, user paths, prompts, or raw authorization headers.

## Discovering TUI clients

A pane hosts a TUI client when `pane_current_command` is `opencode` and `alternate_on` is `1`. Its directory identity is `pane_current_path`. Discovery uses one `tmux list-panes -a` call, deduplicates panes by ID, and does not parse argv. See [`IsTUIClient`](../../internal/contract/panes.go), [`tmuxsrc`](../../internal/tmuxsrc/tmux.go), and [ADR-0006](../adr/0006-identify-tui-by-alternate-screen.md).

- **Live (alternate screen):** `opencode`, `-c`, `-s <id>`, `--auto`, `--standalone`, `.`, `<dir>`, and `--print-logs` entered the alternate screen. `mini`, `--wizard`, the `auth login/logout/switch`, `mcp auth`, and `session export` pickers, `serve`, and `acp` stayed on the main screen. `run` stayed on the main screen in the 2.3.0 fixture. `--help`, `--completions`, invalid arguments, and a nonexistent directory exited without entering it. The `session list` pager (`less -R -S`) entered it and is an accepted false positive.
- **Source:** the main TUI calls `createCliRenderer` without `screenMode` (`packages/tui/src/app.tsx`), so OpenTUI 0.9.4 defaults to the alternate screen. `OTUI_USE_ALTERNATE_SCREEN=0` disables it for that client.
- **Directory:** on macOS, tmux reads `pane_current_path` from the cwd of the tty's foreground process group leader (`osdep_get_cwd` in `osdep-darwin.c`). It is not the shell's cwd. It equaled `lsof`'s cwd for no directory, `.`, a relative path with spaces, an absolute path, `opencode <symlink>`, and `cd <symlink> && opencode`, and for every pane in [`panes.json`](../../testdata/opencode-2.3.0/panes.json).
- **Symlinks:** the cwd tab-list key is `process.cwd()` captured at TUI startup (`app.tsx`, `session-tabs.tsx`). A positional directory is applied with `process.chdir`, and the main TUI never reads `$PWD`. The key and `pane_current_path` are therefore both the physical path, so clients opened through a symlink match the physical directory's tabs.
- **External editor:** with `$EDITOR` open from the TUI, the pane still reported `opencode` with `alternate_on=1` and an unchanged path.
- **Timing:** the TUI sets its title (`OC | <session title>` in a session) after entering the alternate screen. In ten launches, no `pane-title-changed` hook fired before `alternate_on=1`. On exit, the next hook saw `alternate_on=0`. The plugin uses titles only as a wake-up signal, never to identify TUIs.
- **Former classifier gaps:** the argv classifier listed `machine`, hidden `login`, and a nonexistent positional directory as false positives. `login` and a nonexistent directory print help and exit without entering the alternate screen (`default.ts`), so they are no longer listed. Whether `machine` subcommands enter the alternate screen was not verified: in the live sandbox, `machine list` exited because machines were disabled. The plugin no longer keeps a subcommand table, so `machine` is listed only if it opens a full-screen view.

## Tab lists

The default `latest` channel stores tabs at `${XDG_STATE_HOME:-~/.local/state}/opencode/latest/tui/tabs.json`. Live 2.4.1 used this shape:

```json
{
  "global": {"tabs": [{"sessionID": "<id>", "title": "<fallback title>"}], "unread": {}},
  "cwd": {"/path/to/project": {"tabs": [], "unread": {}}}
}
```

- The plugin preserves source tab order. Cwd mode uses an exact key match against the TUI pane's `pane_current_path`; global mode reads `global.tabs` while a TUI is running. Live testing confirmed both sources and a mode change without restarting the plugin. The plugin does not infer a client's tab scope or merge the lists.
- The API session title takes precedence over the stored title. The `unread` objects were empty; the plugin derives unread status from the session API.
- A TUI on its home screen did not create a cwd entry. A missing file or missing directory entry is distinct from an unreadable or incompatible file. The plugin preserves the last available list on read errors.
- Live file events and inode changes confirmed replacement via a temporary file and rename. Watch the containing directory, not a single inode. Switching between existing tabs did not write the file in the tested case; closing a tab did. The tagged source adds retries for replacement errors, primarily for Windows; non-macOS behavior was not tested.

## Service discovery and authentication

Live 2.4.1 stored `id`, `password`, `pid`, `url`, and `version` in `${XDG_STATE_HOME:-~/.local/state}/opencode/service.json` with mode `0600`. `ocsrc.Dial` authenticated with HTTP Basic (username `opencode`, discovery-file password) and matched the `pid` and `version` returned by `GET /api/info`. Bearer authentication and a blank Basic username were rejected. `/openapi.json` required authentication; `/doc` did not.

Read the discovery file again on reconnect; do not cache the identity or credentials across service restarts. After the sandbox service stopped, the discovery file briefly named the old PID and the sandbox TUI restarted the service. The plugin saw a transient HTTP 503, then recovered without starting the service itself. The plugin rewrites a discovery URL with host `0.0.0.0` to `127.0.0.1`; that path was not exercised in this run.

## GET API and tab status

| Purpose | Endpoint and contract |
| --- | --- |
| Service identity | `/api/info`: verify `pid` and `version` against discovery. |
| Tab session | `/api/session/{id}`: read `data`, including `id`, `parentID`, `title`, `outcome`, and `time.idle` / `time.viewed`. A missing session returns `404 SessionNotFoundError`. |
| Descendants | `/api/session?parentID={id}&limit={n}&cursor={cursor}`: read `data` and `cursor.next`. The last nonempty page can still carry `next`; follow it to the empty page. The default limit was 50; `limit=0` returned 400. |
| Running sessions | `/api/session/active`: `data` maps active session IDs to `{"type":"running"}`. Absence from this map means not running; `time.busy` is not an active flag. |
| Pending approvals and questions | `/api/session/{id}/permission` and `/api/session/{id}/form`: use per-session results across the family. `/api/permission/request` and `/api/form` are location-scoped; omitting `location[directory]` did not include requests from other directories. |

[`DiscoverFamily`](../../internal/contract/discover.go) walks descendants at every depth, not ancestors or siblings. A failed read is incomplete data, not an empty result or proof of idle status. Live discovery walked a 52-member family.

[`TabStatus`](../../internal/contract/status.go) applies this priority: **waiting for approval → waiting for an answer → running → failed unread → finished unread → idle**. The first three use the whole session family; unread uses only the tab's own session. It is unread when `time.idle` exists and `time.viewed` is absent or older. Failed runs are distinct from finished runs: success and user interruption both become finished unread. A new active run takes priority over the previous `outcome` and `idle`.

Live checks covered a grandchild's pending permission, a child's pending form, descendant activity, success, failure, user interruption, and marking a run viewed via the sandbox API. Pending permission requests disappeared after a service restart; the plugin reconciled its status from fresh reads. A deleted session produced `SessionNotFound`, but the real TUI also removed the tab from its list, so the sidebar's missing-session state was not observed end to end.

## SSE and reconnects

`GET /api/event` accepts repeated `types` parameters. Use the [plugin's allowlist](../../internal/ocsrc/ocsrc.go); unknown types returned 400. The live stream began with `server.connected` and an immediate `: heartbeat` comment, then heartbeats roughly every 15 seconds. Heartbeats are not data events. Reconnecting with `Last-Event-ID` did not replay events. After the isolated service was suspended, the plugin's default 45-second idle timeout fired and the daemon reconnected and reloaded state.

- After `server.connected`, rebuild the tracked session families and refresh active sessions, permissions, and forms. Do not present stale state as current or overwrite events received during the refresh with older snapshot state.
- Execution events identify the session through `data.sessionID`. For observed completed runs, event `created` matched REST `time.idle`; `session.viewed.data.idle` matched REST `time.viewed`. The failure payload included `type`, `message`, `response`, and `status`; the plugin does not need the raw error body.
- Do not rely on `session.status` or `session.idle`; neither occurred in the live scenarios. The 2.4.1 schema adds `experimental.job.updated`, which the plugin does not subscribe to. Background jobs and queued prompts are not covered by the plugin's running-state check; the effect of background job completion on sidebar status was not verified.
- A sandbox stop or SIGTERM disconnected the stream without an observed `session.execution.interrupted` with `reason: shutdown`. In the observed case, the pending run resumed after restart and succeeded. This does not establish that a shutdown event can never occur; if received, the plugin resyncs instead of treating it as a user interruption.

## Rechecking a release

On macOS, run `go test -count=1 ./...` for plugin regression coverage. Separately, verify the exact OpenCode binary version, start an isolated shared service and tmux server, and repeat the checks above: cwd and global tabs, a paginated family, permission and form priority, unread and execution transitions, SSE silence and reconnect, and which commands enter the alternate screen. Use a loopback mock provider for execution scenarios. Never probe write operations against a user's service.

For pane evidence, run `tmux -L <socket> list-panes -a -F '#{pane_id} #{pane_current_command} #{alternate_on} #{pane_current_path}'` against the isolated tmux server. Use [`deidentify.py`](../../scripts/contract/deidentify.py) to sanitize recorded scenario, stream, and bootstrap data before adding any versioned fixtures. Keep historical fixtures under their original version. The 2.4.1 run did not verify automatic TUI mark-as-viewed behavior, the `tabs.mode=off` or `HERDR_ENV` behavior described by source, attached-client layout, or the 1-second and 10-second update timing targets.
