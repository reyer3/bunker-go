#!/usr/bin/env bash
# release.sh publishes a sanitized snapshot of the private HEAD to the public
# repo. The private history is never published: each release is one fresh
# commit in a separate public clone, tagged with the version.
#
# Usage: scripts/release.sh vX.Y.Z [--push]
#
# The export is guarded before anything is committed, and every guard fails
# closed:
#   - the working tree must be clean, so the snapshot is exactly HEAD;
#   - private paths (odd/, .atl/, .codegraph/) are dropped;
#   - no line may match the denylist (real names, domains, IDs), which lives
#     outside the repo because it is itself sensitive;
#   - betterleaks must report no secrets;
#   - the snapshot must build.
# Without --push the release stays local, so it can be inspected first.
#
# Environment:
#   BUNKER_PUBLIC_DIR        public clone (default ~/projects/bunker-go-public)
#   BUNKER_PUBLIC_REMOTE     cloned when BUNKER_PUBLIC_DIR is missing
#                            (default git@github.com:reyer3/bunker-go.git)
#   BUNKER_RELEASE_DENYLIST  one case-insensitive fixed string per line
#                            (default ~/.config/bunker-go/release-denylist)
#   BUNKER_PUBLIC_AUTHOR     snapshot author (default reyer3 noreply)
set -euo pipefail

PRIVATE_PATHS=(odd .atl .codegraph)

die() { echo "release: $*" >&2; exit 1; }

version="${1:-}"
push=false
[ "${2:-}" = "--push" ] && push=true
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: release.sh vX.Y.Z [--push]"

public_dir="${BUNKER_PUBLIC_DIR:-$HOME/projects/bunker-go-public}"
public_remote="${BUNKER_PUBLIC_REMOTE:-git@github.com:reyer3/bunker-go.git}"
denylist="${BUNKER_RELEASE_DENYLIST:-$HOME/.config/bunker-go/release-denylist}"
author="${BUNKER_PUBLIC_AUTHOR:-reyer3 <56456442+reyer3@users.noreply.github.com>}"

[ -s "$denylist" ] || die "denylist $denylist is missing or empty"
command -v betterleaks >/dev/null || die "betterleaks is not installed"

git diff --quiet HEAD && [ -z "$(git ls-files --others --exclude-standard)" ] ||
	die "working tree is not clean; commit or stash first"

if [ ! -d "$public_dir/.git" ]; then
	git clone -q "$public_remote" "$public_dir"
fi
git -C "$public_dir" diff --quiet HEAD || die "public clone $public_dir has local changes"
if git -C "$public_dir" rev-parse -q --verify "refs/tags/$version" >/dev/null; then
	die "tag $version already exists in $public_dir"
fi

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

git archive HEAD | tar -x -C "$stage"
for p in "${PRIVATE_PATHS[@]}"; do rm -rf "${stage:?}/$p"; done

# grep ignores blank denylist lines only if they are removed first: an empty
# pattern would match every line.
patterns="$(mktemp)"
trap 'rm -rf "$stage" "$patterns"' EXIT
grep -v '^[[:space:]]*$' "$denylist" > "$patterns" || die "denylist has no patterns"
if grep -rInIiF -f "$patterns" "$stage" | sed "s#^$stage/##"; then
	die "denylist match in the export (see above); sanitize the private tree first"
fi

betterleaks dir "$stage" --no-banner --exit-code 1 >/dev/null ||
	die "betterleaks found secrets; run: betterleaks dir . --no-banner"

(cd "$stage" && CGO_ENABLED=0 go build -buildvcs=false -tags goolm ./...) || die "snapshot does not build"

# Replace the public tree with the snapshot, keeping only its .git.
find "$public_dir" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -a "$stage"/. "$public_dir"/

source_rev="$(git rev-parse --short HEAD)"
git -C "$public_dir" add -A
if git -C "$public_dir" diff --cached --quiet; then
	die "nothing changed since the last public release"
fi
name="${author% <*}"
email="${author##*<}"
email="${email%>}"
git -C "$public_dir" -c user.name="$name" -c user.email="$email" \
	commit -q -m "release: $version"
git -C "$public_dir" -c user.name="$name" -c user.email="$email" \
	tag -a "$version" -m "$version"

echo "release: $version committed and tagged in $public_dir (private source $source_rev)"
if $push; then
	branch="$(git -C "$public_dir" symbolic-ref --short HEAD)"
	git -C "$public_dir" push -q origin "$branch" "refs/tags/$version"
	echo "release: pushed $branch and $version"
else
	echo "release: not pushed; inspect $public_dir, then rerun with --push or run:"
	echo "  git -C $public_dir push origin HEAD refs/tags/$version"
fi
