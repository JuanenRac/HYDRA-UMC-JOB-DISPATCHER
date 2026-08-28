@echo off
REM HYDRA_UMC_SCRIPT_STANDARD_HEADER_BEGIN
REM *****************************************************************************
REM Project   : HYDRA-UMC-JOB-DISPATCHER
REM Script    : run.bat
REM Purpose   : Runtime workflow for the project entry point.
REM Author    : JuanenRac (Electro Hobby 3D)
REM Email     : electrohobby3d@gmail.com
REM Copyright : (C) 2026 JuanenRac
REM License   : GPL-3.0 - see LICENSE
REM *****************************************************************************
REM HYDRA_UMC_SCRIPT_STANDARD_HEADER_END
REM HYDRA_UMC_SCRIPT_STANDARD_BANNER_BEGIN
echo.
echo *****************************************************************************
echo * HYDRA-UMC-JOB-DISPATCHER - run.bat
echo * Mode      : RUN WORKFLOW
echo * Author    : JuanenRac (Electro Hobby 3D)
echo * Email     : electrohobby3d@gmail.com
echo * Copyright : (C) 2026 JuanenRac
echo * License   : GPL-3.0 - see LICENSE
echo * ------------------------------------------------------------------------- *
echo * 1. Resolve the runtime prerequisites declared by this script.
echo * 2. Start the project entry point and forward user arguments unchanged.
echo * 3. Preserve its result and keep an interactive terminal open.
echo *****************************************************************************
echo.
REM HYDRA_UMC_SCRIPT_STANDARD_BANNER_END
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
