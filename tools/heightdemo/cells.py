#!/usr/bin/env python3
"""Helpers for validate-height.sh: cut stdin to N terminal cells per line."""
import sys, unicodedata


def w(ch):
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in "WF" else 1


def cut(line, n):
    out, used = "", 0
    for ch in line:
        if used + w(ch) > n:
            break
        out += ch
        used += w(ch)
    return out + " " * (n - used)


if __name__ == "__main__":
    n = int(sys.argv[1])
    for line in sys.stdin.read().split("\n")[:-1] or []:
        print("|" + cut(line, n) + "|")
