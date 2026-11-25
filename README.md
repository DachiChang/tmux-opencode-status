# tmux-opencode-status

English · [繁體中文](README.zh-TW.md)

See the status of your open OpenCode tabs in the tmux sidebar without switching panes.

## Quick install

**Requirements:** macOS (Apple Silicon or Intel), tmux 3.9 with `side-status` support, and TPM. Use OpenCode's `latest` channel with the default state directory and local shared background service. Go is not required.

Add this line to `~/.tmux.conf` before TPM's initialization line:

```tmux
set -g @plugin 'DachiChang/tmux-opencode-status'
```

Run `tmux source-file ~/.tmux.conf`, then press your tmux prefix (`Ctrl-b` by default), followed by uppercase `I`. On first load, look for the `OPENCODE` sidebar on the left. If it doesn't appear, see [Troubleshooting](docs/guide.md#troubleshooting).

The plugin defaults to `cwd` mode. If OpenCode uses global scope, see [Configuring the mode](docs/guide.md#configuring-the-mode).

## Basic controls

- **Mouse:** With tmux's `mouse` option on, click any row in a directory group to jump to that group's OpenCode pane.
- **Keyboard:** Press `prefix + A`, then press a letter shown in the sidebar.

The plugin overrides `MouseDown1SideStatus` and the existing prefix binding for the trigger key. If you have custom bindings, see [Jumping and key bindings](docs/guide.md#jumping-and-key-bindings).

## More information

[Theme colors](docs/guide.md#theme-colors) · [Troubleshooting](docs/guide.md#troubleshooting) · [Development and releases](docs/guide.md#development-and-releases)
