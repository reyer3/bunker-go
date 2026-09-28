#!/usr/bin/env bash
# Tests for scripts/release.sh against throwaway git repos. Nothing here
# touches the network, the real public clone or the real denylist.
set -euo pipefail

SCRIPT="$(cd "$(dirname "$0")" && pwd)/release.sh"
# Tests run in subshells, so failures are counted in a file.
FAILS="$(mktemp)"

fail() { echo "FAIL: $*"; echo x >> "$FAILS"; }
pass() { echo "ok:   $*"; }

# setup builds a private repo, a bare "GitHub" remote, a public clone of it,
# a denylist and a fake betterleaks, all under a fresh temp dir.
setup() {
	T="$(mktemp -d)"
	export HOME="$T/home" GIT_CONFIG_GLOBAL="$T/gitconfig"
	mkdir -p "$HOME"
	git config --global user.name test
	git config --global user.email test@example.com
	git config --global init.defaultBranch main

	git init -q --bare "$T/remote.git"
	git clone -q "$T/remote.git" "$T/public" 2>/dev/null
	git -C "$T/public" commit -q --allow-empty -m "initial"
	git -C "$T/public" push -q origin main

	mkdir -p "$T/private/odd" "$T/private/.atl" "$T/private/.engram"
	cd "$T/private"
	git init -q
	printf 'module example.com/demo\n\ngo 1.22\n' > go.mod
	printf 'package main\n\nfunc main() {}\n' > main.go
	echo "private notes about Zorblax Quux" > odd/notes.md
	echo "runtime state" > .atl/state.md
	echo '{"project_name": "demo"}' > .engram/config.json
	git add -A && git commit -q -m "private"

	echo "zorblax quux" > "$T/denylist"

	mkdir -p "$T/bin"
	printf '#!/bin/sh\nexit 0\n' > "$T/bin/betterleaks"
	chmod +x "$T/bin/betterleaks"
	export PATH="$T/bin:$PATH"

	export BUNKER_PUBLIC_DIR="$T/public" BUNKER_RELEASE_DENYLIST="$T/denylist"
}

public_head() { git -C "$T/public" rev-parse HEAD; }

test_clean_release_commits_and_tags_without_push() {
	setup
	before="$(public_head)"
	if ! "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "clean release exited non-zero"; return; fi
	[ "$(public_head)" != "$before" ] || fail "no snapshot commit in public clone"
	git -C "$T/public" rev-parse -q --verify refs/tags/v0.2.0 >/dev/null || fail "tag v0.2.0 missing"
	[ -f "$T/public/main.go" ] || fail "main.go not exported"
	[ ! -e "$T/public/odd" ] || fail "odd/ leaked into public"
	[ ! -e "$T/public/.atl" ] || fail ".atl/ leaked into public"
	[ ! -e "$T/public/.engram" ] || fail ".engram/ leaked into public"
	git -C "$T/remote.git" rev-parse -q --verify refs/tags/v0.2.0 >/dev/null && fail "pushed without --push"
	pass "clean release commits and tags without pushing"
}

test_denylist_hit_aborts_before_commit() {
	setup
	echo "// maintained by ZORBLAX QUUX" >> main.go
	git commit -qam "leak"
	before="$(public_head)"
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "denylist hit did not abort"; return; fi
	[ "$(public_head)" = "$before" ] || fail "public clone changed after denylist hit"
	git -C "$T/public" rev-parse -q --verify refs/tags/v0.2.0 >/dev/null && fail "tag created after denylist hit"
	pass "denylist hit aborts before commit (case-insensitive)"
}

test_denylist_ignores_license_copyright() {
	setup
	echo "Copyright 2026 Zorblax Quux" > LICENSE
	git add LICENSE && git commit -qm "license"
	if ! "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "denylisted name in LICENSE aborted the release"; return; fi
	[ -f "$T/public/LICENSE" ] || fail "LICENSE not exported"
	pass "denylist ignores the LICENSE copyright line"
}

test_denylist_hit_in_comment_still_aborts_with_license_present() {
	setup
	echo "Copyright 2026 Zorblax Quux" > LICENSE
	echo "// what zorblax sent" >> main.go
	echo "zorblax" >> "$T/denylist"
	git add -A && git commit -qm "leak in comment"
	before="$(public_head)"
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "bare name in a comment did not abort"; return; fi
	[ "$(public_head)" = "$before" ] || fail "public clone changed after comment hit"
	pass "bare denylisted name in code aborts even with LICENSE excluded"
}

test_betterleaks_finding_aborts() {
	setup
	printf '#!/bin/sh\nexit 1\n' > "$T/bin/betterleaks"
	before="$(public_head)"
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "betterleaks finding did not abort"; return; fi
	[ "$(public_head)" = "$before" ] || fail "public clone changed after betterleaks finding"
	pass "betterleaks finding aborts"
}

test_missing_denylist_fails_closed() {
	setup
	rm "$T/denylist"
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "missing denylist did not abort"; return; fi
	pass "missing denylist fails closed"
}

test_dirty_tree_aborts() {
	setup
	echo "uncommitted" >> main.go
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "dirty tree did not abort"; return; fi
	pass "dirty tree aborts"
}

test_bad_version_aborts() {
	setup
	if "$SCRIPT" 0.2 >/dev/null 2>&1; then fail "bad version did not abort"; return; fi
	pass "bad version aborts"
}

test_existing_tag_aborts() {
	setup
	"$SCRIPT" v0.2.0 >/dev/null 2>&1 || { fail "first release failed"; return; }
	if "$SCRIPT" v0.2.0 >/dev/null 2>&1; then fail "re-release of existing tag did not abort"; return; fi
	pass "existing tag aborts"
}

test_push_publishes_branch_and_tag() {
	setup
	if ! "$SCRIPT" v0.2.0 --push >/dev/null 2>&1; then fail "release --push exited non-zero"; return; fi
	git -C "$T/remote.git" rev-parse -q --verify refs/tags/v0.2.0 >/dev/null || fail "tag not pushed"
	[ "$(git -C "$T/remote.git" rev-parse main)" = "$(public_head)" ] || fail "main not pushed"
	pass "--push publishes branch and tag"
}

for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
	(set -e; $t) || fail "$t crashed"
done

fails="$(wc -l < "$FAILS")"
rm -f "$FAILS"
if [ "$fails" -gt 0 ]; then
	echo "$fails failure(s)"
	exit 1
fi
echo "all release tests passed"
