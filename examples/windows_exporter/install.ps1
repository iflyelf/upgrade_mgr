<#
.SYNOPSIS
    windows_exporter 单文件安装/更新/卸载脚本（可直接作为平台制品或 script_url）。

.DESCRIPTION
    与 examples/node_exporter/install.sh 对齐的 Windows 版本：
      install    安装或覆盖升级（默认）
      update     更新已安装实例：备份旧 exe、替换、重启，失败自动回滚
      uninstall  停止并移除服务、二进制与数据（默认需要确认）
      status     查看安装状态与版本

    兼容 Windows Server 2008 R2 ~ Windows 11，PowerShell 2.0 起可用
    （下载使用 System.Net.WebClient，不依赖 Invoke-WebRequest / Expand-Archive）。

.PARAMETER Action
    install（默认）/ update / uninstall / status。

.PARAMETER Yes
    卸载时跳过交互确认。

.PARAMETER KeepData
    卸载时保留配置、日志与 textfile 指标目录。

.PARAMETER Arch
    amd64（默认）/ arm64。留空时按当前进程架构自动判断。

.PARAMETER Version
    目标版本，默认 latest（iflyelf/windows_exporter 的 latest 发布）。

.PARAMETER ListenPort
    监听端口，默认 16666。

.PARAMETER TextfileDir
    textfile 采集器目录，默认 C:\monitor\windows_exporter\textfile_inputs。

.PARAMETER InstallDir
    安装目录，默认 C:\Program Files\windows_exporter。

.PARAMETER BinaryPath
    本地已存在的 exe 路径（跳过下载）。老系统（Server 2008 R2 + PowerShell 2.0/.NET 2.0）
    不支持 TLS 1.2，无法直连 GitHub；推荐由平台先把 exe 下发到本机，再用本参数安装。

.PARAMETER BinaryUrl
    完整 exe 下载地址（可用 http）。也可把 exe 放到内网 HTTP 地址，用本参数直接下载。

.PARAMETER ConfigPath
    自定义配置文件路径；指定后安装/更新时复制为安装目录下的 config.yaml。

.PARAMETER NoConfig
    不生成/覆盖 config.yaml（使用已有配置）。

.PARAMETER NoFirewall
    不创建 Windows 防火墙入站规则。

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 install

.EXAMPLE
    .\install.ps1 update -Version latest

.EXAMPLE
    .\install.ps1 uninstall -Yes

.NOTES
    退出码：0 成功；1 失败；2 参数错误。
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('install', 'update', 'uninstall', 'status')]
    [string]$Action = 'install',

    [switch]$Yes,
    [switch]$KeepData,

    [string]$Arch = '',
    [string]$Version = 'latest',
    [int]$ListenPort = 16666,
    [string]$TextfileDir = 'C:\monitor\windows_exporter\textfile_inputs',
    [string]$InstallDir = '',
    [string]$BinaryPath = '',
    [string]$BinaryUrl = '',
    [string]$ConfigPath = '',
    [switch]$NoConfig,
    [switch]$NoFirewall
)

$ErrorActionPreference = 'Stop'

# 输出编码：Windows 控制台默认 GBK(OEM)，平台按 UTF-8 读取会出现乱码。
# 主动把 stdout 设为 UTF-8；失败不阻断（只影响日志可读性）。
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }
# 脚本自身含中文：PS2 在部分系统上按 ANSI 解析无 BOM 文件会乱码。
# 若检测到当前脚本不是 UTF-8 解码（$PSVersionTable 无 BOM 信息，这里做二次兜底），
# 只需保证输出编码即可；Agent 会在执行前转成 UTF-8 BOM。

