#!/usr/bin/env bash
# Prepares the AUR commit for a release (M9). It never pushes: you push to
# the AUR with your own key.
#
#   packaging/aur/update.sh 0.1.0 [aur-clone-dir]
#     fills pkgver and the sha256 sums from the release's checksums.txt,
#     regenerates .SRCINFO, and (with a clone of
#     ssh://aur@aur.archlinux.org/ternly-bin.git) copies both in and commits.
#   CHECKSUMS=dist/checksums.txt packaging/aur/update.sh 0.0.0-SNAPSHOT-x
#     uses a local checksums.txt instead (a dry run against a snapshot).
set -euo pipefail
ver=${1:?usage: update.sh <version> [aur-clone-dir]}
clone=${2:-}
here=$(cd "$(dirname "$0")/ternly-bin" && pwd)
url=https://github.com/rajasatyajit/ternly/releases/download/v$ver/checksums.txt
sums=$(if [ -n "${CHECKSUMS:-}" ]; then cat "$CHECKSUMS"; else curl -fsSL "$url"; fi)
sum() { awk -v f="ternly_${ver}_linux_$1.tar.gz" '$2 == f { print $1 }' <<<"$sums"; }
x86=$(sum amd64) arm=$(sum arm64)
[ -n "$x86" ] && [ -n "$arm" ] || { echo "update.sh: no linux archives for $ver in checksums.txt" >&2; exit 1; }
pkgver=${ver//-/_} # pacman versions can't contain '-'
sed -i -e "s/^pkgver=.*/pkgver=$pkgver/" -e "s/^pkgrel=.*/pkgrel=1/" \
  -e "s/^sha256sums_x86_64=.*/sha256sums_x86_64=('$x86')/" \
  -e "s/^sha256sums_aarch64=.*/sha256sums_aarch64=('$arm')/" "$here/PKGBUILD"
if [ "$pkgver" != "$ver" ]; then # the archives keep the release's own version string
  sed -i "s/\${pkgver}/$ver/g; s/^pkgver=.*/pkgver=$pkgver/" "$here/PKGBUILD"
fi
(cd "$here" && makepkg --printsrcinfo > .SRCINFO)
echo "PKGBUILD and .SRCINFO updated for $ver (x86_64 $x86, aarch64 $arm)"
if [ -n "$clone" ]; then
  cp "$here/PKGBUILD" "$here/.SRCINFO" "$clone/"
  git -C "$clone" add PKGBUILD .SRCINFO
  git -C "$clone" commit -q -m "ternly-bin $ver"
  echo "committed in $clone — review, then: git -C $clone push"
fi
