#!/usr/bin/env bash
# HYDRA_UMC_SCRIPT_STANDARD_HEADER_BEGIN
# *****************************************************************************
# Project   : HYDRA-UMC-JOB-DISPATCHER
# Script    : run.sh
# Purpose   : Runtime workflow for the project entry point.
# Author    : JuanenRac (Electro Hobby 3D)
# Email     : electrohobby3d@gmail.com
# Copyright : (C) 2026 JuanenRac
# License   : GPL-3.0 - see LICENSE
# *****************************************************************************
# HYDRA_UMC_SCRIPT_STANDARD_HEADER_END
# HYDRA_UMC_SCRIPT_STANDARD_BANNER_BEGIN
printf '\n*******************************************************************************\n'
printf '%s\n' "* HYDRA-UMC-JOB-DISPATCHER - run.sh"
printf '%s\n' "* Mode      : RUN WORKFLOW"
printf '%s\n' "* Author    : JuanenRac (Electro Hobby 3D)"
printf '%s\n' "* Email     : electrohobby3d@gmail.com"
printf '%s\n' "* Copyright : (C) 2026 JuanenRac"
printf '%s\n' "* License   : GPL-3.0 - see LICENSE"
printf '%s\n' "* ------------------------------------------------------------------------- *"
printf '%s\n' "* 1. Resolve the runtime prerequisites declared by this script."
printf '%s\n' "* 2. Start the project entry point and forward user arguments unchanged."
printf '%s\n' "* 3. Preserve its result and keep an interactive terminal open."
printf '%s\n' "*******************************************************************************"
printf '\n'
# HYDRA_UMC_SCRIPT_STANDARD_BANNER_END
set -uo pipefail  # no -e: we need to reach the trap below even if the binary exits non-zero
cd "$(dirname "$0")"

# Keep the window open if this was double-clicked instead of run from an
# already-open terminal - only prompts when stdin is actually a terminal
# (never in CI/piped/non-interactive runs). Not `exec`ing the binary
# below (a deliberate change from a plain passthrough) is what lets this
# trap still run once the server itself exits.
trap '[ -t 0 ] && read -r -p "Press Enter to close..." _' EXIT

if [ -x build/hydra-umc-job-dispatcher ]; then
    build/hydra-umc-job-dispatcher "$@"
    exit $?
elif [ -x build/hydra-umc-job-dispatcher.exe ]; then
    build/hydra-umc-job-dispatcher.exe "$@"
    exit $?
else
    echo "No compiled binary found. Run build.sh first." >&2
    exit 1
fi
