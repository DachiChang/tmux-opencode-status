# Keyboard jumping: enter jump mode with `prefix A` and select the sidebar layout by `client_key_table`

Pressing the trigger key (default `prefix A`, configurable via `@opencode-status-jump-trigger`) puts the tmux client in jump mode. The sidebar replaces `▎` on directory group rows with jump labels such as ` a ` and ` b `. Pressing a letter switches to that group's jump target, just as clicking the group does. Any other key exits jump mode.

- Bind the trigger key with `bind-key -T prefix <key> switch-client -T opencode-status-jump`. The jump key table name `opencode-status-jump` is part of the public interface. Users can bind their own root-table key (for example, `bind -n M-a switch-client -T opencode-status-jump`); the plugin neither provides nor manages such a key.
- Publish two layouts on each update, `@tos_layout_main` and `@tos_layout_jump` (named in ADR-0004). Set `side-status-format` to `#{?#{==:#{client_key_table},opencode-status-jump},#{@tos_layout_jump},#{@tos_layout_main}}`. tmux expands `client_key_table` per client, so only the client in jump mode sees the labels. The format expands the layout once via `#{@…}`, so commas and `#{` in titles do not break the conditional.
- Bind `a`–`z` in the jump key table once at startup. When pressed, each binding reads `@tos_jump_key_<letter>` (pane ID `%N`) and uses `run-shell -C` to execute `switch-client` within tmux without starting a shell. If the option is empty, the binding does nothing. Write the letter mappings and layouts in the same `source-file` call so the visible labels match their jump targets.
- In the jump layout, the renderer truncates group names to allow for the label width (three cells for the label plus one for a space), without squeezing out the group summary. Groups without a letter (the 27th eligible group onward, or groups without a jump target) retain `▎`. Labels use `emphasis` for the background and `title` for the foreground; no new colors are added.
- Record the bound trigger key in `@tos_jump_trigger`. On handoff, the incoming instance reads it and, if the key has changed, unbinds the old key before binding the new one. On an explicit stop, the daemon uses the recorded key to unbind it and clears the jump key table. **The plugin does not install a replacement binding or save or restore any previous binding.** Reloading `tmux.conf` restores an overwritten binding from that file. Restoring a built-in tmux binding requires restarting the server.
- If the trigger key is invalid (empty or unrecognized by tmux), do not bind it or fall back to the default `A`. `diagnose` reports the configured key, the bound key, and whether the binding succeeded, failed, or was overwritten. An invalid setting counts as a problem.

## Why this approach?

- No `refresh-client`: when tmux handles a key, switching key tables requests a redraw before executing the binding. Entering and leaving jump mode both redraw immediately, as confirmed by next-3.9 source inspection and hands-on testing.
- Default to `A`: it was unused in the tmux next-3.9 default prefix table, TPM, resurrect, continuum, and common plugins such as pain-control, sessionist, yank, logging, fzf, floax, fingers, sidebar, and urlview. `o` (next pane), `g` (the floating-pane move table in 3.9), `Space`, and `Tab` are tmux defaults. Users with a `C-a` prefix often bind `a` to `send-prefix`.
- Do not bind a root-table key by default: tmux intercepts root-table keys before applications see them. This has a wider impact than a prefix-table key, so leave the choice to users.
- On stop, unbind instead of restoring a default (unlike `MouseDown1SideStatus` in ADR-0002). `A` is not a built-in tmux binding, so there is no default to restore. Changing the trigger key is the user's choice.

## Relationship to other decisions

- Supersedes the earlier README promise that the only tmux binding change was taking over `MouseDown1SideStatus` and that no keyboard bindings would be added.
- Builds on ADR-0003: only the normal and jump-mode layouts are needed.
- Does not conflict with ADR-0001: it changes only tmux.
