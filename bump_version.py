#!/usr/bin/env python3
# =============================================================================
# HYDRA-UMC-JOB-DISPATCHER - bump_version.py
# Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
# GPL-3.0 - see LICENSE
#
# Ecosystem-wide versioning policy: the version bumps automatically on
# every REAL build (every run of build.bat/build.sh - the only thing
# that counts as a "build" for a Go binary; just running `go run .`
# directly does NOT bump anything), using an odometer/mileage-counter
# rule in base 10: the last component (patch) goes up by 1; if it
# would roll past 9, it resets to 0 and carries 1 into the component
# to its left (minor), which repeats the same rule leftward (major has
# no component further left, so it just keeps counting past 9 with no
# reset - same as a real odometer's leftmost digit never wrapping back
# to 0 mid-drive).
# Example: 0.1.9 -> 0.2.0. Another: 0.9.9 -> 0.0.0.
#
# Called by build.bat/build.sh BEFORE `go build` runs, so every
# compiled binary carries a version strictly newer than the last one
# actually shipped (edits the `const Version = "X.Y.Z"` line in
# version.go, since go.mod has no such field for application binaries).
# Standalone-runnable too (invoked twice in a row is how this script's
# own bump logic gets verified without needing a full go build each
# time).
# =============================================================================
from __future__ import annotations

import re
import sys
from pathlib import Path

VERSION_GO = Path(__file__).resolve().parent / "version.go"
VERSION_RE = re.compile(r'^const Version = "([^"]+)"\s*$', re.MULTILINE)


def bump(version: str) -> str:
    """Applies the base-10 odometer/carry rule to a MAJOR.MINOR.PATCH
    string and returns the next version string."""
    parts = [int(p) for p in version.split(".")]
    i = len(parts) - 1
    parts[i] += 1
    while i > 0 and parts[i] > 9:
        parts[i] = 0
        parts[i - 1] += 1
        i -= 1
    return ".".join(str(p) for p in parts)


def main() -> int:
    if not VERSION_GO.is_file():
        print(f"ERROR: {VERSION_GO} not found - cannot bump version.", file=sys.stderr)
        return 1

    text = VERSION_GO.read_text(encoding="utf-8")
    match = VERSION_RE.search(text)
    if not match:
        print(f'ERROR: no const Version = "X.Y.Z" line found in {VERSION_GO}.', file=sys.stderr)
        return 1

    old_version = match.group(1)
    new_version = bump(old_version)
    new_text = text[: match.start()] + f'const Version = "{new_version}"' + text[match.end() :]
    VERSION_GO.write_text(new_text, encoding="utf-8")

    print(f"Version bumped: {old_version} -> {new_version}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
