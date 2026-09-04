#!/bin/sh
# Install Ferrule from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/NakliTechie/ferrule/main/install.sh | sh
#
# Downloads the binary for this machine, checks it against the release's SHA256SUMS, and
# puts it somewhere on PATH. No sudo unless /usr/local/bin is the only option and it is
# not writable, in which case it says so and installs to ~/.local/bin instead.
#
# On an Apple Silicon Mac you probably want the app instead: the release has a
# Ferrule-macos.zip you can drag to Applications.
set -eu

REPO=NakliTechie/ferrule
API="https://api.github.com/repos/$REPO/releases/latest"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

for tool in curl shasum; do
  command -v "$tool" >/dev/null 2>&1 || {
    [ "$tool" = shasum ] && command -v sha256sum >/dev/null 2>&1 && continue
    die "$tool is required"
  }
done
sha() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1"; else sha256sum "$1"; fi
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
  darwin|linux) ;;
  *) die "unsupported system: $os — Windows users download the .exe from the release page" ;;
esac
asset="ferrule-$os-$arch"

# Where the binary goes: the first directory on PATH this user can write to.
target=""
for dir in "$HOME/.local/bin" /usr/local/bin; do
  case ":$PATH:" in *":$dir:"*) [ -w "$dir" ] || [ ! -e "$dir" ] && target=$dir && break ;; esac
done
[ -n "$target" ] || target="$HOME/.local/bin"
mkdir -p "$target"

tag=$(curl -fsL "$API" 2>/dev/null | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$tag" ] || die "no published release yet — build from source with 'make build'"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
say "Ferrule $tag — $asset"
curl -fsSL "$base/$asset" -o "$tmp/$asset" || die "could not download $asset"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "could not download SHA256SUMS"

# A download nobody checked is a download somebody could have replaced.
want=$(grep " $asset\$" "$tmp/SHA256SUMS" | awk '{print $1}' | head -1)
[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
got=$(sha "$tmp/$asset" | awk '{print $1}')
[ "$want" = "$got" ] || die "checksum mismatch for $asset — refusing to install
  expected $want
  got      $got"

chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$target/ferrule"
say "Installed $target/ferrule"
case ":$PATH:" in
  *":$target:"*) say "Run: ferrule serve" ;;
  *) say "Add $target to your PATH, then run: ferrule serve" ;;
esac
