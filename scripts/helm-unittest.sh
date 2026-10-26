#!/usr/bin/env bash
set -euo pipefail

# Install the checksum-pinned upstream plugin in a temporary plugin directory.
# Never run mutable install hooks or change the developer's Helm plugins.
version=1.2.1
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64; digest=48e86ecc1467b009630e5954375e5d8494139f73b466df5fe8c04fa45b634939 ;;
  Linux-aarch64|Linux-arm64) platform=linux-arm64; digest=c66062713cd3cbb1a80d7098bd1d4a48db8438f56b43fb6377df65447f93f8de ;;
  Darwin-x86_64) platform=macos-amd64; digest=3e57daace5249d8dafd5fd2f6fbebf773c7e53a56e904db1f903031866853158 ;;
  Darwin-arm64) platform=macos-arm64; digest=38ec0475b9206ba62454ab0f51a7fecbb9bfe103e9a7a1426f3b799d4f432ab2 ;;
  *) echo 'helm-unittest: supported hosts are Linux/macOS amd64/arm64' >&2; exit 1 ;;
esac

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
cache_dir="$repo_dir/tools/bin/helm-unittest/$version"
archive="$cache_dir/helm-unittest-$platform-$version.tgz"
mkdir -p "$cache_dir"
stage_dir=$(mktemp -d "$cache_dir/.stage.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT
mkdir -p "$stage_dir/plugins/unittest"
verify_archive() {
  local actual
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$1" | cut -d ' ' -f 1)
  else
    actual=$(shasum -a 256 "$1" | cut -d ' ' -f 1)
  fi
  [ "$actual" = "$digest" ] || { echo "helm-unittest: checksum mismatch for $1" >&2; exit 1; }
}
if [ ! -f "$archive" ]; then
  curl --fail --silent --show-error --location --retry 3 \
    "https://github.com/helm-unittest/helm-unittest/releases/download/v$version/helm-unittest-$platform-$version.tgz" \
    --output "$stage_dir/plugin.tgz"
  # Verify before caching, then verify cached bytes again on every invocation.
  verify_archive "$stage_dir/plugin.tgz"
  mv "$stage_dir/plugin.tgz" "$archive"
fi
verify_archive "$archive"
tar -xzf "$archive" -C "$stage_dir/plugins/unittest"
export HELM_PLUGINS="$stage_dir/plugins"
cd "$repo_dir"
echo "helm-unittest v$version ($platform)"
"${HELM:-helm}" unittest --strict "$@" "${CHART_DIR:-charts/moco}"
