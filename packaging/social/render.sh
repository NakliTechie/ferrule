#!/bin/sh
# Render the social card at 2x and downsample, so the type is crisp at GitHub's 1280×640.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../../marketing/social.png"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu \
  --hide-scrollbars --force-device-scale-factor=2 --window-size=1280,640 \
  --screenshot="$tmp/2x.png" "file://$here/social.html" 2>/dev/null
sips -z 640 1280 "$tmp/2x.png" --out "$out" >/dev/null
sips -g pixelWidth -g pixelHeight "$out" | tail -2
ls -l "$out" | awk '{print "  " $5 " bytes  " $9}'
