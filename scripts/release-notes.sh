#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Prints the CHANGELOG section of a version (text between "## [X.Y.Z]" and the
# next "## "). Fails if there is none, so a tag without a changelog entry
# cannot be released:   scripts/release-notes.sh 0.1.0
set -euo pipefail

version=${1:?usage: release-notes.sh VERSION (without the leading v)}
cd "$(dirname "$0")/.."
notes=$(awk -v v="$version" '
	/^## / { on = (index($0, "## [" v "]") == 1); next }
	on { print }
' CHANGELOG.md)
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "CHANGELOG.md has no section '## [$version]'" >&2
	exit 1
fi
printf '%s\n' "$notes"
