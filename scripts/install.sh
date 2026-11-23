#!/bin/sh
# Installs Anyserve for the current user, with no root needed:
#
#   curl -fsSL https://github.com/jonsjava/anyserve/releases/latest/download/install.sh | sh
#
# Anything after `sh -s --` is passed to the installed anyserve, e.g.
#   ... | sh -s -- fleet join <manager> <token>
#
# The binary is checked against the release's checksums, and the checksums
# against Anyserve's release key (with openssl, when it's installed).
set -eu

REPO=jonsjava/anyserve
BIN_DIR=${ANYSERVE_BIN_DIR:-$HOME/.local/bin}
RELEASE_KEY_PEM='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAdAUFcHwaJokGxbvj5dQZCu6Ise6TFdiMNSGlHNreXNo=
-----END PUBLIC KEY-----'

say() { printf '%s\n' "$*" >&2; }
die() { say "anyserve install: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "this needs $1"; }

need curl
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s); see https://github.com/$REPO for Windows" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

version=${ANYSERVE_VERSION:-}
if [ -z "$version" ]; then
  # The latest release's URL ends in its tag.
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") || die "can't reach GitHub"
  version=${url##*/}
fi
case "$version" in v[0-9]*) ;; *) die "no release found (got '$version')" ;; esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
base=${ANYSERVE_RELEASE_BASE:-"https://github.com/$REPO/releases/download/$version"}
asset="anyserve-$version-$os-$arch"
say "Downloading Anyserve $version for $os/$arch..."
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "download failed"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS-$version" || die "checksums missing"
curl -fsSL -o "$tmp/SHA256SUMS.sig" "$base/SHA256SUMS-$version.sig" || die "signature missing"

if command -v openssl >/dev/null 2>&1; then
  printf '%s\n' "$RELEASE_KEY_PEM" > "$tmp/key.pem"
  base64 -d < "$tmp/SHA256SUMS.sig" > "$tmp/sig.bin" 2>/dev/null || openssl base64 -d -in "$tmp/SHA256SUMS.sig" -out "$tmp/sig.bin"
  openssl pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$tmp/SHA256SUMS" -sigfile "$tmp/sig.bin" >/dev/null 2>&1 \
    || die "the checksums aren't signed by Anyserve's release key; not installing"
  say "Signature verified."
else
  say "openssl isn't installed, so only the checksum (fetched over HTTPS) is checked."
fi

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "no checksum for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$got" = "$want" ] || die "checksum mismatch; not installing"

mkdir -p "$BIN_DIR"
chmod 755 "$tmp/$asset"
mv "$tmp/$asset" "$BIN_DIR/anyserve"
say "Installed $BIN_DIR/anyserve ($version)."
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Add $BIN_DIR to your PATH, e.g.: echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.profile" ;;
esac

if [ $# -gt 0 ]; then
  exec "$BIN_DIR/anyserve" "$@"
fi
say "Next: run 'anyserve' for a guided install."
