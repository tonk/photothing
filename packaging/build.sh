#!/usr/bin/env bash
# Builds photothing for Linux and packages it: a tarball, a .deb and an .rpm in dist/.
#
#   packaging/build.sh [amd64|arm64]        (default: the architecture of this machine)
#
# Environment:
#   VERSION     semantic version, with or without the leading "v"; default: see packaging/version.sh
#   COMMIT      commit hash to record in --version; default: git HEAD
#   REPO_URL    project URL shown in the About box (default: the one built into the source)
#   MAINTAINER  "Name <address>" for the package metadata
#   NFPM        path of the nfpm program (default: nfpm from PATH); SKIP_PACKAGES=1 builds the tarball only
set -euo pipefail
cd "$(dirname "$0")/.."

arch="${1:-$(go env GOARCH)}"
case "$arch" in amd64|arm64) ;; *) echo "error: unsupported architecture '$arch' (use amd64 or arm64)" >&2; exit 2 ;; esac

VERSION="$(packaging/version.sh)"
COMMIT="${COMMIT:-$(git rev-parse HEAD 2>/dev/null || true)}"
MAINTAINER="${MAINTAINER:-photothing maintainers <noreply@example.invalid>}"
epoch="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
export VERSION MAINTAINER GOARCH="$arch" SOURCE_DATE_EPOCH="$epoch"

out=dist
build="$out/build"
rm -rf "$build"
mkdir -p "$build/units"

echo ">> photothing $VERSION linux/$arch"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT${REPO_URL:+ -X main.repoURL=$REPO_URL}" -o "$build/photothing" .

# The units in contrib/ are written for a manual install under /usr/local/bin with install instructions
# in the header; for the package, keep only the unit itself and point it at /usr/bin.
for unit in photothing.service photothing@.service; do
  sed -n '/^\[Unit\]/,$p' "contrib/$unit" | sed 's#/usr/local/bin/photothing#/usr/bin/photothing#' > "$build/units/$unit"
done

# Tarball: the binary plus everything needed to install it by hand.
name="photothing_${VERSION}_linux_${arch}"
stage="$build/$name"
mkdir -p "$stage"
cp "$build/photothing" README.md CHANGELOG.md config.example "$stage/"
cp -r contrib "$stage/contrib"
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" -C "$build" -cf - "$name" \
  | gzip -n -9 > "$out/$name.tar.gz"
rm -rf "$stage"

if [ "${SKIP_PACKAGES:-}" != 1 ]; then
  nfpm="${NFPM:-nfpm}"
  command -v "$nfpm" >/dev/null || { echo "error: nfpm not found (set NFPM=/path/to/nfpm)" >&2; exit 1; }
  for fmt in deb rpm; do
    "$nfpm" package --config packaging/nfpm.yaml --packager "$fmt" --target "$out/" >/dev/null
  done
fi
rm -rf "$build"
ls -1 "$out" | grep -E '\.(tar\.gz|deb|rpm)$' | sed "s#^#$out/#"
