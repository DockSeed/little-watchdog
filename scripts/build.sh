#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Release build: static binaries for linux amd64, arm64 and armv6 (Raspberry Pi
# OS "armhf" is ARMv6 and up, so GOARM=6), the .deb and .tar.gz of each, and
# SHA256SUMS over all of it. Same source and same Go version give the same
# bytes: -trimpath, no VCS stamping, SOURCE_DATE_EPOCH from the commit.
# Used by CI, by the release workflow and by hand:
#   scripts/build.sh [VERSION [OUTDIR]]     (defaults: 0.0.0~dev, ./dist)
# VERSION must start with a digit (the .deb needs that).
set -euo pipefail

version=${1:-0.0.0~dev}
out=${2:-dist}
case $version in [0-9]*) ;; *) echo "VERSION must start with a digit: $version" >&2; exit 2 ;; esac
cd "$(dirname "$0")/.."

# Never let go fetch another toolchain or any module.
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0
if [ "$(go list -m all)" != "$(go list -m)" ]; then
	echo "go.mod has dependencies; this project uses the standard library only" >&2
	exit 1
fi

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
	# the time of the commit of THIS repository, never of some repository around it
	[ "$(git rev-parse --show-toplevel 2>/dev/null)" = "$PWD" ] ||
		{ echo "not the root of a git checkout: set SOURCE_DATE_EPOCH" >&2; exit 1; }
	SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) ||
		{ echo "no commit here: set SOURCE_DATE_EPOCH" >&2; exit 1; }
fi
export SOURCE_DATE_EPOCH

mkdir -p "$out"
out=$(cd "$out" && pwd)

build() { # name GOARCH [GOARM]
	GOOS=linux GOARCH=$2 GOARM=${3:-} go build -trimpath -buildvcs=false \
		-ldflags="-s -w -X main.version=${version}" \
		-o "$out/little-wd_${version}_linux_$1" ./cmd/little-wd
}
build amd64 amd64
build arm64 arm64
build armv6 arm 6

scripts/package.sh "$version" "$out"
(cd "$out" && sha256sum -- little-wd_* *.deb *.tar.gz >SHA256SUMS)
ls -l "$out"