# ---------- 常量 ----------
$ServiceName = 'windows_exporter'
$DisplayName = 'windows_exporter'
$FirewallRule = "windows_exporter (TCP $ListenPort)"
$ProxyPrefix = 'https://proxy.xiaonuo.live/?url='
if ($env:WINDOWS_EXPORTER_PROXY) { $ProxyPrefix = $env:WINDOWS_EXPORTER_PROXY }
if ($env:WINDOWS_EXPORTER_VERSION) { $Version = $env:WINDOWS_EXPORTER_VERSION }
if ($env:WINDOWS_EXPORTER_LISTEN_PORT) { $ListenPort = [int]$env:WINDOWS_EXPORTER_LISTEN_PORT }
# 自定义下载基础地址（末尾不要带斜杠），默认 iflyelf release。
$script:BaseUrl = ''
if ($env:WINDOWS_EXPORTER_BASE) { $script:BaseUrl = $env:WINDOWS_EXPORTER_BASE.TrimEnd('/') }
# 完整 exe 地址优先（支持 http，用于老系统无法 TLS 1.2 的场景）。
if ($env:WINDOWS_EXPORTER_BINARY_URL) { $BinaryUrl = $env:WINDOWS_EXPORTER_BINARY_URL }
if ($env:WINDOWS_EXPORTER_BINARY_PATH) { $BinaryPath = $env:WINDOWS_EXPORTER_BINARY_PATH }

if (-not $InstallDir) {
    if ($env:ProgramFiles) {
        $InstallDir = Join-Path $env:ProgramFiles 'windows_exporter'
    } else {
        $InstallDir = 'C:\Program Files\windows_exporter'
    }
}
$ExePath = Join-Path $InstallDir 'windows_exporter.exe'
$ExeBak = "$ExePath.bak"
$ConfigFile = Join-Path $InstallDir 'config.yaml'

# 输出约定（重要）：
#   - 不能用 Write-Host：它写 host/information 流，平台把 stdout 重定向到管道时收不到。
#   - 不能用 Write-Output：函数返回值就是管道输出，调用方一旦用 `| Out-Null` 或 `$x = f`
#     就会把日志一起吞掉（曾导致任务日志只剩几行、错误信息全丢）。
#   - 统一用 [Console]::Out / [Console]::Error 直接写进程 stdout/stderr，
#     所有 PowerShell 版本（含 2.0）都能被重定向捕获。
function Write-Step([string]$Message) { [Console]::Out.WriteLine("==> $Message") }
function Write-Warn([string]$Message) { [Console]::Out.WriteLine("WARN: $Message") }
function Fail([string]$Message, [int]$Code = 1) {
    # 用 [Environment]::Exit 而不是 exit：PowerShell 2.0 中 `exit` 在函数/管道里
    # 可能被 Out-Null 等管道消费掉，导致脚本“静默结束且退出码为 0”，
    # 平台因此误判安装成功（随后健康检查失败）。Environment::Exit 立即终止进程。
    [Console]::Error.WriteLine("ERROR: $Message")
    [Console]::Out.WriteLine("ERROR: $Message")
    [Console]::Out.Flush()
    [Environment]::Exit($Code)
}

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p = New-Object Security.Principal.WindowsPrincipal($id)
    return $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not (Test-Admin)) {
    Fail '需要管理员权限：请以“管理员 PowerShell”重新执行。' 1
}

# TLS：GitHub / 代理要求 TLS 1.2。.NET 2.0/3.5 没有 Tls12 枚举名，
# 直接按名字 Parse 会抛「未找到值 Tls12」，因此用数值位或（Tls12 = 3072）。
# 若底层 Schannel/.NET 确实不支持，下载会失败，下面会给出针对性提示。
$tls12Supported = $false
try {
    $mask = [int][Net.ServicePointManager]::SecurityProtocol -bor 3072
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]$mask
    $tls12Supported = $true
} catch {
    Write-Warn "无法启用 TLS 1.2（下载可能失败）：$($_.Exception.Message)"
}
Write-Step "TLS 1.2: $tls12Supported (PowerShell $($PSVersionTable.PSVersion), CLR $($PSVersionTable.CLRVersion))"
if (-not $tls12Supported) {
    Write-Warn '当前 .NET 不支持 TLS 1.2：无法从 GitHub/proxy 下载；请改用 -BinaryUrl 指向内网 HTTP 地址，或先安装 .NET Framework 4.5+'
}

