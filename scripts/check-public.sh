#!/usr/bin/env bash
# Fails when tracked or untracked (non-ignored) files contain data that must not reach the public repository.
# With --cached it checks only the staged content, which is what a commit records.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
scope=--untracked
[ "${1:-}" = "--cached" ] && scope=--cached

# Applied to each matched token, so an allowed value can't hide a real one on the same line.
allow='^(111122223333|111111111111|noreply@anthropic[.]com)$'
patterns=(
  # Whole digit runs; awk keeps the 12-digit ones. A boundary in the pattern would swallow the
  # separator between two adjacent ids.
  '[0-9]{12,}'
  '/Users/[A-Za-z]|/home/[A-Za-z]'
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z.]{2,}'
  '(AKIA|ASIA)[0-9A-Z]{16}'
)
found=0
for p in "${patterns[@]}"; do
  rc=0
  # -z separates path, line and match with NUL; git grep exits 1 for no match and >1 on error.
  matches=$(git grep "$scope" -z -noIE "$p" -- . ':!**/package-lock.json' ':!go.sum' | tr '\0' '\t'; exit "${PIPESTATUS[0]}") || rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "check-public: git grep failed with exit code $rc for pattern $p" >&2
    exit 2
  fi
  if [ -n "$matches" ] && printf '%s\n' "$matches" | awk -v allow="$allow" '
    { i = index($0, "\t"); rest = substr($0, i + 1); j = index(rest, "\t"); m = substr(rest, j + 1) }
    m ~ /^[0-9]+$/ && length(m) != 12 { next }
    m !~ allow { print substr($0, 1, i - 1) ":" substr(rest, 1, j - 1) ": " m; bad = 1 }
    END { exit !bad }'; then
    found=1
  fi
done
if [ "$found" -eq 1 ]; then
  echo "check-public: remove the matches above before committing" >&2
  exit 1
fi
echo "check-public: ok"
