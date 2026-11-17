package heightfit

import (
	"sort"
	"strconv"
	"strings"
)

// Published is the global state shared by all clients: one tmux user option per
// distinct layout, plus a side-status-format that picks one per client from
// #{client_height}. Clients never share a cut; each selects its own.
type Published struct {
	// Options maps option name (@tos_sb_<minAvail>) to the layout used from that
	// available height upward, until the next threshold.
	Options map[string]string
	min     []int // ascending thresholds
}

// OptPrefix is the tmux user option prefix owned by this plugin; it must never
// collide with a user's own @options (the POC used @oc_side / @sb_*).
const OptPrefix = "@tos_sb_"

// MinAvail is the smallest side-status height we lay out (title+rule+footer).
const MinAvail = 3

func Publish(groups []Group, width int) Published {
	return PublishView(View{Groups: groups}, width)
}

// PublishView is Publish for a full View.
func PublishView(v View, width int) Published {
	full, _ := FitView(v, 1<<20)
	fullAvail := max(len(full), MinAvail)
	p := Published{Options: map[string]string{}}
	prev := ""
	for avail := MinAvail; avail <= fullAvail; avail++ {
		rows, _ := FitView(v, avail)
		cur := strings.Join(Render(rows, width), "\n")
		if cur == prev {
			continue
		}
		p.Options[OptPrefix+strconv.Itoa(avail)] = cur
		p.min = append(p.min, avail)
		prev = cur
	}
	return p
}

// Select returns the layout a client with avail side-status rows displays.
func (p Published) Select(avail int) string {
	best := p.min[0]
	for _, m := range p.min {
		if m <= avail {
			best = m
		}
	}
	return p.Options[OptPrefix+strconv.Itoa(best)]
}

// Format builds side-status-format. Option values are read with #{@name} (one
// expansion), so '#' escaping done by Render is the only layer.
func (p Published) Format() string {
	const avail = "#{e|-:#{client_height},#{?#{==:#{status},off},0,#{?#{==:#{status},on},1,#{status}}}}"
	ms := append([]int(nil), p.min...)
	sort.Sort(sort.Reverse(sort.IntSlice(ms)))
	var b strings.Builder
	b.WriteString("#{?")
	for i, m := range ms {
		if i == len(ms)-1 {
			b.WriteString("#{" + OptPrefix + strconv.Itoa(m) + "}}")
			break
		}
		b.WriteString("#{e|>=:" + avail + "," + strconv.Itoa(m) + "},#{" + OptPrefix + strconv.Itoa(m) + "},")
	}
	if len(ms) == 1 {
		return "#{" + OptPrefix + strconv.Itoa(ms[0]) + "}"
	}
	return b.String()
}
