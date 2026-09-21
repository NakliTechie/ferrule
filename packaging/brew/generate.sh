#!/bin/sh
# Render the Homebrew formula and cask for one release into a tap checkout.
#
#   packaging/brew/generate.sh v1.1.0 /path/to/homebrew-tap [path/to/SHA256SUMS]
#
# Reads SHA256SUMS from the published release (or the local file the release job just
# built), so it can only describe assets that exist; a checksum list missing an asset
# fails here rather than producing a formula that 404s on install.
set -eu
tag=${1:?tag, e.g. v1.1.0}
tap=${2:?path to the homebrew-tap checkout}
version=${tag#v}
here=$(dirname "$0")
if [ -n "${3:-}" ]; then
  sums=$(cat "$3")
else
  sums=$(curl -fsSL "https://github.com/NakliTechie/ferrule/releases/download/$tag/SHA256SUMS")
fi

sum() {
  s=$(printf '%s\n' "$sums" | awk -v f="$1" '$2 == f { print $1 }')
  [ -n "$s" ] || { echo "generate: $1 is not in SHA256SUMS for $tag" >&2; exit 1; }
  printf '%s' "$s"
}

mkdir -p "$tap/Formula" "$tap/Casks"
sed -e "s/__VERSION__/$version/g" \
    -e "s/__SHA_DARWIN_ARM64__/$(sum ferrule-darwin-arm64)/" \
    -e "s/__SHA_DARWIN_AMD64__/$(sum ferrule-darwin-amd64)/" \
    -e "s/__SHA_LINUX_ARM64__/$(sum ferrule-linux-arm64)/" \
    -e "s/__SHA_LINUX_AMD64__/$(sum ferrule-linux-amd64)/" \
    "$here/ferrule.rb.tmpl" > "$tap/Formula/ferrule.rb"
sed -e "s/__VERSION__/$version/g" \
    -e "s/__SHA_MACOS_ZIP__/$(sum Ferrule-macos.zip)/" \
    "$here/ferrule-cask.rb.tmpl" > "$tap/Casks/ferrule.rb"
echo "  $tap/Formula/ferrule.rb"
echo "  $tap/Casks/ferrule.rb"