function Resolve-Arch {
    if ($Arch) {
        switch ($Arch.ToLower()) {
            'amd64' { return 'amd64' }
            'x64' { return 'amd64' }
            'arm64' { return 'arm64' }
            default { Fail "不支持的架构: $Arch（可选 amd64 / arm64）" 2 }
        }
    }
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { return 'arm64' }
    if ($env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { return 'arm64' }
    return 'amd64'
}

function Get-ServiceState {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) { return 'Missing' }
    return $svc.Status.ToString()
}

function Wait-ServiceState([string]$Want, [int]$TimeoutSec = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        if ((Get-ServiceState) -eq $Want) { return $true }
        Start-Sleep -Seconds 1
    }
    return ((Get-ServiceState) -eq $Want)
}

function Stop-ServiceIfRunning {
    $state = Get-ServiceState
    if ($state -eq 'Missing' -or $state -eq 'Stopped') { return }
    Write-Step "停止服务 $ServiceName（当前 $state）"
    try {
        Stop-Service -Name $ServiceName -Force -ErrorAction Stop
    } catch {
        Write-Warn "Stop-Service 失败：$($_.Exception.Message)"
    }
    if (-not (Wait-ServiceState 'Stopped' 30)) {
        Write-Warn '服务未在 30 秒内停止，尝试强制结束进程'
        Get-Process -Name 'windows_exporter' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
    }
}

# 平台地址：Agent 执行脚本时会注入 UPGRADE_AGENT_SERVER_URL。
# 无法访问外网（或系统不支持 TLS 1.2）时，改从平台自身下载（平台代缓存，可用 HTTP）。
$PlatformURL = $env:UPGRADE_AGENT_SERVER_URL
if ($env:WINDOWS_EXPORTER_PLATFORM_URL) { $PlatformURL = $env:WINDOWS_EXPORTER_PLATFORM_URL }

function Download-Exporter([string]$OutFile) {
    if ($BinaryPath) {
        if (-not (Test-Path $BinaryPath)) {
            Fail "指定的本地安装包不存在: $BinaryPath" 1
        }
        Write-Step "使用本地安装包 $BinaryPath"
        Copy-Item -Force $BinaryPath $OutFile
        $len0 = (Get-Item $OutFile).Length
        if ($len0 -lt 1048576) {
            Fail "本地安装包过小（$len0 字节），可能不是 windows_exporter.exe: $BinaryPath" 1
        }
        return $OutFile
    }
    if ($BinaryUrl) {
        # 直接使用完整地址（内网 HTTP 镜像/制品地址），不再拼代理前缀。
        $url = $BinaryUrl
    } elseif ($script:BaseUrl) {
        $url = "$($script:BaseUrl)/windows_exporter-$($script:ResolvedArch).exe"
    } else {
        $base = "https://github.com/iflyelf/windows_exporter/releases/download/$Version/windows_exporter-$($script:ResolvedArch).exe"
        $url = "$ProxyPrefix$base"
    }
    Write-Step "下载 $url"
    if (Test-Path $OutFile) { Remove-Item -Force $OutFile -ErrorAction SilentlyContinue }
    try {
        $wc = New-Object System.Net.WebClient
        $wc.Headers.Add('User-Agent', 'upgrade-agent/windows_exporter-installer')
        $wc.DownloadFile($url, $OutFile)
    } catch {
        $first = $_.Exception.Message
        # 回退：从平台自身下载（平台代缓存 GitHub 文件，可用 http，绕开 TLS 1.2 限制）。
        if ($PlatformURL -and -not $BinaryUrl) {
            $fallback = $PlatformURL.TrimEnd('/') + "/api/external/binary/windows-exporter-" +
                        $script:ResolvedArch
            Write-Warn "直连下载失败（$first），改用平台地址 $fallback"
            try {
                $wc2 = New-Object System.Net.WebClient
                $wc2.Headers.Add('User-Agent', 'upgrade-agent/windows_exporter-installer')
                $wc2.DownloadFile($fallback, $OutFile)
                return $OutFile
            } catch {
                $first = "$first；平台下载也失败：$($_.Exception.Message)"
            }
        }
        $msg = "下载失败：$first"
        if (-not $tls12Supported) {
            $msg += "`n该系统的 .NET/PowerShell 不支持 TLS 1.2（Windows Server 2008 R2 常见），" +
                    "且未能从平台地址获取安装包。" +
                    "请确认平台可访问 GitHub，或由平台下发 exe 后用 -BinaryPath <本地路径> 安装。"
        } else {
            $msg += "`n请确认目标机能访问 platform / proxy.xiaonuo.live / GitHub，并检查网络与代理设置。"
        }
        Fail $msg 1
    }
    if (-not (Test-Path $OutFile)) { Fail '下载后未找到文件' 1 }
    $len = (Get-Item $OutFile).Length
    if ($len -lt 1048576) {
        Remove-Item -Force $OutFile -ErrorAction SilentlyContinue
        Fail "下载文件过小（$len 字节），可能是错误页面或代理拦截；请检查 $url" 1
    }
    return $OutFile
}

