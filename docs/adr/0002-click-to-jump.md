# Always enable click-to-jump by taking over `MouseDown1SideStatus`

Clicking any row in a directory group switches the tmux client to that group's **jump target**: the pane hosting its TUI client, even in a different tmux session. This feature is always on and has no toggle.

- On startup, the daemon takes over `MouseDown1SideStatus` in the root key table. If the range string is `oc<N>`, it switches to pane `%N`; otherwise it runs tmux's default `switch-client -t =`. The binding runs entirely within tmux, without starting a shell or invoking the plugin binary.
- When explicitly stopped, the daemon restores tmux's default binding, `switch-client -t =`, rather than using `unbind`. After stopping, `side-status-format` returns to the tmux default, which relies on this binding for click-to-switch behavior on sessions and windows. During a handoff, the outgoing instance leaves the binding alone and the incoming instance rewrites it. There is no cleanup when the tmux server exits.
- **The plugin does not save or restore a user's custom binding.** This keeps behavior simple and predictable, at the cost of overwriting a custom `MouseDown1SideStatus` binding. The README documents this trade-off.
- The sidebar wraps each group row separately in `#[range=user|oc<N>]…#[norange]` rather than relying on a range to span line breaks. The range argument consists only of a fixed prefix and decimal digits; it contains no user data.

## Why use `range=user` instead of `range=pane`?

Testing and source inspection of tmux next-3.9 confirmed:

- In status formats, `strftime` consumes the `%` in `range=pane|%N`; escape it as `%%N`.
- Even with the escape, the mouse event sets only the pane; the session and window remain the clicking client's current ones. `cmd_mouse_pane()` backs out if `window_has_pane()` fails, so the event can only switch panes within the same window, not across sessions.
- `mouse_status_range` receives only the string `pane`, not the pane number.
- `range=user|X` triggers the `SideStatus` key and puts `X` (up to 15 bytes) in `mouse_status_range`. The binding can then switch to the exact pane, even across sessions.

Revisit this choice if tmux adds cross-session support to `range=pane`.

## Relationship to other decisions

This does not conflict with ADR-0001 (read-only access to OpenCode): the binding changes only tmux, not OpenCode. It supersedes the earlier README promise not to add keyboard or mouse bindings.
