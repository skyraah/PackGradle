
@REM installer=$(curl -s https://api.github.com/packwiz/packwiz-installer/releases/latest | grep browser_download_url | cut -d'"' -f4 |grep -E '.jar$')
@REM bootstrap=$(curl -s https://api.github.com/packwiz/packwiz-installer-bootstrap/releases/latest | grep browser_download_url | cut -d'"' -f4 |grep -E '.jar$')

@REM echo "${installer}"
@REM wget -P ./lib ${installer}
@REM echo "${bootstrap}"
@REM wget -P ./lib ${bootstrap}

@REM pause 按空格继续

@echo off
setlocal

echo.
echo ================================
echo   Downloading Packwiz Installer
echo ================================
echo.

if not exist "%~dp0lib" mkdir "%~dp0lib"

echo [1/2] Downloading packwiz-installer...

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
    "$r = Invoke-RestMethod 'https://api.github.com/repos/packwiz/packwiz-installer/releases/latest';" ^
    "$a = $r.assets | Where-Object { $_.name -like '*.jar' } | Select-Object -First 1;" ^
    "Invoke-WebRequest $a.browser_download_url -OutFile '%~dp0lib\packwiz-installer.jar'"

if errorlevel 1 (
    echo.
    echo [ERROR] packwiz-installer 下载失败！
    goto error
)

echo [OK] packwiz-installer 下载完成。
echo.

echo [2/2] Downloading packwiz-installer-bootstrap...

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
    "$r = Invoke-RestMethod 'https://api.github.com/repos/packwiz/packwiz-installer-bootstrap/releases/latest';" ^
    "$a = $r.assets | Where-Object { $_.name -like '*.jar' } | Select-Object -First 1;" ^
    "Invoke-WebRequest $a.browser_download_url -OutFile '%~dp0lib\packwiz-installer-bootstrap.jar'"

if errorlevel 1 (
    echo.
    echo [ERROR] packwiz-installer-bootstrap 下载失败！
    goto error
)

echo.
echo ================================
echo          Download Complete
echo ================================
echo.
echo 文件已经下载到：
echo %~dp0lib
echo.

goto end

:error
echo.
echo ================================
echo             ERROR
echo ================================
echo.
echo 请检查上面的错误信息。
echo.

:end
pause