function Write-DefaultConfig {
    if ($NoConfig) {
        Write-Step '跳过配置生成（-NoConfig）'
        return
    }
    if ($ConfigPath -and (Test-Path $ConfigPath)) {
        Write-Step "使用自定义配置 $ConfigPath"
        Copy-Item -Force $ConfigPath $ConfigFile
    } elseif (-not (Test-Path $ConfigFile)) {
        Write-Step "生成默认配置 $ConfigFile"
        $content = @"
# windows_exporter 配置（由 install.ps1 生成）
# 采集器与 textfile 目录可在此调整；修改后重启服务生效。
collectors:
  enabled: cpu,logical_disk,physical_disk,memory,os,hardware_info,net,service

collector:
  textfile:
    directories:
      - $TextfileDir
  service:
    # 只采集业务服务时可改为 include（正则），例如：
    # include: "myapp|myagent"
    # 需要屏蔽大量系统服务时使用 exclude；完整示例见
    # examples/windows_exporter/config.yaml
    include: "windows_exporter|upgrade-agent"
"@
        # 配置里含中文注释：用无 BOM 的 UTF-8 写入。
        # 不用 Set-Content -Encoding UTF8：PS 5 会写 BOM，YAML 解析器可能报错。
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($ConfigFile, $content, $utf8NoBom)
    } else {
        Write-Step "保留已有配置 $ConfigFile"
    }
}

function Ensure-Dirs {
    if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null }
    if (-not (Test-Path $TextfileDir)) { New-Item -ItemType Directory -Force -Path $TextfileDir | Out-Null }
}

function Install-FirewallRule {
    if ($NoFirewall) { return }
    Write-Step "创建防火墙入站规则（TCP $ListenPort）"
    & netsh advfirewall firewall delete rule name="$FirewallRule" 2>$null | Out-Null
    & netsh advfirewall firewall add rule name="$FirewallRule" dir=in action=allow protocol=TCP localport=$ListenPort | Out-Null
}

function Remove-FirewallRule {
    Write-Step "删除防火墙规则（如存在）"
    & netsh advfirewall firewall delete rule name="$FirewallRule" 2>$null | Out-Null
}

