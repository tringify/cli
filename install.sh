#!/bin/sh
# Install the Tringify CLI on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/tringify/cli/main/install.sh | sh
#
# Environment:
#   TRINGIFY_CLI_VERSION  release tag to install (default: latest)
#   TRINGIFY_CLI_BIN      directory for the tringify command (default: ~/.local/bin)
set -eu

repo="tringify/cli"
version="${TRINGIFY_CLI_VERSION:-latest}"
bin_dir="${TRINGIFY_CLI_BIN:-$HOME/.local/bin}"

fail() { echo "tringify install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported operating system $(uname -s); on Windows use install.ps1 (see https://github.com/$repo#install)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

if [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi
name="tringify-$os-$arch"
archive="$name.tar.gz"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

echo "Downloading $archive ($version)"
curl -fsSL "$base/$archive" -o "$work/$archive" || fail "download failed: $base/$archive"
curl -fsSL "$base/SHA256SUMS" -o "$work/SHA256SUMS" || fail "download failed: $base/SHA256SUMS"

expected="$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$work/SHA256SUMS")"
[ -n "$expected" ] || fail "no checksum published for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$work/$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$work/$archive" | awk '{print $1}')"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$work/$archive" -C "$work"
[ -f "$work/$name/tringify" ] || fail "unexpected archive layout"
mkdir -p "$bin_dir"
install -m 0755 "$work/$name/tringify" "$bin_dir/tringify"

echo "Installed $("$bin_dir/tringify" version) to $bin_dir/tringify"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "Add $bin_dir to your PATH to run tringify." ;;
esac
