# Identify TUI clients by foreground command and alternate screen

A tmux pane hosts a TUI client when its foreground command is `opencode` (`pane_current_command`) **and** it is on the alternate screen (`alternate_on == 1`). The client's directory identity is `pane_current_path`. Pane discovery reads all of this with one `tmux list-panes -a` call. It does not run `ps`, read argv through `KERN_PROCARGS2`, or run `lsof`, and it keeps no table of OpenCode subcommands or flags.

- A pane that qualifies but has an empty `pane_current_path` is a read error. The daemon keeps the last available data and shows a notice. An empty string is never used as a directory key.
- Panes listed more than once through linked windows are still deduplicated by pane ID.
- `diagnose` reports a global `alternate-screen off` as a problem and a global `allow-set-title off` as a notice. It does not check per-window or per-pane overrides.

## Why change it?

The previous rule classified argv, but argv alone cannot tell a flag's value from a positional argument. The classifier copied part of OpenCode's parser into hard-coded tables, which went stale and handled edge cases incorrectly. In OpenCode 2.4.1, `--completions zsh`, `--bogus`, a nonexistent directory, `login`, and `machine` were misclassified as TUIs, while `-s run` was not recognized as one. Every new subcommand or flag required a table update. See #23 for the full evidence.

The alternate screen follows what OpenCode does, not how it parses arguments. The main TUI calls OpenTUI's `createCliRenderer` without `screenMode`, so it uses the default alternate screen. `mini`, `run`, `serve`, `acp`, `--wizard`, and the auth pickers stay on the main screen. Help, completions, and invalid arguments exit before entering the alternate screen. New subcommands need no change unless they open a full-screen view.

On macOS, tmux reads `pane_current_path` from the cwd of the tty's foreground process group leader. This is the same process and the same kernel data that `ps` and `lsof` returned. OpenCode keys the cwd tab list with `process.cwd()` captured when the TUI starts, so both values are the kernel-resolved physical path. Clients opened through a symlink therefore share the physical directory's tab list.

The TUI sets its terminal title after entering the alternate screen, so the existing `pane-title-changed` wake-up sees `alternate_on == 1`. This wake-up now also detects a TUI started in a new window or pane: the structural hook (`after-new-window`, `after-split-window`) fires before the TUI enters the alternate screen. On exit, the TUI leaves the alternate screen before clearing its title. As a result, the closed client disappears on the next title event instead of waiting for the backstop.

## Trade-offs

- **Accepted false positive:** while the `opencode session list` pager (`less`) is open, it counts as a TUI client because it uses the alternate screen and its foreground command is still `opencode`. Any future full-screen OpenCode subcommand would behave the same way. Excluding these cases would require reading argv again.
- **Preconditions:** with tmux `alternate-screen off`, every TUI client is missed. With `OTUI_USE_ALTERNATE_SCREEN=0` in OpenCode's environment, that client is missed. With tmux `allow-set-title off` or OpenCode `terminal.title: false`, detection still works, but every newly started TUI waits for the backstop, including one started in a new window. Previously, argv identified it on the structural hook. A TUI exited in an existing shell also waits for the backstop.
- **Unchanged gap:** a foreground process not named `opencode`, such as a wrapper launcher or `ssh`, is still missed.
- **Brief window:** a starting TUI reports `opencode` with `alternate_on == 0` for about 130 ms. A scan during this window, such as one triggered by `after-new-window`, does not list the TUI; the later title wake-up does.
- **Architectural dependency:** if OpenCode's main TUI ever stops using the alternate screen, this rule misses every client and must be revisited.

## Relationship to other decisions

- Consistent with ADR-0001: discovery remains read-only and only queries tmux.
- Replaces the "not merged merely because symlinks resolve to the same location" clause in the glossary's **Directory identity** entry.
