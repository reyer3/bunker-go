#!/usr/bin/env bash
# check-denylist.sh fails when any tracked file contains a denylisted
# string (real names, domains, IDs). The denylist is itself sensitive, so
# it never lives in the repo: CI passes it through the RELEASE_DENYLIST
# secret, and locally it is read from BUNKER_RELEASE_DENYLIST (default
# ~/.config/bunker-go/release-denylist).
#
# Usage: scripts/check-denylist.sh [--cached]
#   --cached  check the staged index instead of the working tree's tracked
#             files (the pre-commit hook uses this).
#
# Matches are reported as file:line only, never the line itself, so a
# public CI log cannot leak what was matched.
set -euo pipefail

mode=()
[ "${1:-}" = "--cached" ] && mode=(--cached)

patterns="$(mktemp)"
trap 'rm -f "$patterns"' EXIT

if [ -n "${RELEASE_DENYLIST:-}" ]; then
	printf '%s\n' "$RELEASE_DENYLIST" > "$patterns"
else
	file="${BUNKER_RELEASE_DENYLIST:-$HOME/.config/bunker-go/release-denylist}"
	if [ ! -s "$file" ]; then
		# Forks and fresh clones have no denylist: warn, never block them.
		echo "check-denylist: no denylist configured, skipping" >&2
		exit 0
	fi
	cp "$file" "$patterns"
fi

# An empty pattern would match every line, so blank lines are dropped.
clean="$(grep -v '^[[:space:]]*$' "$patterns" || true)"
if [ -z "$clean" ]; then
	echo "check-denylist: denylist is empty, skipping" >&2
	exit 0
fi
printf '%s\n' "$clean" > "$patterns"

# LICENSE is excluded: its copyright line must name the author.
hits="$(git grep "${mode[@]}" -n -I -i -F -f "$patterns" -- . ':!LICENSE' | cut -d: -f1,2 || true)"
if [ -n "$hits" ]; then
	echo "check-denylist: denylisted text found at:" >&2
	echo "$hits" >&2
	exit 1
fi
echo "check-denylist: clean"
