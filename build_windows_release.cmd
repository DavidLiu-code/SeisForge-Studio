@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0build_windows_release.ps1" %*
exit /b %ERRORLEVEL%
