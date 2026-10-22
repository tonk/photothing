#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Usage: scripts/third-party-licenses.sh OUTDIR
# Copies the license files of every Go module that is linked into photothing to OUTDIR/<module>/ and
# writes OUTDIR/INDEX.txt (module, version, files). The binary contains this code, so its licenses
# must travel with it. Fails when a module has no license file at all.
set -euo pipefail
cd "$(dirname "$0")/.."

out="${1:?usage: third-party-licenses.sh OUTDIR}"
rm -rf "$out"
mkdir -p "$out"

main="$(go list -m)"
missing=()
: > "$out/INDEX.txt"
while IFS=$'\t' read -r path version dir; do
  [ "$path" = "$main" ] && continue
  files=()
  while IFS= read -r f; do files+=("$f"); done < <(find "$dir" -maxdepth 1 -type f \
    \( -iname 'LICEN[CS]E*' -o -iname 'COPYING*' -o -iname 'NOTICE*' -o -iname 'PATENTS*' \) -printf '%f\n' | sort)
  if [ "${#files[@]}" -eq 0 ]; then
    missing+=("$path@$version")
    continue
  fi
  mkdir -p "$out/$path"
  for f in "${files[@]}"; do cp "$dir/$f" "$out/$path/$f"; done
  printf '%s %s: %s\n' "$path" "$version" "${files[*]}" >> "$out/INDEX.txt"
done < <(go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}{{"\t"}}{{.Module.Version}}{{"\t"}}{{.Module.Dir}}{{end}}{{end}}' . | sort -u)

if [ "${#missing[@]}" -gt 0 ]; then
  echo "error: no license file found for: ${missing[*]}" >&2
  exit 1
fi
echo "$(wc -l < "$out/INDEX.txt") modules" >&2
