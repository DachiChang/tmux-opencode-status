# tmux-opencode-status guide

English · [繁體中文](guide.zh-TW.md) · [Quick start](../README.md)

See the status of open OpenCode TUI tabs across a tmux server in tmux 3.9's native **side-status**. The sidebar shows which tabs are waiting for approval or an answer, running, failed unread, finished unread, idle, or affected by a data problem.

The plugin is read-only with respect to OpenCode: it does not install an OpenCode plugin, change OpenCode settings, or start or restart its service. It sets only three tmux bindings: `MouseDown1SideStatus` in the root key table (see [Click to jump](#click-to-jump)), a prefix trigger key (default `A`), and the `opencode-status-jump` key table (see [Keyboard jump](#keyboard-jump)).

## Requirements

- tmux **3.9** with `side-status`, `side-status-width`, and `side-status-format`. The plugin reports an error if these options are missing; it does not fall back to another display method.
- macOS (Apple Silicon `arm64` or Intel `x86_64`). **Go is not required for normal use.**
- OpenCode on the standard **latest** channel, using the **default state directory** (`opencode/` under `$XDG_STATE_HOME` or `~/.local/state`) and the local shared background service. Other release channels, custom state directories, remote or private service instances, and aggregation across separate data environments are not supported.

## Installation details

```tmux
set -g @plugin 'DachiChang/tmux-opencode-status'
# Optional: cwd (default) or global
# set -g @opencode-status-mode global
# Optional: override a theme color; see Theme colors below
# set -g @opencode-status-color-project colour253
```

Press `prefix + I` to install. TPM runs `tmux-opencode-status.tmux` from the plugin directory. The script:

1. Checks that tmux has the required capabilities.
2. Uses the plugin's `VERSION` and `uname -m` to download `tmux-opencode-status_<version>_darwin_<arch>.tar.gz` and `SHA256SUMS` from the corresponding GitHub release.
3. Verifies the SHA-256 checksum, archive contents, and executable architecture before installing into the plugin's `bin/<version>-darwin-<arch>/` directory.
4. Runs `tmux-opencode-status start -show`.

The install directory uses `arm64` on Apple Silicon and `amd64` on Intel macOS (`x86_64`).

If the installed binary passes verification, the script does not download it again. **Any failure** (a failed download or verification, an unsupported architecture, or missing tmux capabilities) is reported on stderr and through `tmux display-message`. The script **never falls back to `go build` or to a binary for a different architecture**.

### Default behavior

- The sidebar defaults to the **left side**, **36 terminal cells** wide (not 36 characters). It opens once per tmux server on first load. Reloading the plugin or handing it off to a new daemon instance will not reopen it after you hide it.
- **Known limitation: the plugin cannot honor an explicit `set -g side-status off` on first load.** tmux defaults `side-status` to `off`, and neither `show-options` (including `-g`, `-q`, `-A`, and `-H`) nor format variables distinguish the default from an explicit setting. In tests with tmux next-3.9, an unset option, `set -g side-status off`, and `set -gu side-status` all produced identical output (`options-table.c` defines `default_num = 0`). On first load, `start -show` therefore opens the sidebar once. Reloads, handoffs, and daemon restarts on the same tmux server do not reopen it. To keep it hidden on startup, run `start` without `-show` (for example, when starting the plugin manually outside TPM). Use `tmux set -g side-status left` when you want to show it.
- Repeated loads (via `tmux source-file` or TPM updates) are idempotent: there is one daemon instance per tmux socket. If the configuration or executable changes, the old instance hands off to the new one. A successful `start` confirms that an instance holds the lock and has recorded its configuration, not that the sidebar is already visible.
- When the tmux server exits, the daemon exits and cleans up plugin resources.

## Jumping and key bindings

### Click to jump

Click any row in a **directory group** (the directory row, one of its tab rows, or the `No tabs` row) to switch to the **pane hosting that group's TUI client**. This switches the session, window, and pane together, even across tmux sessions or if the pane is not the active pane in its window. This feature is always enabled and requires no plugin configuration.

- **Multiple TUI clients:** If several TUI clients are running in the same directory, the jump target is the pane with the lowest numeric ID (`N` in `%N`, so `%3` comes before `%13`). In global mode, the `GLOBAL` group picks from all TUI clients. The lowest ID indicates the earliest pane creation, not the earliest OpenCode launch.
- **Pane only:** The plugin is read-only and cannot switch to a particular tab inside the TUI.
- **Other rows:** Clicking the title, divider, read-error notice, `No OpenCode TUI running`, empty space, or another non-plugin range follows the tmux default (`switch-client -t =`).
- **Jump targets update with the sidebar:** When a TUI opens, closes, or moves to another pane, the next sidebar update reflects the change.

**This overrides your `MouseDown1SideStatus` binding.** On startup, including a handoff to a new instance, the daemon installs its binding in the root key table. An explicit `stop` **restores only the tmux default** (`switch-client -t =`); it does not save or restore your custom binding.

The binding disappears when the tmux server exits. If the daemon is forcibly killed (`kill -9`), the binding remains until the next startup rewrites it, but clicks on non-plugin ranges still run `switch-client -t =`. The binding runs entirely within tmux, without opening a shell or invoking the plugin binary. See [ADR-0002](adr/0002-click-to-jump.md) for the rationale.

### Keyboard jump

Press the trigger key (`prefix` then `A` by default) to enter **jump mode**. In that client's sidebar only, purple-backed **jump labels** (` a `, ` b `, …) replace `▎` at the start of each labeled group row. Press a label's letter to jump to the same pane you would reach by clicking the group, including across tmux sessions. The labels disappear after the jump. Pressing `Esc`, an unbound key, or a letter with no group exits jump mode without switching panes or sending the key to the pane.

```tmux
# Optional: change the trigger key (key name only, always bound in the prefix table).
# The default A does not conflict with tmux defaults or common plugins.
# set -g @opencode-status-jump-trigger J
# Optional: add a binding in the root table to skip the prefix.
# The plugin does not manage root keyboard bindings; the key table name is a public contract.
# bind -n M-a switch-client -T opencode-status-jump
```

- **Jump labels:** The sidebar assigns letters from `a` in group order, only to groups with a jump target. At most 26 groups get labels; the remaining groups keep `▎` and can only be selected with the mouse. Tab rows do not get labels. A group's letter still works when its row is off-screen. Labels use `emphasis` for the background and `title` for the text, with no extra color options.
- **Multiple tmux clients:** Only the client that presses the trigger key shows jump labels. The other clients' sidebars do not change. The trigger key still enters jump mode when the sidebar is hidden (`side-status off`).
- **Changing the trigger key:** After changing `@opencode-status-jump-trigger`, **reload `tmux.conf`**. TPM reruns the entry script, and the trigger setting is part of the daemon's configuration fingerprint. The handoff unbinds the old key before binding the new one. Running `set -g` without a reload does not change the binding.
- **Invalid trigger keys:** An empty value or a key name tmux does not recognize results in no prefix binding and **no fallback to `A`**. `diagnose` reports it and counts it as an issue.
- **This overrides the existing prefix binding for that key.** The default `A` is not used by tmux defaults or common plugins. If `tmux.conf` or another plugin already binds the key, this plugin takes over on load. If something else replaces the plugin's binding later, `diagnose` reports `Overwritten`.
- **Stopping and reloading:** An explicit `stop` unbinds the trigger key, clears the `opencode-status-jump` table, and removes all `@tos_*` options. **It does not restore any previous binding.** Reload your configuration to restore bindings from `tmux.conf`; restart the tmux server to restore a built-in binding. The plugin leaves your custom root binding alone, but after `stop` it points to an empty table, so pressing it is equivalent to canceling.
- The bindings run entirely within tmux, without opening a shell or invoking the plugin binary. See [ADR-0004](adr/0004-internal-option-naming.md) and [ADR-0005](adr/0005-keyboard-jump.md) for the rationale.

## Configuring the mode

```tmux
set -g @opencode-status-mode cwd      # Default: group by TUI working directory; matches OpenCode's cwd scope
set -g @opencode-status-mode global   # One GLOBAL group; matches OpenCode's global scope
```

Reload your configuration to apply changes; no tmux restart is needed. **Set the plugin mode to match OpenCode's actual tab scope.** The plugin neither detects nor warns about mismatches. For example, with OpenCode in cwd scope and the plugin in global mode, the list may be empty or stale. Mixing tab scopes across TUI clients is not supported.

Values other than `cwd` or `global` are invalid, including an empty value, differences in case, and leading or trailing whitespace. An unset option defaults to `cwd`; a read error does not. On an invalid value or read error, the plugin shows a notice and keeps the last valid mode if it has one. Otherwise, it does not start tracking.

## Theme colors

The sidebar has 13 color options, each corresponding to a global tmux user option named `@opencode-status-color-<name>`. Unset options use their defaults. Only colors are configurable; the layout, symbols, and styling (no bold text) are fixed.

| Name | Default | Used for |
| --- | --- | --- |
| `emphasis` | `#A78BFA` | `OPENCODE` title block background, `▎` before directory names, and jump label backgrounds |
| `title` | `#18181B` | `OPENCODE` title text and jump label letters |
| `split` | `#404040` | Divider below the title |
| `project` | `#F2EEE3` | Directory names |
| `active-tab` | `#cac5b5` | Tab titles for running, needs attention, finished unread, and failed unread |
| `inactive-tab` | `#71717A` | Tab titles for idle and data problems, the `No tabs` note, and the `…` when the right-hand summary runs out of room |
| `state-running` | `#4ADE80` | `●` Running (including the symbol and count in the right-hand summary) |
| `state-ask` | `#FACC15` | `?` Needs attention |
| `state-finished` | `#60A5FA` | `◆` Finished unread |
| `state-failed` | `#F87171` | `✗` Failed unread |
| `state-problem` | `#A1A1AA` | `!` Data problem |
| `state-idle` | `#71717A` | `·` Idle |
| `notice` | `#FACC15` | `▲` Read-error notice symbol and text |

```tmux
set -g @opencode-status-color-emphasis '#7C3AED'     # #RRGGBB
set -g @opencode-status-color-project colour253      # colourN / colorN (0–255)
set -g @opencode-status-color-state-running green    # Named tmux color (including bright variants such as brightred)
set -g @opencode-status-color-split default          # Terminal default
```

The four accepted formats are `#RRGGBB`, `colourN` / `colorN` (where `N` is 0–255), a named color, and `default`. Only `black`, `red`, `green`, `yellow`, `blue`, `magenta`, `cyan`, and `white` and their `bright` variants (such as `brightred`) are accepted as named colors. For other tmux color names such as `orange`, use `#RRGGBB` or `colourN` instead.

An empty value, leading or trailing whitespace, or characters that would break tmux formatting (such as `]`, `,`, or `#{`) make the value invalid. Only that option falls back to its default; the other colors still apply. The sidebar does not show an error, but `diagnose` lists the invalid options and reports the fallback.

Setting or unsetting an option (`set -gu`) or reloading `tmux.conf` updates the sidebar immediately without restarting the daemon. The plugin only reads these options; it does not write or remove them. Your settings remain in place after the daemon stops.

## Troubleshooting

### Diagnostics (read-only)

```sh
# Run inside tmux, or select a server with -L <name> / -S <path> (no fallback to the default server).
~/.tmux/plugins/tmux-opencode-status/bin/<version>-darwin-<arch>/tmux-opencode-status diagnose
```

`diagnose` reports the tmux version and capabilities, plugin mode, invalid color options, daemon status, discovered TUI client and tab counts, service connectivity, and recent errors. **It does not include credentials, prompts, conversations, or OpenCode session or tab titles.** It does not connect to OpenCode or write any options.

### Session overrides

If you have set a per-session override with `set -t <session> side-status…`, that tmux session does not follow the global setting, and the plugin **will not remove the override**. `diagnose` lists affected sessions by ID only. To inherit the global setting, use `tmux set -u -t <session> side-status`. The same applies to `side-status-width` and `side-status-format`.

### Common issues

| Symptom | What to check |
| --- | --- |
| tmux displays `Download failed` on load | Check your network and whether the release has artifacts for the requested version (`VERSION` maps to tag `v<version>`). The plugin does not build automatically; developers can use the binary override in [Building from source](#building-from-source). |
| `Integrity verification failed` or `No valid checksum` | The checksum did not match, or `SHA256SUMS` had no valid checksum for the artifact. Retry; if it keeps happening, do not use that release and report the problem. |
| `Unsupported architecture` or `Unsupported platform` | Only macOS arm64 and x86_64 are supported. |
| tmux is `missing required capabilities` | Upgrade to tmux 3.9 with the `side-status` options. Check your version with `tmux -V`. |
| The sidebar does not appear | Run `diagnose` to check the daemon, mode, and service connectivity. Confirm `side-status` is not `off`, and check for session overrides. |
| The gray `!` persists | This indicates a data problem (for example, the service is unreachable or the tab list cannot be read). Check `Recent errors` in `diagnose`. The plugin does not start the OpenCode service. |
| Theme colors do not take effect | `diagnose` lists invalid values that fell back to defaults; the sidebar does not show these errors. See [Theme colors](#theme-colors) for accepted formats. |
| The mode looks wrong or the list is empty | Check that `@opencode-status-mode` matches OpenCode's tab scope. |
| Clicking the sidebar does not jump | Jumping works only on rows within a directory group. Run `tmux list-keys -T root \| grep MouseDown1SideStatus` to confirm the plugin binding contains `mouse_status_range`, and check that `mouse` is `on`. |
| The trigger key does nothing | Look for `Keyboard jump` in `diagnose`: it may report `Bound`, `Invalid setting`, `Binding failed`, or `Overwritten`. Run `tmux list-keys -T prefix <key>` to confirm the binding is `switch-client -T opencode-status-jump`. Reload `tmux.conf` after changing `@opencode-status-jump-trigger`. |
| You want to uninstall completely | Run `tmux-opencode-status stop` to clean up plugin resources. Remove the `@plugin` entry from `tmux.conf` and press `prefix + alt + u`. |

Daemon error logs are named `<id>.log` under `$XDG_RUNTIME_DIR/tmux-opencode-status/`, or under `$TMPDIR/tmux-opencode-status-<uid>/` if `XDG_RUNTIME_DIR` is not set.

## Development and releases

### Building from source

```sh
scripts/build-dev.sh            # → bin/dev/tmux-opencode-status (requires Go)
```

```tmux
set -g @opencode-status-binary "/absolute/path/bin/dev/tmux-opencode-status"
```

When `@opencode-status-binary` is set, the entry script uses that file and **skips all downloads**. If the path does not exist or is not executable, the script reports an error instead of falling back to a download. If the option is unset, the script never builds from source automatically.

### Releasing (maintainers)

```sh
scripts/build-release.sh [version]   # Defaults to VERSION; writes dist/*.tar.gz and dist/SHA256SUMS
```

The build uses `CGO_ENABLED=0`, `-trimpath`, and fixed archive metadata. The same source and Go version produce byte-for-byte identical artifacts. [The release workflow](../.github/workflows/release.yml) builds the artifacts and creates a GitHub release when a `v<VERSION>` tag is pushed. **Building locally does not tag or publish anything; a maintainer must initiate the release.**

### Testing and release limits

Run `go test -count=1 ./...` on macOS with the required tmux capabilities. The end-to-end tests use isolated tmux servers and temporary directories. The TPM tests serve locally built release artifacts over HTTP and check that the installer does not invoke Go.

**The Intel (`x86_64`) release artifact has not yet been tested on Intel hardware.** Mach-O architecture checks and launching through Rosetta do not verify native Intel behavior. Build reproducibility has been verified on one host with the same Go version, not across hosts. Release artifacts are not signed or notarized. The checksum detects corruption or a mismatch with `SHA256SUMS`; it cannot authenticate a release if both files are replaced.

## Documentation

- [OpenCode integration contract](contracts/opencode-2.4.1.md)
- [Architecture decision records](adr/)
- [Project glossary](../GLOSSARY.md)
