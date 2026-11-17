// heightdemo publishes a controlled tab list to an isolated tmux server
// (-L socket only) so docs/validation/height.md can be reproduced. It is a
// validation tool (issue #3, kept in sync by #10), not the production sidebar:
// it renders through the same internal/render code and @tos_sb_ options the
// daemon uses, so what it shows is what the daemon would publish.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

var scenarios = map[string][]render.Group{
	"base": {
		{Name: "aicova/api", Tabs: []render.Tab{
			{Title: "修正登入逻辑 bug", State: render.Running},
			{Title: "PR #42 #[fg=red]x #{pane_id} #(touch /tmp/tos-h3-injected) ,}", State: render.Attention},
			{Title: "寫單元測試", State: render.Idle},
		}},
		{Name: "infra", Tabs: []render.Tab{
			{Title: "terraform plan failed 這是一個非常長的標題需要截斷", State: render.Failed},
			{Title: "k8s upgrade", State: render.Unread},
		}},
		{Name: "profcoo/api", Tabs: []render.Tab{
			{Title: "migrate db", State: render.Unknown},
			{Title: "docs", State: render.Running},
			{Title: "refactor", State: render.Idle},
		}},
	},
	"many": many(),
	"shrunk": {
		{Name: "aicova/api", Tabs: []render.Tab{{Title: "修正登入逻辑 bug", State: render.Running}}},
	},
	"grown": {
		{Name: "aicova/api", Tabs: []render.Tab{
			{Title: "t1", State: render.Running}, {Title: "t2", State: render.Running},
			{Title: "t3", State: render.Attention}, {Title: "t4", State: render.Idle},
			{Title: "t5", State: render.Idle}, {Title: "t6", State: render.Unread},
		}},
		{Name: "infra", Tabs: []render.Tab{{Title: "x1", State: render.Failed}, {Title: "x2", State: render.Idle}}},
	},
}

func many() []render.Group {
	var gs []render.Group
	for g := 0; g < 12; g++ {
		grp := render.Group{Name: fmt.Sprintf("proj%02d/api", g)}
		for i := 0; i < 8; i++ {
			grp.Tabs = append(grp.Tabs, render.Tab{Title: fmt.Sprintf("g%d tab %d 一些標題", g, i), State: render.State((g + i) % 6)})
		}
		gs = append(gs, grp)
	}
	return gs
}

func main() {
	sock := flag.String("socket", "", "isolated tmux socket name (-L); required")
	scen := flag.String("scenario", "base", "base|shrunk|grown")
	width := flag.Int("width", 36, "side-status width in cells")
	flag.Parse()
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "-socket is required (never touches the default server)")
		os.Exit(2)
	}
	groups, ok := scenarios[*scen]
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown scenario")
		os.Exit(2)
	}
	p := render.Publish(groups, *width-1) // one column goes to the divider line

	// One `source-file -` = one command queue: every option changes before the
	// next redraw, so no client ever sees a half-applied state. Passing the
	// options as argv instead fails with "command too long" (~16 KB imsg limit).
	var script strings.Builder
	script.WriteString("set-option -g side-status left\nset-option -g side-status-width " + fmt.Sprint(*width) + "\n")
	var names []string
	for n := range p.Options {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		script.WriteString("set-option -g " + n + " " + quote(p.Options[n]) + "\n")
	}
	script.WriteString("set-option -g side-status-format " + quote(p.Format()) + "\n")
	cmd := exec.Command("tmux", "-L", *sock, "-f", "/dev/null", "source-file", "-")
	cmd.Stdin = strings.NewReader(script.String())
	if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
		fmt.Fprintf(os.Stderr, "tmux: %v: %s\n", err, strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	bytes := 0
	for _, v := range p.Options {
		bytes += len(v)
	}
	fmt.Printf("published scenario=%s layouts=%d option_bytes=%d format_bytes=%d\n", *scen, len(p.Options), bytes, len(p.Format()))
}

// quote renders v as a tmux config double-quoted string.
func quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}
