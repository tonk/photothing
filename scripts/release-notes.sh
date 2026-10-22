#!/usr/bin/env bash
# Usage: scripts/release-notes.sh X.Y.Z
# Prints the CHANGELOG.md section of one version (without its heading): the release notes.
set -euo pipefail

version="${1:?usage: release-notes.sh VERSION}"
changelog="${CHANGELOG:-CHANGELOG.md}"

notes="$(awk -v v="$version" '
  index($0, "## [" v "] - ") == 1 { on = 1; next }
  on && /^## \[/                  { exit }
  on                              { print }
' "$changelog")"

# Trim blank lines at both ends.
notes="$(printf '%s\n' "$notes" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')"
if [ -z "$notes" ]; then
  echo "error: no release notes for $version in $changelog" >&2
  exit 1
fi
printf '%s\n' "$notes"
