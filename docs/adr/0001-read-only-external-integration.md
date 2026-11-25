# Track OpenCode TUI activity through read-only external integration

To keep OpenCode usable independently, the tmux plugin builds its status overview from tmux panes, tab lists saved by the TUI, and read-only data from the local shared service. It does not install an OpenCode plugin, modify OpenCode settings, or manage the service lifecycle. Compared with having an OpenCode plugin report activity directly, this approach requires less setup from users. In return, the tmux plugin takes on the compatibility risk of a non-public tab storage format and must resynchronize its data after a disconnect.

Users explicitly choose cwd or global plugin mode; cwd is the default. The plugin does not infer each TUI client's effective tab scope, so users must keep the plugin mode aligned with their OpenCode settings. This deliberately leaves out setups where individual TUI clients override their tab scopes differently, avoiding the deeper integration needed to read each client's settings.
