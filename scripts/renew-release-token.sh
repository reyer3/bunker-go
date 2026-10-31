#!/usr/bin/env bash
# Renews RELEASE_PLEASE_TOKEN and re-runs the release that failed.
#
# release-please needs a fine-grained token (contents + pull requests write
# on this repo) instead of GITHUB_TOKEN, so the release PR gets CI and its
# tag triggers goreleaser (see .github/workflows/release.yml). When that
# token expires the release job fails with "Bad credentials".
#
# The token is read without echo and piped straight into the secret: it
# never touches disk or shell history, and nobody needs to remember it.
set -euo pipefail

# The token is typed at a hidden prompt, so it needs an interactive terminal.
if [[ ! -t 0 ]]; then
  echo "renew-release-token: run it from an interactive terminal (stdin is not a TTY)" >&2
  exit 1
fi

repo=$(gh repo view --json nameWithOwner -q .nameWithOwner)
owner=${repo%%/*}
days=${1:-366}

# GitHub pre-fills the fine-grained token form from these parameters; check
# the form before generating (repository access: only this repo).
url="https://github.com/settings/personal-access-tokens/new?name=release-please-${repo#*/}&description=release-please+for+${repo}&target_name=${owner}&expires_in=${days}&contents=write&pull_requests=write"

echo "1. Create the token (only ${repo}, Contents + Pull requests: write):"
echo "   ${url}"
command -v xdg-open >/dev/null && xdg-open "$url" >/dev/null 2>&1 || true
command -v open >/dev/null && open "$url" >/dev/null 2>&1 || true

read -rsp "2. Paste the token (hidden): " token
echo
[[ -n "$token" ]] || { echo "renew-release-token: empty token" >&2; exit 1; }

# Fail before touching the secret if the token can't see the repo.
if ! GH_TOKEN="$token" gh api "repos/${repo}" -q .full_name >/dev/null 2>&1; then
  echo "renew-release-token: the token cannot access ${repo}" >&2
  exit 1
fi

printf '%s' "$token" | gh secret set RELEASE_PLEASE_TOKEN --repo "$repo"
unset token
echo "3. RELEASE_PLEASE_TOKEN updated."

run=$(gh run list --repo "$repo" --workflow release.yml --branch main \
  --status failure --limit 1 --json databaseId -q '.[0].databaseId')
if [[ -z "$run" ]]; then
  echo "4. No failed release run on main; nothing to re-run."
  exit 0
fi
gh run rerun "$run" --repo "$repo"
echo "4. Re-running release run ${run}; following it:"
gh run watch "$run" --repo "$repo" --exit-status
