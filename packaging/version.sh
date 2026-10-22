#!/usr/bin/env bash
# Prints the version to build, a semantic version without the leading "v":
#   on a tag v1.2.3            ->  1.2.3
#   3 commits after that tag   ->  1.2.3-3-g0a1b2c3        (a pre-release of the next version)
#   uncommitted changes        ->  ...-dirty
#   no tag at all              ->  0.0.0-dev.0a1b2c3
# The VERSION environment variable overrides all of it.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -n "${VERSION:-}" ]; then
  echo "${VERSION#v}"
  exit 0
fi
d="$(git describe --tags --always --dirty 2>/dev/null || true)"
d="${d#v}"
if [[ "$d" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
  echo "$d"
else
  echo "0.0.0-dev.${d:-unknown}"
fi
