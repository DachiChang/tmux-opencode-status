#!/usr/bin/env bash
# Reproduce docs/validation/height.md. Uses only isolated tmux servers
# (-L <socket> -f /dev/null); the user's default tmux server and config are
# never touched. "Clients" are real tmux clients attached from panes of an
# outer isolated server, so each has the pane size we give it.
set -u
cd "$(dirname "$0")/.."
IN=tos-h3-in-$$ ; OUT=tos-h3-out-$$
I(){ tmux -L "$IN" -f /dev/null "$@"; }
O(){ tmux -L "$OUT" -f /dev/null "$@"; }
trap 'I kill-server 2>/dev/null; O kill-server 2>/dev/null' EXIT
DEMO=$(mktemp -d)/heightfit-demo
go build -o "$DEMO" ./cmd/heightfit-demo || exit 1
pub(){ "$DEMO" -socket "$IN" -scenario "$1"; }
show(){ # name
  echo "### client $1 ($(O display -p -t "$1" '#{pane_width}x#{pane_height}'))"
  O capture-pane -p -t "$1" | python3 scripts/cells.py 36
}
step(){ echo; echo "## $*"; }

echo "tmux: $(tmux -V)  go: $(go version | cut -d' ' -f3)  $(uname -sm)"
I new-session -d -s main -x 120 -y 40
O new-session -d -s hold -x 200 -y 50
BIND_BEFORE=$(I list-keys | md5)
pub base >/dev/null
for spec in "c8 70 8" "c12 70 12" "c16 70 16" "c24 70 24"; do
  set -- $spec
  O new-session -d -s "$1" -x "$2" -y "$3" "tmux -L $IN -f /dev/null attach -t main"
done
sleep 1.2

step "A. one server, four clients of different heights, same global state"
I list-clients -F '#{client_tty} #{client_width}x#{client_height}'
for c in c8 c12 c16 c24; do show $c; done

step "B. resize one client (c12 -> 24 rows, c24 -> 9 rows); others untouched"
O resize-window -t c12 -y 24 ; O resize-window -t c24 -y 9 ; sleep 1
for c in c8 c12 c16 c24; do show $c; done

step "C. new global state (scenario=shrunk: 1 tab) published once; all clients follow"
pub shrunk ; sleep 1
for c in c8 c16; do show $c; done

step "D. state grows again (scenario=grown), c8 resized to 14"
pub grown; O resize-window -t c8 -y 14 ; sleep 1
for c in c8 c16 c24; do show $c; done

step "D2. cut falls inside the second group (c8 -> 13 rows = 12 side rows)"
O resize-window -t c8 -y 13 ; sleep 1; show c8

step "E. hostile titles are drawn literally (see c24 in scenario base) and nothing ran"
pub base; sleep 1
rm -f /tmp/tos-h3-injected
show c24
test -e /tmp/tos-h3-injected && echo "INJECTED!" || echo "no injected side effect"

step "G. status lines change: available rows = client_height - status lines (c8 at 10 rows)"
O resize-window -t c8 -y 10 ; sleep 1
echo "-- status on (1 line): 9 side rows"; show c8
I set -t main status 3; sleep 1
echo "-- status 3: 7 side rows"; show c8
I set -t main status off; sleep 1
echo "-- status off: 10 side rows"; show c8
I set -t main status on

step "H. latency from publish to visible change (c16), 96 tabs / 12 groups"
python3 - "$DEMO" "$IN" "$OUT" <<'PY'
import subprocess, sys, time
demo, i, o = sys.argv[1:]
cap = lambda: subprocess.run(["tmux","-L",o,"-f","/dev/null","capture-pane","-p","-t","c16"],capture_output=True,text=True).stdout
before = cap()
t0 = time.time()
print(subprocess.run([demo,"-socket",i,"-scenario","many"],capture_output=True,text=True).stdout.strip())
while cap() == before and time.time()-t0 < 5: time.sleep(0.02)
print("visible after %.0f ms" % ((time.time()-t0)*1000))
PY
show c16
show c8

step "F. no key/mouse bindings added"
BIND_AFTER=$(I list-keys | md5)
[ "$BIND_BEFORE" = "$BIND_AFTER" ] && echo "list-keys identical before/after publish" || echo "BINDINGS CHANGED"
echo "extra panes in server: $(I list-panes -a | wc -l | tr -d ' ') (1 expected); control clients: $(I list-clients -F '#{client_control_mode}' | grep -c 1)"
