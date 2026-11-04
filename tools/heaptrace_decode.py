#!/usr/bin/env python3
"""Decode the heap-trace lines of the diagnostic firmware (make fw-build-heaptrace).

Each session the firmware logs the internal-RAM blocks that outlived it:
    trace[N] alive addr=0x3fcbeeb0 size=84 by 0x4201... 0x4203...
    trace[N] summary: internal alive=... | allocs=... | overflowed=...
This groups them by who allocated them, with the call stack resolved against
the ELF of that same build. Run it where the ESP-IDF toolchain is on PATH
(the devcontainer):

    python3 tools/heaptrace_decode.py --loki http://<loki>:3100 --minutes 15
    python3 tools/heaptrace_decode.py < device.log
"""
import argparse
import collections
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ALIVE = re.compile(r"trace\[(\d+)\] alive addr=(0x[0-9a-f]+) size=(\d+) by((?: 0x[0-9a-f]+)*)")
SUMMARY = re.compile(r"trace\[(\d+)\] summary: internal alive=(\d+).*")
LOKI_LIMIT = 5000
# Allocator and wrapper frames: the owner is the first frame past these.
WRAPPERS = re.compile(
    r"^(heap_caps_|multi_heap|tlsf_|malloc|calloc|realloc|_malloc_r|_calloc_r|_realloc_r|"
    r"pvPortMalloc|mem_malloc|mem_calloc|memp_malloc|mbedtls_calloc|media_lib_module_calloc|"
    r"os_malloc|os_zalloc|os_calloc)"
)


def loki_lines(url, minutes):
    params = urllib.parse.urlencode({
        "query": '{job="device-syslog"} |~ "trace\\\\[[0-9]+\\\\] (alive|summary|INCOMPLETE)"',
        "start": int((time.time() - minutes * 60) * 1e9),
        "limit": LOKI_LIMIT,
        "direction": "forward",
    })
    try:
        with urllib.request.urlopen(f"{url}/loki/api/v1/query_range?{params}", timeout=30) as r:
            data = json.load(r)
    except (urllib.error.URLError, json.JSONDecodeError) as e:
        sys.exit(f"Loki query to {url} failed: {e}")
    rows = sorted((int(ts), line) for s in data["data"]["result"] for ts, line in s["values"])
    if len(rows) >= LOKI_LIMIT:
        print(f"warning: Loki returned the {LOKI_LIMIT}-line limit, older windows may be cut; narrow --minutes",
              file=sys.stderr)
    return [line for _, line in rows]


def symbolize(elf, addr2line, pcs):
    if not os.path.exists(elf):
        sys.exit(f"ELF not found: {elf} (it must be the one from the build that produced the log)")
    # Return addresses point past the call; -3 lands on the call instruction.
    try:
        out = subprocess.run(
            [addr2line, "-a", "-f", "-C", "-e", elf, *(hex(int(p, 16) - 3) for p in pcs)],
            capture_output=True, text=True, check=True,
        ).stdout.splitlines()
    except FileNotFoundError:
        sys.exit(f"{addr2line} not found: run this inside the devcontainer or pass --addr2line")
    except subprocess.CalledProcessError as e:
        sys.exit(f"{addr2line} failed: {e.stderr.strip()}")
    if len(out) != 3 * len(pcs):
        sys.exit(f"{addr2line} returned {len(out)} lines for {len(pcs)} addresses")
    names = {}
    for i in range(0, len(out) - 2, 3):
        addr = hex(int(out[i], 16) + 3)
        loc = re.sub(r"^.*/(components|managed_components|main)/", r"\1/", out[i + 2])
        names[addr] = (out[i + 1], loc)
    return names


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--elf", default=os.path.join(os.path.dirname(__file__), "../firmware/build-heaptrace/sebastian.elf"))
    ap.add_argument("--addr2line", default=os.environ.get("ADDR2LINE", "xtensa-esp32s3-elf-addr2line"))
    ap.add_argument("--loki", help="Loki base URL; without it, log lines are read from stdin")
    ap.add_argument("--minutes", type=int, default=15)
    ap.add_argument("--window", type=int, help="only this session window")
    ap.add_argument("--frames", type=int, default=5, help="owner frames to print per group")
    args = ap.parse_args()

    lines = loki_lines(args.loki, args.minutes) if args.loki else sys.stdin.read().splitlines()
    records, summaries, expected = [], [], collections.Counter()
    for line in lines:
        if m := ALIVE.search(line):
            window = int(m.group(1))
            if args.window is None or window == args.window:
                records.append((window, int(m.group(2), 16), int(m.group(3)), m.group(4).split()))
        elif m := SUMMARY.search(line):
            summaries.append(m.group(0))
            expected[int(m.group(1))] += int(m.group(2))
        elif "INCOMPLETE" in line:
            summaries.append(line[line.index("trace["):])
    if not records and not summaries:
        sys.exit("no trace lines found (is the device running the fw-build-heaptrace firmware?)")

    got = collections.Counter(r[0] for r in records)
    for window, n in sorted(expected.items()):
        if (args.window is None or window == args.window) and got[window] != n:
            print(f"warning: window {window} lists {got[window]} blocks but its summary says {n}"
                  " (lost syslog lines, or several boots in the time range)", file=sys.stderr)

    pcs = sorted({pc for r in records for pc in r[3]})
    names = symbolize(args.elf, args.addr2line, pcs) if pcs else {}
    unresolved = sum(1 for v in names.values() if v[0] == "??")
    if names and unresolved * 2 > len(names):
        print(f"warning: {unresolved}/{len(names)} addresses unresolved; is --elf from this same build?",
              file=sys.stderr)

    groups = collections.defaultdict(list)
    for window, addr, size, bt in records:
        frames = [names.get(hex(int(p, 16)), ("?", "?")) for p in bt]
        owner = [f for f in frames if not WRAPPERS.match(f[0])] or frames
        groups[(window, tuple(f[0] for f in owner[:4]))].append((addr, size, owner))

    for s in summaries:
        print(s)
    for (window, _), items in sorted(groups.items(), key=lambda kv: (kv[0][0], -sum(i[1] for i in kv[1]))):
        sizes = dict(collections.Counter(i[1] for i in items))
        print(f"\nwindow {window}: {len(items)} blocks, {sum(i[1] for i in items)} B, sizes={sizes}")
        for fn, loc in items[0][2][: args.frames]:
            print(f"    {fn}  ({loc})")
        print("    at", " ".join(hex(i[0]) for i in sorted(items)[:6]))


if __name__ == "__main__":
    main()
