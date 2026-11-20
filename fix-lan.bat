@echo off
chcp 65001 >nul
rem File Linker: allow phone access from the same LAN.
rem Right-click this file, then choose "Run as administrator".

net session >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Administrator rights required.
  echo Right-click this file and choose "Run as administrator".
  pause
  exit /b 1
)

echo ==^> Set network profile to Private
powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-NetConnectionProfile | ForEach-Object { if ($_.NetworkCategory -ne 'Private') { $old = $_.NetworkCategory; Set-NetConnectionProfile -InterfaceIndex $_.InterfaceIndex -NetworkCategory Private -ErrorAction SilentlyContinue; Write-Host ('   ' + $_.Name + ': ' + $old + ' -^> Private') } else { Write-Host ('   ' + $_.Name + ': already Private') } }"

echo ==^> Allow inbound TCP 8080
powershell -NoProfile -ExecutionPolicy Bypass -Command "$n = 'File Linker LAN 8080'; if (-not (Get-NetFirewallRule -DisplayName $n -ErrorAction SilentlyContinue)) { New-NetFirewallRule -DisplayName $n -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8080 -Profile Private,Public | Out-Null; Write-Host '   firewall rule created' } else { Write-Host '   firewall rule already exists' }"

echo.
echo Done. Use one of these URLs on your phone:
powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' -and $_.InterfaceAlias -notmatch 'VMware|VirtualBox|vEthernet|Hyper-V|Loopback' } | ForEach-Object { Write-Host ('   http://' + $_.IPAddress + ':8080') }"

echo.
echo If the server window is still open, restart it once, then open the URL on your phone.
pause
