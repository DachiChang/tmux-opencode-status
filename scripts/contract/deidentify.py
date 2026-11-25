#!/usr/bin/env python3
"""De-identify sandbox recordings for use as test fixtures.

Usage: deidentify.py <scenario.json> <scenario.sse> <bootstrap.json> <output-dir>

- Replace session, permission, form, event, and inbox IDs with stable synthetic
  IDs. Use role names for named sessions (for example, ses_R).
- Replace directories with /work/demo and project IDs with proj_demo.
- Replace titles with tab-<role>, form-title, or field. Replace free text such
  as inbox payload text and permission resources with <redacted>.
- Rebase timestamps so the earliest is 1_000_000, preserving relative intervals.
- Scan keys and strings for remaining IDs. Unmapped session IDs matching the
  session ID pattern become ses_UNMAPPED, which fixture tests reject.

Do not commit raw recordings containing real IDs. See the recording workflow
in docs/contracts/opencode-2.4.1.md.
"""
import json, os, re, sys

scn = json.load(open(sys.argv[1]))
boot = json.load(open(sys.argv[3]))
sse_lines = open(sys.argv[2]).read().splitlines()
out = sys.argv[4]
os.makedirs(out, exist_ok=True)

role = {v: k for k, v in scn['roots'].items()}
role.update({v: k for k, v in boot['roots'].items()})
idmap = {}
counters = {}
TIME_KEYS = ('created', 'updated', 'busy', 'idle', 'viewed', 'archived', 'firstToken', 'streamed', 'completed')
DIR = '/work/demo'


def sid(real):
    if real in idmap:
        return idmap[real]
    if real in role:
        new = 'ses_%s' % role[real]
    else:
        counters['ses'] = counters.get('ses', 0) + 1
        new = 'ses_X%d' % counters['ses']
    idmap[real] = new
    return new


def gen(prefix, real):
    if real not in idmap:
        counters[prefix] = counters.get(prefix, 0) + 1
        idmap[real] = '%s_%d' % (prefix, counters[prefix])
    return idmap[real]


events = [json.loads(l[6:]) for l in sse_lines if l.startswith('data: ')]
times = []


def collect(o):
    if isinstance(o, dict):
        for k, v in o.items():
            if k in TIME_KEYS and isinstance(v, (int, float)) and v > 1e12:
                times.append(v)
            collect(v)
    elif isinstance(o, list):
        for x in o:
            collect(x)


collect(scn)
collect(boot)
collect(events)
base = min(times)


def t(v):
    return int(v - base) + 1_000_000


def clean(o):
    if isinstance(o, dict):
        r = {}
        for k, v in o.items():
            if k in ('sessionID', 'parentID') and isinstance(v, str) and v.startswith('ses'):
                r[k] = sid(v)
            elif k == 'id' and isinstance(v, str) and v.startswith('ses'):
                r[k] = sid(v)
            elif k == 'id' and isinstance(v, str) and v.startswith('per_'):
                r[k] = gen('per', v)
            elif k == 'requestID':
                r[k] = gen('per', v)
            elif k == 'id' and isinstance(v, str) and v.startswith('frm_'):
                r[k] = gen('frm', v)
            elif k == 'id' and isinstance(v, str) and v.startswith('evt_'):
                r[k] = gen('evt', v)
            elif k == 'inboxID':
                r[k] = gen('msg', v)
            elif k == 'projectID':
                r[k] = 'proj_demo'
            elif k == 'directory':
                r[k] = DIR
            elif k in TIME_KEYS and isinstance(v, (int, float)):
                r[k] = t(v)
            elif k == 'aggregateID' and isinstance(v, str):
                r[k] = sid(v) if v.startswith('ses') else v
            elif k == 'text' and isinstance(v, str):
                r[k] = '<redacted>'
            elif k == 'slug':
                r[k] = 'slug'
            elif k == 'cursor' and isinstance(v, dict):
                r[k] = {kk: (None if vv is None else '<cursor>') for kk, vv in v.items()}
            else:
                r[k] = clean(v)
        return r
    if isinstance(o, list):
        return [clean(x) for x in o]
    return o


def final(o):
    """Replace remaining real IDs in keys and strings using the ID map."""
    def sub(text):
        for real in sorted(idmap, key=len, reverse=True):
            text = text.replace(real, idmap[real])
        return re.sub(r'ses_[A-Za-z0-9]{20,}', 'ses_UNMAPPED', text)
    if isinstance(o, dict):
        return {sub(k): final(v) for k, v in o.items()}
    if isinstance(o, list):
        return [final(x) for x in o]
    if isinstance(o, str):
        return sub(o)
    return o


def scrub(o):
    if isinstance(o, dict):
        if 'fields' in o and 'sessionID' in o and 'title' in o:
            o['title'] = 'form-title'
        if isinstance(o.get('form'), dict) and 'fields' in o['form']:
            o['form']['title'] = 'form-title'
        if 'title' in o and isinstance(o.get('id'), str) and o['id'].startswith('ses_'):
            o['title'] = 'tab-' + o['id'][4:]
        if 'title' in o and 'sessionID' in o and 'fields' not in o and isinstance(o['sessionID'], str):
            o['title'] = 'tab-' + o['sessionID'][4:]
        if isinstance(o.get('fields'), list):
            for f in o['fields']:
                f['title'] = 'field'
        if 'resources' in o:
            o['resources'] = ['<redacted>']
        if 'metadata' in o and isinstance(o['metadata'], dict):
            o['metadata'] = {}
        for v in o.values():
            scrub(v)
    elif isinstance(o, list):
        for x in o:
            scrub(x)


# Assign synthetic IDs to named sessions first so keys and values agree.
for real in list(scn['roots'].values()) + list(boot['roots'].values()):
    sid(real)

# --- Scenario snapshots ---
snaps = clean(scn['snapshots'])
scrub(snaps)
snaps = final(snaps)
deleted = final(clean(scn['deleted_get']['body']))
json.dump({'directory': DIR, 'roots': {k: sid(v) for k, v in scn['roots'].items()}, 'snapshots': snaps,
           'deleted_get': {'status': scn['deleted_get']['status'], 'body': deleted}},
          open(os.path.join(out, 'snapshots.json'), 'w'), indent=1, ensure_ascii=False)

# --- SSE events (preserve heartbeats and order) ---
evs = clean(events)
scrub(evs)
evs = final(evs)
it = iter(evs)
sse_out = []
for l in sse_lines:
    if l.startswith('data: '):
        sse_out += ['data: ' + json.dumps(next(it), separators=(',', ':'), ensure_ascii=False), '']
    elif l.startswith(':'):
        sse_out += [l, '']
open(os.path.join(out, 'events.sse'), 'w').write('\n'.join(sse_out) + '\n')

# --- Bootstrap discovery data ---
bclean = clean({k: v for k, v in boot.items() if k not in ('dir', 'roots')})
scrub(bclean)
bclean = final(bclean)
bclean['directory'] = DIR
bclean['roots'] = {k: sid(v) for k, v in boot['roots'].items()}
json.dump(bclean, open(os.path.join(out, 'bootstrap.json'), 'w'), indent=1, ensure_ascii=False)

print('ok', len(snaps), 'snapshots', len(evs), 'events')
