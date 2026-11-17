#!/usr/bin/env python3
"""唯讀探針：列出指定 tmux socket 內每個 pane 的 TUI 辨識事實（JSON 輸出）。

用法：probe_panes.py <tmux-socket-name> [路徑前綴=替換字串 ...]

只讀 tmux（list-panes）、ps（tty 前景行程組）、lsof（cwd）與 sysctl KERN_PROCARGS2（精確 argv）。
前景行程 = 該 tty 上 pgid == tpgid 的行程。不寫入任何東西，也不連線 OpenCode。
"""
import ctypes, ctypes.util, json, subprocess, sys

sock = sys.argv[1]
subs = [a.split('=', 1) for a in sys.argv[2:]]
libc = ctypes.CDLL(ctypes.util.find_library('c'))


def redact(s):
    for a, b in subs:
        s = s.replace(a, b)
    return s


def argv_of(pid):
    mib = (ctypes.c_int * 3)(1, 49, int(pid))  # CTL_KERN, KERN_PROCARGS2, pid
    size = ctypes.c_size_t(0)
    if libc.sysctl(mib, 3, None, ctypes.byref(size), None, 0) != 0:
        return None
    buf = ctypes.create_string_buffer(size.value)
    if libc.sysctl(mib, 3, buf, ctypes.byref(size), None, 0) != 0:
        return None
    raw = buf.raw[:size.value]
    argc = int.from_bytes(raw[:4], 'little')
    parts = raw[4:].split(b'\0')
    i = 1
    while parts[i] == b'':
        i += 1
    return [a.decode(errors='replace') for a in parts[i:i + argc]]


def cwd_of(pid):
    out = subprocess.run(['lsof', '-a', '-p', str(pid), '-d', 'cwd', '-Fn'], capture_output=True, text=True).stdout
    for l in out.splitlines():
        if l.startswith('n'):
            return l[1:]


fmt = '\t'.join(['#{window_name}', '#{pane_current_command}', '#{alternate_on}', '#{pane_tty}', '#{pane_current_path}'])
rows = subprocess.run(['tmux', '-L', sock, 'list-panes', '-a', '-F', fmt], capture_output=True, text=True, check=True).stdout.splitlines()
result = []
for row in rows:
    name, cmd, alt, tty, path = row.split('\t')
    ps = subprocess.run(['ps', '-t', tty.replace('/dev/', ''), '-o', 'pid=,pgid=,tpgid='], capture_output=True, text=True).stdout.splitlines()
    fg = [p.split()[0] for p in ps if len(p.split()) == 3 and p.split()[1] == p.split()[2]]
    pid = fg[0] if fg else None
    result.append({
        'name': name,
        'paneCurrentCommand': cmd,
        'alternateOn': alt == '1',
        'paneCurrentPath': redact(path),
        'foregroundArgv': [redact(a) for a in (argv_of(pid) or [])] if pid else [],
        'foregroundCwd': redact(cwd_of(pid) or '') if pid else '',
    })
json.dump(result, sys.stdout, indent=1, ensure_ascii=False)
print()