function Create-Service {
    # 使用 New-Service 而非 sc.exe create：binPath 含空格与引号时，
    # PowerShell 调用原生 exe 的转义规则很绕，New-Service 直接按原样写入。
    $argLine = '--config.file="' + $ConfigFile + '"' +
               ' --web.listen-address=:' + $ListenPort +
               ' --collector.textfile.directory="' + $TextfileDir + '"'
    $binPath = '"' + $ExePath + '" ' + $argLine

    Write-Step "删除同名旧服务（如存在）"
    Remove-ServiceQuietly

    Write-Step "注册服务 $ServiceName"
    try {
        New-Service -Name $ServiceName -BinaryPathName $binPath -DisplayName $DisplayName -StartupType Automatic -ErrorAction Stop | Out-Null
    } catch {
        Fail "创建服务失败：$($_.Exception.Message)`n请确认以管理员身份运行，且没有同名服务被占用。" 1
    }
    # 描述与失败重启策略：sc.exe 的参数都是简单 token，无引号问题。
    & sc.exe description $ServiceName "windows_exporter - Prometheus metrics exporter for Windows" | Out-Null
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null
}

function Remove-ServiceQuietly {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) { return }
    try { Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue } catch { }
    & sc.exe delete $ServiceName 2>$null | Out-Null
    # 服务删除后标记为 pending delete 时，CreateService 会短暂报错；最多等 5 秒。
    $deadline = (Get-Date).AddSeconds(5)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 500
    }
}

