# 构建 Windows Agent MSI（WiX Toolset v3）
#
# 依赖：candle.exe / light.exe（CI: choco install wixtoolset -y）
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\build_windows_msi.ps1 `
#     -AgentExe dist\upgrade-agent.exe -Version 0.1.0
#
# 产物：dist\upgrade-agent-<version>-x64.msi
# 安装：
#   msiexec /i dist\upgrade-agent-0.1.0-x64.msi SERVERURL=https://upgrade.example.com ENROLLTOKEN=<令牌>

param(
    [string]$AgentExe = 'dist\upgrade-agent.exe',
    [string]$Version = '0.1.0',
    [string]$OutDir = 'dist'
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $AgentExe)) {
    throw "Agent 可执行文件不存在: $AgentExe"
}
# MSI Version 必须为数字（major.minor.build[.revision]）。
if ($Version -notmatch '^\d+\.\d+\.\d+(\.\d+)?$') {
    Write-Host "版本 '$Version' 不是 MSI 数字版本，回退为 0.1.0"
    $Version = '0.1.0'
}

$wxs = Join-Path $PSScriptRoot '..\build\windows\agent.wxs'
if (-not (Test-Path -LiteralPath $wxs)) {
    throw "WiX 源文件不存在: $wxs"
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

# 查找 WiX 工具：PATH → 常见安装目录 → chocolatey shim 目录。
function Find-WixTool([string]$Name) {
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    $bases = @()
    $pf86 = ${env:ProgramFiles(x86)}
    $pf = $env:ProgramFiles
    foreach ($root in @($pf86, $pf)) {
        if (-not $root) { continue }
        $dirs = Get-ChildItem -Path $root -Filter 'WiX Toolset v3*' -Directory -ErrorAction SilentlyContinue
        foreach ($d in $dirs) { $bases += (Join-Path $d.FullName 'bin') }
    }
    $bases += 'C:\ProgramData\chocolatey\bin'
    foreach ($base in $bases) {
        $p = Join-Path $base $Name
        if (Test-Path -LiteralPath $p) { return $p }
    }
    throw "未找到 $Name；请安装 WiX Toolset v3（choco install wixtoolset -y）"
}

$candle = Find-WixTool 'candle.exe'
$light = Find-WixTool 'light.exe'
$agentPath = (Resolve-Path -LiteralPath $AgentExe).Path
$obj = Join-Path $OutDir 'upgrade-agent.wixobj'
$msi = Join-Path $OutDir ("upgrade-agent-{0}-x64.msi" -f $Version)

Write-Host "candle: $candle"
Write-Host "light : $light"
Write-Host "agent : $agentPath"
Write-Host "msi   : $msi"

& $candle -nologo -arch x64 "-dVersion=$Version" "-dAgentExe=$agentPath" -out $obj $wxs
if ($LASTEXITCODE -ne 0) { throw "candle 失败: $LASTEXITCODE" }

& $light -nologo -out $msi $obj
if ($LASTEXITCODE -ne 0) { throw "light 失败: $LASTEXITCODE" }
if (-not (Test-Path -LiteralPath $msi)) { throw "MSI 未生成: $msi" }

Write-Host "[OK] 已生成 $msi"
