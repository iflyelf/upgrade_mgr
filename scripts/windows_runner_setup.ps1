# Windows 自托管 Runner 初始化（Server 2008 R2 / Windows 10+ / arm64）
#
# 用途：在真实 Windows 机器上准备 Go 1.20 + GitHub Actions Runner，
# 供 .github/workflows/windows-e2e.yml 的 2008 R2 / arm64 真机回归使用。
#
# 前置：
#   - 管理员 PowerShell；
#   - Server 2008 R2 需 .NET Framework 4.6.2+（Runner 依赖），并已安装对应补丁；
#   - 机器能访问 github.com / go.dev（离线环境请先自行下载 zip 并用 -GoZip/-RunnerZip 指定）。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\windows_runner_setup.ps1 `
#     -RepoURL https://github.com/iflyelf/upgrade_mgr `
#     -RunnerToken <注册令牌> `
#     -Labels server2008r2
#
# 注意：2008 R2 建议使用仍支持该系统的 Runner 版本（默认 2.311.0）；
#       更高版本可能因 .NET 运行时要求无法启动。

param(
    [Parameter(Mandatory = $true)][string]$RepoURL,
    [Parameter(Mandatory = $true)][string]$RunnerToken,
    [string]$RunnerVersion = '2.311.0',
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    [string]$InstallDir = 'C:\actions-runner',
    [string]$GoVersion = '1.20.14',
    [string]$GoDir = 'C:\Go',
    [string]$Labels = 'server2008r2',
    [string]$GoZip = '',
    [string]$RunnerZip = '',
    # 国内网络：Go 官方源不可达时自动回退阿里云镜像。
    [string]$GoMirror = 'https://mirrors.aliyun.com/golang',
    # 可选 GitHub 加速前缀，如 https://gh-proxy.com/ ；留空只用官方地址。
    [string]$RunnerMirrorPrefix = ''
)

$ErrorActionPreference = 'Stop'

# 老系统默认只启用 TLS 1.0；GitHub/go.dev 需要 TLS 1.2+。
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch { }

function Fail([string]$Message) {
    Write-Host "[FAIL] $Message"
    exit 1
}

function Download([string]$Url, [string]$Dest) {
    Write-Host "下载: $Url"
    $wc = New-Object System.Net.WebClient
    $wc.DownloadFile($Url, $Dest)
}

# Download-First 依次尝试多个地址，全部失败才报错（国内网络回退）。
function Download-First([string[]]$Urls, [string]$Dest) {
    $last = $null
    foreach ($u in $Urls) {
        if (-not $u) { continue }
        try {
            Download $u $Dest
            if (Test-Path -LiteralPath $Dest) { return }
        } catch {
            $last = $_.Exception.Message
            Write-Host "  ↳ 失败: $last"
            if (Test-Path -LiteralPath $Dest) { Remove-Item -LiteralPath $Dest -Force }
        }
    }
    throw "所有下载地址均失败: $last"
}

# PS 2.0 没有 Expand-Archive，使用 .NET API（需 .NET 4.5+）。
function Expand-Zip([string]$Zip, [string]$Dir) {
    if (Test-Path -LiteralPath $Dir) {
        Remove-Item -LiteralPath $Dir -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($Zip, $Dir)
}

Write-Host "== 配置 Windows Runner =="
Write-Host ("OS: " + [System.Environment]::OSVersion.VersionString)

# 1) Go
$goExe = Join-Path $GoDir 'bin\go.exe'
if (-not (Test-Path $goExe)) {
    $zip = $GoZip
    if ($zip -eq '') {
        $zip = Join-Path $env:TEMP "go-$GoVersion-$Arch.zip"
        $urls = @("https://go.dev/dl/go$GoVersion.windows-$Arch.zip")
        if ($GoMirror -ne '') {
            $urls += ($GoMirror.TrimEnd('/') + "/go$GoVersion.windows-$Arch.zip")
        }
        Download-First $urls $zip
    }
    if (-not (Test-Path $zip)) { Fail "Go 压缩包不存在: $zip" }
    Expand-Zip $zip $GoDir
}
if (-not (Test-Path $goExe)) { Fail "Go 安装失败: $goExe" }
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
if ($machinePath -notlike "*$GoDir\bin*") {
    [Environment]::SetEnvironmentVariable('Path', $machinePath + ";$GoDir\bin", 'Machine')
}
$env:Path = "$GoDir\bin;" + $env:Path
$goVer = (& go version) 2>&1
Write-Host $goVer
if ($goVer -notmatch 'go1\.20\.') {
    Fail "需要 Go 1.20.x（Go 1.21+ 不再支持 Server 2008 R2）"
}

# 2) Actions Runner
$runnerPackage = "actions-runner-win-$Arch-$RunnerVersion.zip"
if ($RunnerZip -eq '') {
    $RunnerZip = Join-Path $env:TEMP $runnerPackage
    $official = "https://github.com/actions/runner/releases/download/v$RunnerVersion/$runnerPackage"
    $urls = @()
    if ($RunnerMirrorPrefix -ne '') {
        $urls += ($RunnerMirrorPrefix.TrimEnd('/') + '/' + $official)
    }
    $urls += $official
    Download-First $urls $RunnerZip
}
if (-not (Test-Path $RunnerZip)) { Fail "Runner 压缩包不存在: $RunnerZip" }
if (-not (Test-Path (Join-Path $InstallDir 'config.cmd'))) {
    Expand-Zip $RunnerZip $InstallDir
}

Push-Location $InstallDir
try {
    Write-Host "注册 Runner（labels=$Labels）"
    & .\config.cmd --url $RepoURL --token $RunnerToken --labels $Labels --unattended --replace
    if ($LASTEXITCODE -ne 0) { Fail "config.cmd 失败: $LASTEXITCODE" }

    Write-Host "安装并启动 Runner 服务"
    & .\svc.cmd install
    & .\svc.cmd start
} finally {
    Pop-Location
}

Write-Host "[OK] Runner 已注册并启动；在仓库 Settings → Variables 设置 ENABLE_WIN2008R2_RUNNER=true 后启用流水线。"