function Start-AndVerify {
    Write-Step "启动服务 $ServiceName"
    try {
        Start-Service -Name $ServiceName -ErrorAction Stop
    } catch {
        Write-Warn "Start-Service 失败：$($_.Exception.Message)"
    }
    if (-not (Wait-ServiceState 'Running' 30)) {
        return $false
    }
    # 端口连通性检查：服务 Running 但监听失败时也能尽早发现。
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        try {
            $client = New-Object System.Net.Sockets.TcpClient
            $client.Connect('127.0.0.1', $ListenPort)
            $client.Close()
            return $true
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    Write-Warn "服务已运行，但 127.0.0.1:$ListenPort 暂不可连接（可能仍在初始化）"
    return $true
}

function Get-ExporterVersion {
    if (-not (Test-Path $ExePath)) { return '' }
    try {
        $out = & $ExePath --version 2>&1
        return ($out | Out-String).Trim()
    } catch {
        return ''
    }
}

function Do-Install {
    $script:ResolvedArch = Resolve-Arch
    Write-Step "系统: $([Environment]::OSVersion.VersionString)；架构: $script:ResolvedArch；端口: $ListenPort"
    Ensure-Dirs
    Stop-ServiceIfRunning

    $tmp = Join-Path $env:TEMP "windows_exporter-download-$PID.exe"
    Download-Exporter $tmp
    if ((Get-ServiceState) -ne 'Missing' -and (Test-Path $ExePath)) {
        Copy-Item -Force $ExePath $ExeBak
    }
    Copy-Item -Force $tmp $ExePath
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue

    Write-DefaultConfig
    Create-Service
    Install-FirewallRule
    if (-not (Start-AndVerify)) {
        Write-Warn '服务启动失败，尝试回滚'
        if (Test-Path $ExeBak) {
            Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 2
            Copy-Item -Force $ExeBak $ExePath
            Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
        }
        Fail "服务未能启动。请查看事件日志：Get-EventLog -LogName Application -Source $ServiceName -Newest 20" 1
    }
    Write-Output ''
    Write-Output "安装完成：$ExePath"
    Write-Output "配置文件：$ConfigFile"
    Write-Output "指标地址：http://<主机IP>:$ListenPort/metrics"
}

function Do-Update {
    $script:ResolvedArch = Resolve-Arch
    if ((Get-ServiceState) -eq 'Missing') {
        Fail '未安装 windows_exporter 服务，请先执行 install' 1
    }
    Write-Step '备份当前版本'
    if (Test-Path $ExePath) { Copy-Item -Force $ExePath $ExeBak }

    $tmp = Join-Path $env:TEMP "windows_exporter-download-$PID.exe"
    Download-Exporter $tmp
    Stop-ServiceIfRunning
    Copy-Item -Force $tmp $ExePath
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue

    if (-not (Start-AndVerify)) {
        Write-Warn '新版本启动失败，回滚到备份'
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
        if (Test-Path $ExeBak) {
            Copy-Item -Force $ExeBak $ExePath
            Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
            if (Wait-ServiceState 'Running' 30) {
                Fail '更新失败，已回滚到旧版本（服务运行中）' 1
            }
        }
        Fail '更新失败，回滚后服务仍未运行，请查看事件日志' 1
    }
    Write-Output ''
    Write-Output '更新完成。'
}

function Do-Uninstall {
    if ((Get-ServiceState) -eq 'Missing' -and -not (Test-Path $ExePath)) {
        Write-Step '未安装 windows_exporter（无需卸载）'
        return
    }
    if (-not $Yes) {
        $answer = Read-Host "确定卸载 windows_exporter 并删除 $InstallDir 吗？输入 yes 继续"
        if ($answer -ne 'yes') {
            Write-Step '已取消'
            return
        }
    }
    Stop-ServiceIfRunning
    Write-Step "删除服务 $ServiceName"
    Remove-ServiceQuietly
    Remove-FirewallRule

    if ($KeepData) {
        Write-Step '保留配置与数据（-KeepData），仅删除二进制'
        Remove-Item -Force $ExePath -ErrorAction SilentlyContinue
        Remove-Item -Force $ExeBak -ErrorAction SilentlyContinue
    } else {
        Write-Step "删除安装目录 $InstallDir"
        if (Test-Path $InstallDir) { Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue }
        if (Test-Path $TextfileDir) { Remove-Item -Recurse -Force $TextfileDir -ErrorAction SilentlyContinue }
    }
    Write-Output ''
    Write-Output '卸载完成。'
}

function Do-Status {
    $state = Get-ServiceState
    Write-Output "服务:     $ServiceName（$state）"
    if ($state -ne 'Missing') {
        $svc = Get-Service -Name $ServiceName
        Write-Output "启动类型: $($svc.StartType)"
    }
    Write-Output "安装路径: $ExePath（存在: $(Test-Path $ExePath)）"
    Write-Output "配置文件: $ConfigFile（存在: $(Test-Path $ConfigFile)）"
    Write-Output "指标目录: $TextfileDir"
    Write-Output "监听端口: $ListenPort"
    $ver = Get-ExporterVersion
    if ($ver) { Write-Output "版本:     $ver" }
    if ($state -eq 'Running') {
        try {
            $req = [System.Net.WebRequest]::Create("http://127.0.0.1:$ListenPort/metrics")
            $req.Timeout = 3000
            $resp = $req.GetResponse()
            $resp.Close()
            Write-Output '端口检查: /metrics 可访问'
        } catch {
            Write-Output "端口检查: 不可访问（$($_.Exception.Message)）"
        }
    }
}

Write-Step "windows_exporter install.ps1: action=$Action port=$ListenPort dir=$InstallDir"
Write-Step "PowerShell $($PSVersionTable.PSVersion), CLR $($PSVersionTable.CLRVersion), admin=$((Test-Admin))"

try {
    switch ($Action) {
        'install' { Do-Install }
        'update' { Do-Update }
        'uninstall' { Do-Uninstall }
        'status' { Do-Status }
        default { Fail "未知动作: $Action" 2 }
    }
} catch {
    # PowerShell 2.0 在函数/管道内抛错时退出码可能仍为 0，这里显式兜底，
    # 保证平台能通过退出码判定失败。
    Fail ("未捕获异常: " + $_.Exception.Message + " @ " + $_.InvocationInfo.PositionMessage) 1
}

[Console]::Out.Flush()
[Environment]::Exit(0)
