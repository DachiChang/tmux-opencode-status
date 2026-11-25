# OpenCode Status Sidebar

Shows the status of open OpenCode TUI tabs across working directories on the same tmux server.

## Language

**TUI client**:
A running instance of the OpenCode terminal interface. A tmux pane hosts one when its foreground command is `opencode` and it is on the alternate screen. Multiple TUI clients can run in the same directory.
_Avoid_: Agent (when referring to the terminal interface)

**Tab**:
An open work item in the OpenCode TUI that corresponds to one session. Open tabs are not the same as the full session history.
_Avoid_: Session (as a synonym for an open tab)

**Session**:
An OpenCode conversation and its execution history. A session can be the one associated with a tab or a descendant of that session.

**Session family**:
A session and all its descendants at any depth. Descendant activity contributes to the corresponding tab's status rather than appearing as separate subagent entries.

**Directory group**:
The set of tabs for a working directory with at least one running TUI client. TUI clients in the same directory belong to the same group.

**Directory identity**:
The identifier OpenCode uses to store a directory's tab list: the TUI client's physical working directory. TUI clients opened through a symlink share the physical directory's tab list. Directories are not merged because they belong to the same Git repository; separate worktrees remain distinct.

**Jump target**:
The tmux pane hosting the TUI client that a group jumps to, not a tab inside the TUI. If several clients qualify, the target is the pane with the lowest numeric ID (`N` in `%N`), among clients in the directory group or among all TUI clients in global mode.

**Jump mode**:
A per-tmux-client state between pressing the trigger key and pressing the next key. Only that client's sidebar shows jump labels on group rows; pressing a label's letter jumps to its target, and any other key exits the mode.

**Trigger key**:
The key in the tmux prefix table that enters jump mode, defaulting to `A` (`prefix + A`). A user-defined binding in the root table that directly enters the jump key table is not the trigger key.
_Avoid_: Shortcut (ambiguous between prefix and root bindings)

**Jump key table**:
The tmux key table that receives the next key in jump mode and binds `a`–`z` to the corresponding jump labels. Its name is fixed as `opencode-status-jump` and is part of the plugin's public interface; users can enter it directly through a binding in the root table (for example, `bind -n M-a switch-client -T opencode-status-jump`).
_Avoid_: Jump keyboard, jump shortcut table

**Jump label**:
A letter that replaces `▎` on a group row in jump mode, assigned from `a` through `z` in sidebar order only to groups with jump targets; groups without a letter keep `▎`. Sidebar height does not affect assignment: a group's letter still works even when its row is off-screen.

**Status sidebar**:
An overview of OpenCode activity shared across tmux sessions on the same server. Each tmux server has its own overview.

**Tab status**:
The single status shown for a tab, in priority order: waiting for approval, waiting for an answer, running, failed unread, finished unread, and idle. The initial version does not consider queued prompts.

**Waiting for approval**:
The session family has at least one pending permission request.

**Waiting for an answer**:
The session family has at least one unanswered question and no pending permission requests.

**Running**:
At least one session in the family is running, and nothing is waiting for approval or an answer.

**Unread**:
OpenCode has not marked the end of the tab's own most recent run as read. OpenCode clients share this state; the sidebar keeps no read state of its own.

**Failed unread**:
The tab's own most recent run failed and is unread, and its session family has no higher-priority status.

**Finished unread**:
The tab's own most recent run ended without failure and is unread, and its session family has no higher-priority status. This includes both successful and interrupted runs.
_Avoid_: Completed unread (can imply success)

**Idle**:
The tab is confirmed to have no higher-priority status. A tab is not idle just because current data is unavailable.

**Needs attention**:
The collective term for waiting for approval and waiting for an answer. Both appear as `?`, but remain distinct statuses with separate priority levels.

**Unknown status**:
The tab's current status cannot be verified because data is unavailable. This does not imply that the tab is idle, that a run failed, or that its last known status still applies.

**Missing session**:
The tab still exists, but OpenCode has explicitly reported that its corresponding session does not exist. This is distinct from temporary data unavailability and does not remove the tab.

**Data problem**:
The collective term for unknown status and missing session. Both appear as `!` without extra text next to the tab title, and neither implies a failed run.

**Tab scope**:
The scope within which OpenCode TUI clients share a tab list: either cwd scope or global scope.

**Cwd scope**:
The tab scope that maintains a separate shared tab list for each TUI working directory.

**Global scope**:
The tab scope that shares one tab list regardless of TUI working directory.

**Plugin mode**:
The plugin's explicitly configured tab list source: either cwd or global, never both. It does not reflect an automatically detected TUI tab scope.

**Cwd mode**:
The plugin mode that includes only cwd tab lists for directories with running TUI clients. Multiple TUI clients in the same directory share one list.

**Global mode**:
The plugin mode that includes one shared global tab list whenever at least one TUI client is running, without filtering by individual session directories.
