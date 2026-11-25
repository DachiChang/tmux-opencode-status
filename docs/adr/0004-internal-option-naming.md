# Name internal tmux options `@tos_<purpose>_<name>` and clean up all `@tos_*` options

Global tmux user options written by the plugin start with `@tos_`, followed by a purpose category and a name. User settings use `@opencode-status-*`. The plugin reads these settings but does not write or clear them.

| Category | Options | Contents |
| --- | --- | --- |
| `layout` | `@tos_layout_main`, `@tos_layout_jump` | Sidebar layouts for normal and jump mode |
| `jump` | `@tos_jump_trigger`, `@tos_jump_key_<letter>` | The bound trigger key and the pane for each jump label |

- Cleanup expands from `@tos_sb_*` to `@tos_*`. On the first update, remove any `@tos_*` option not written by that update, including stale options from a previous instance or crash and options with old names. On an explicit stop, remove all `@tos_*` options.
- Write all `@tos_*` options together on each update, in the same `source-file` call. Rewrite even state that is only set at certain points, such as `@tos_jump_trigger`; otherwise the first update would treat it as stale and remove it.
- Do not add migration logic for the old name `@tos_sb_main`: the first update from the new version removes it as stale. ADR-0003 retains the historical name; this ADR replaces it with `@tos_layout_main`.

## Why change it?

`@tos_sb_` originally meant “sidebar layout.” Keyboard jumping (ADR-0005) adds internal state that is not layout data: the trigger key and letter-to-pane mappings. Putting that state under `sb` would be misleading. Including the purpose in each option's name makes `show-options -g` easier to read. A single cleanup prefix means new options do not require changes to the cleanup logic.

## Relationship to other decisions

This does not conflict with ADR-0001 (read-only access to OpenCode): it changes only tmux options. The `@opencode-status-shown` marker for the sidebar's first open is outside the `@tos_` namespace and is handled as before.
