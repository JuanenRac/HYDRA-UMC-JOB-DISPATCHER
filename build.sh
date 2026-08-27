#!/usr/bin/env bash
# HYDRA-UMC-JOB-DISPATCHER - build.sh
# Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
# GPL-3.0 - see LICENSE
set -euo pipefail
python3 "$(dirname "$0")/bump_manifest_version.py" || exit 1
cd "$(dirname "$0")"

# Keep the window open if this was double-clicked (e.g. from a file
# manager) instead of run from an already-open terminal - fires on
# success AND on a `set -e` early exit alike, but only prompts when
# stdin is actually a terminal (never in CI/piped/non-interactive runs).
trap '[ -t 0 ] && read -r -p "Press Enter to close..." _' EXIT

echo "=== HYDRA-UMC-JOB-DISPATCHER build ==="
python3 bump_version.py || echo "WARNING: could not bump version, continuing build anyway."

mkdir -p build
go build -o build/hydra-umc-job-dispatcher .

echo "Build OK: build/hydra-umc-job-dispatcher"
