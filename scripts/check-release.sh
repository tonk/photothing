#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Usage: scripts/check-release.sh vX.Y.Z[-prerelease]
# Checks that a release tag is a semantic version (https://semver.org) and that CHANGELOG.md has a
# section for it. Prints the version without the leading "v". Used by the release workflow.
set -euo pipefail

tag="${1:?usage: check-release.sh TAG}"
changelog="${CHANGELOG:-CHANGELOG.md}"

# The regular expression from semver.org, anchored, with the "v" that tags carry.
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(\+[0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*)?$'
if ! [[ "$tag" =~ $semver ]]; then
  echo "error: tag '$tag' is not a semantic version like v1.2.3 or v1.2.3-rc.1" >&2
  exit 1
fi
version="${tag#v}"

# Build metadata ("+...") does not take part in version precedence and cannot be used in package versions.
if [[ "$version" == *+* ]]; then
  echo "error: tag '$tag' has build metadata (+...), which release packages cannot carry" >&2
  exit 1
fi

if ! grep -qF "## [$version] - " "$changelog"; then
  echo "error: $changelog has no section '## [$version] - YYYY-MM-DD'; add it before tagging" >&2
  exit 1
fi
echo "$version"
