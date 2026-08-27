@echo off
REM =============================================================================
REM HYDRA-UMC-JOB-DISPATCHER - run.bat
REM Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
REM GPL-3.0 - see LICENSE
REM =============================================================================
REM HYDRA-UMC-JOB-DISPATCHER - run.bat
REM Runs the already-built binary. Run build.bat first.
setlocal
cd /d "%~dp0"

if exist build\hydra-umc-job-dispatcher.exe (
    build\hydra-umc-job-dispatcher.exe %*
) else (
    echo No compiled binary found. Run build.bat first.
    pause
    exit /b 1
)
endlocal
pause
