# Windows Agent 冒烟脚本（PowerShell 2.0 兼容，Server 2008 R2 起可用）
#
# 用途：CI 在真实 Windows 主机上验证 Agent 可启动、状态读取与诊断输出。
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\windows_agent_smoke.ps1 -AgentExe dist\upgrade-agent.exe
#
# 说明：status 未注册时返回 2，diagnose 不能连接服务端时返回 1，
# 二者都属于「可预期的环境结果」，脚本只要求程序能正常运行并输出内容。

param(
    [string]$AgentExe = "dist\upgrade-agent.exe",
    [string]$DiagnoseServer = "http://127.0.0.1:1"
)

$ErrorActionPreference = "Stop"

function Fail([string]$Message) {
    Write-Host "[FAIL] $Message"
    exit 1
}

Write-Host "== Windows Agent 冒烟测试 =="
Write-Host ("PowerShell: " + $PSVersionTable.PSVersion.ToString())
Write-Host ("OS: " + [System.Environment]::OSVersion.VersionString)
Write-Host ("Agent: " + $AgentExe)

if (-not (Test-Path -LiteralPath $AgentExe)) {
    Fail "Agent 可执行文件不存在: $AgentExe"
}

# 1) version 必须成功
$out = (& $AgentExe version 2>&1 | Out-String)
$code = $LASTEXITCODE
Write-Host $out
if ($code -ne 0) {
    Fail "version 退出码 $code"
}
if ($out -notmatch "upgrade-agent") {
    Fail "version 输出缺少标识"
}

# 2) status -json：0=已注册，2=未注册（干净主机属于正常情况）
$out = (& $AgentExe status -json 2>&1 | Out-String)
$code = $LASTEXITCODE
Write-Host $out
if ($code -ne 0 -and $code -ne 2) {
    Fail "status 退出码 $code"
}
if ($out.Trim().Length -eq 0) {
    Fail "status 无输出"
}

# 3) diagnose：服务端不可达时退出码 1，但必须给出各阶段诊断信息而不是崩溃
$out = (& $AgentExe diagnose -server $DiagnoseServer -timeout 2s 2>&1 | Out-String)
$code = $LASTEXITCODE
Write-Host $out
if ($code -ne 0 -and $code -ne 1) {
    Fail "diagnose 退出码 $code"
}
if ($out.Trim().Length -eq 0) {
    Fail "diagnose 无输出"
}

Write-Host "[OK] Windows Agent 冒烟测试通过"
exit 0
