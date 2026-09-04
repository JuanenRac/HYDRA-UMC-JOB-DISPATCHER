// HYDRA-UMC-JOB-DISPATCHER - version.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Go has no native version field in go.mod for an application binary
// (that field is only for library module resolution), so this repo
// carries its own release version here, bumped by bump_version.py on
// every real build (see build.bat/build.sh) using the ecosystem-wide
// odometer/mileage-counter rule (patch+1 per build, carry into minor
// past 9, carry into major past 9, major never wraps).
package main

// Version is the current release version of HYDRA-UMC-JOB-DISPATCHER.
const Version = "0.1.0"