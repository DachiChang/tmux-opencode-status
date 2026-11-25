package sidebar

import (
	"fmt"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// ThemeOptionPrefix is the prefix of the tmux global user options for the color palette (plugin-specific, not shared with other options);
// the full option name is the prefix plus the color name, for example `set -g @opencode-status-color-project colour33`.
const ThemeOptionPrefix = "@opencode-status-color-"

// ThemeSource reads the raw color values set by the user: keys are color names (without the prefix), and only configured entries are included.
// A read failure must be reported as an error and must not be treated as everything unset.
type ThemeSource interface {
	Swatches() (map[string]string, error)
}

type unsetTheme struct{}

func (unsetTheme) Swatches() (map[string]string, error) { return nil, nil }

// resolveTheme reads and applies the colors: on success the new theme wins (an invalid entry falls back to the default without affecting the others);
// on a read failure it keeps the last valid theme, or the default theme if no read has ever succeeded. It returns the read error for the caller to record.
func (d *Daemon) resolveTheme() error {
	raw, err := d.deps.Theme.Swatches()
	if err != nil {
		return fmt.Errorf("read theme settings: %w", err)
	}
	d.theme, _ = render.ParseTheme(raw)
	return nil
}
