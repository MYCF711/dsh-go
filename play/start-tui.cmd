@echo off
chcp 65001 >nul
title dsh-go TUI (Martty)
cd /d "D:\DeepSeek Harness\default-workspace\DSH-GO\play"
"C:\Users\Administrator\AppData\Roaming\npm\martty-cli.cmd" --agent "D:\DeepSeek Harness\default-workspace\DSH-GO\tmp\DSH GO.exe" --agent-arg acp --workspace "D:\DeepSeek Harness\default-workspace\DSH-GO\play" --theme dark
echo.
echo [martty 已退出] 按任意键关闭
pause >nul
