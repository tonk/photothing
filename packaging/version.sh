#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Prints the version to build, a semantic version without the leading "v":
#   on a tag v1.2.3            ->  1.2.3
#   3 commits after that tag   ->  1.2.3-3-g0a1b2c3        (a pre-release of the next version)
#   uncommitted changes        ->  ...-dirty
#   no tag at all              ->  0.0.0-dev.g0a1b2c3
# The "g" before a bare hash is what git describe puts there too, and it matters: in a semantic version a
# numeric identifier may not start with a zero, and a hash such as 0508346 is exactly that.
# The VERSION environment variable overrides all of it (VERSION_DESCRIBE stands in for the output of
# git describe, so that the rules above can be tested).
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -n "${VERSION:-}" ]; then
  echo "${VERSION#v}"
  exit 0
fi
d="${VERSION_DESCRIBE-$(git describe --tags --always --dirty 2>/dev/null || true)}"
d="${d#v}"
if [[ "$d" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
  echo "$d"
else
  echo "0.0.0-dev.g${d:-unknown}"
fi
