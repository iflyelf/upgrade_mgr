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
    （下载使用 System.Net.HttpWebRequest，不依赖 Invoke-WebRequest / Expand-Archive）。
    Windows 10 / Server 2016 以下自动使用 legacy 通道，避免 Go 1.21+ 二进制无法启动。

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
    legacy 通道默认使用官方 v0.23.1；一般无需手动指定。

.PARAMETER Flavor
    auto（默认）/ latest / legacy。
    auto 在 Windows 10 / Server 2016 及以上使用 latest；在 Windows 7 / 8 / 8.1 /
    Server 2008 R2 / 2012 / 2012 R2 使用 legacy。
    legacy 固定使用官方 windows_exporter v0.23.1（Go 1.20 构建，最后支持旧系统的版本），
    对应平台外部文件 windows-exporter-<arch>-legacy。

.PARAMETER ListenPort
    监听端口，默认 16666。

.PARAMETER TextfileDir
    textfile 采集器目录，默认 C:\monitor\windows_exporter\textfile_inputs。

.PARAMETER InstallDir
    安装目录，默认 C:\Program Files\windows_exporter。

.PARAMETER BinaryPath
    本地已存在的 exe 路径（跳过下载）。老系统（Server 2008 R2 + PowerShell 2.0/.NET 2.0）
    不支持 TLS 1.2，无法直连 GitHub；推荐由平台先把 exe 下发到本机，再用本参数安装。
    legacy 默认配置启用 hardware_info，因此必须使用本仓库的 *-legacy 资产；
    官方 v0.23.1 不含该采集器，请配合 -NoConfig 自备配置。

.PARAMETER BinaryUrl
    完整 exe 下载地址（可用 http）。也可把 exe 放到内网 HTTP 地址，用本参数直接下载。
    legacy 通道需指向本仓库 *-legacy 资产（含 hardware_info）。

.PARAMETER ConfigPath
    自定义配置文件路径；指定后安装/更新时复制为安装目录下的 config.yaml。

.PARAMETER NoConfig
    不生成/覆盖 config.yaml（使用已有配置）。

.PARAMETER ServiceAccount
    可选：服务运行账户（如 NT AUTHORITY\LocalService 或 DOMAIN\svc_exporter）。
    留空使用 LocalSystem。使用低权限账户前，需先在「本地安全策略 → 用户权限分配 →
    作为服务登录」中授予该账户权限，否则服务无法启动。

.PARAMETER ServicePassword
    可选：服务账户密码；托管服务账户（gMSA）可留空。

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

    [ValidateSet('auto', 'latest', 'legacy')]
    [string]$Flavor = 'auto',
    [int]$ListenPort = 16666,
    [string]$TextfileDir = 'C:\monitor\windows_exporter\textfile_inputs',
    [string]$InstallDir = '',
    [string]$BinaryPath = '',
    [string]$BinaryUrl = '',
    [string]$ConfigPath = '',
    [switch]$NoConfig,
    [string]$ServiceAccount = '',
    [string]$ServicePassword = '',
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
if ($env:WINDOWS_EXPORTER_FLAVOR) { $Flavor = $env:WINDOWS_EXPORTER_FLAVOR }
# 最后一个用 Go 1.20 构建、支持 Windows 7 / Server 2008 R2 的官方版本。
$LegacyVersion = '0.23.1'
# 默认配置模板版本：模板内容变更时递增，旧脚本生成的配置会自动备份并升级。
$ConfigTemplateVersion = '2'
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

function Resolve-Flavor {
    $f = $Flavor
    if ($env:WINDOWS_EXPORTER_FLAVOR) { $f = $env:WINDOWS_EXPORTER_FLAVOR }
    $f = ($f + '').Trim().ToLower()
    if ($f -eq 'latest' -or $f -eq 'legacy') { return $f }
    if ($f -ne '' -and $f -ne 'auto') {
        Fail "不支持的 Flavor: $f（可选 auto / latest / legacy）" 2
    }
    # Go 1.21+ 的二进制不再支持 Windows 7 / 8 / 8.1 / Server 2008 R2 / 2012 / 2012 R2。
    # Windows 10 / Server 2016 起主版本号为 10。
    if ([Environment]::OSVersion.Version.Major -lt 10) { return 'legacy' }
    return 'latest'
}

function Initialize-Exporter {
    $script:ExporterFlavor = Resolve-Flavor
    $script:AssetName = "windows-exporter-$($script:ResolvedArch)"
    if ($script:ExporterFlavor -eq 'legacy') {
        $script:AssetName = "$($script:AssetName)-legacy"
        $script:ReleaseVersion = $LegacyVersion
        if ($Version -and $Version -ne 'latest') {
            $requested = $Version.TrimStart('v')
            if ($requested -ne $LegacyVersion) {
                Fail "legacy 通道只提供 v$LegacyVersion（Go 1.20 / 含 hardware_info）；不支持 -Version $Version" 2
            }
            $script:ReleaseVersion = $requested
        }
    } else {
        $script:ReleaseVersion = $Version
        if (-not $script:ReleaseVersion) { $script:ReleaseVersion = 'latest' }
        if ($Version -ne 'latest') {
            Fail 'latest 通道只对应 iflyelf/windows_exporter 的 latest 发布；旧系统/历史版本请用 -Flavor legacy' 2
        }
    }
    # 平台外部文件名称可覆盖（例如私有镜像使用不同命名）。
    if ($env:WINDOWS_EXPORTER_PLATFORM_ASSET) {
        $script:AssetName = $env:WINDOWS_EXPORTER_PLATFORM_ASSET.Trim()
    }
    $osv = [Environment]::OSVersion.Version
    Write-Step "下载通道: $($script:ExporterFlavor)（OS $($osv.Major).$($osv.Minor) build $($osv.Build)，平台资产 $($script:AssetName)）"
    if ($script:ExporterFlavor -eq 'legacy') {
        Write-Step "旧系统通道使用 windows_exporter v$($script:ReleaseVersion)（Go 1.20，支持 Server 2008 R2 ~ 2012 R2）"
    }
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

function Test-DownloadedFile([string]$Path) {
    if (-not (Test-Path $Path)) { return $false }
    $len = (Get-Item $Path).Length
    if ($len -lt 1048576) {
        Remove-Item -Force $Path -ErrorAction SilentlyContinue
        return $false
    }
    return $true
}

function Save-UrlToFile([string]$Url, [string]$OutFile, [int]$TimeoutMs) {
    # 使用 HttpWebRequest 显式设置超时（WebClient 默认约 100 秒，平台代下载可能更久）。
    $req = [System.Net.WebRequest]::Create($Url)
    $req.Method = 'GET'
    $req.UserAgent = 'upgrade-agent/windows_exporter-installer'
    if ($req -is [System.Net.HttpWebRequest]) {
        $req.Timeout = $TimeoutMs
        $req.ReadWriteTimeout = $TimeoutMs
    }
    $resp = $null
    $stream = $null
    $fs = $null
    try {
        $resp = $req.GetResponse()
        $stream = $resp.GetResponseStream()
        $fs = [System.IO.File]::Create($OutFile)
        # PS 2.0 / CLR 2.0 没有 Stream.CopyTo，手动分块复制。
        $buffer = New-Object byte[] 81920
        while (($n = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
            $fs.Write($buffer, 0, $n)
        }
    } finally {
        if ($fs) { $fs.Close() }
        if ($stream) { $stream.Close() }
        if ($resp) { $resp.Close() }
    }
}

function Download-Exporter([string]$OutFile) {
    if ($BinaryPath) {
        if (-not (Test-Path $BinaryPath)) {
            Fail "指定的本地安装包不存在: $BinaryPath" 1
        }
        Write-Step "使用本地安装包 $BinaryPath"
        Copy-Item -Force $BinaryPath $OutFile
        if (-not (Test-DownloadedFile $OutFile)) {
            Fail "本地安装包过小或不可读，可能不是 windows_exporter.exe: $BinaryPath" 1
        }
        return
    }

    $arch = $script:ResolvedArch
    if ($BinaryUrl) {
        $primary = $BinaryUrl
    } elseif ($script:BaseUrl) {
        # 自定义内网镜像：默认按对应通道的官方文件名拼接，可用环境变量覆盖文件名。
        $assetFile = "windows_exporter-$arch.exe"
        if ($script:ExporterFlavor -eq 'legacy') {
            $assetFile = "windows_exporter-$arch-legacy.exe"
        }
        if ($env:WINDOWS_EXPORTER_ASSET_FILE) {
            $assetFile = $env:WINDOWS_EXPORTER_ASSET_FILE.Trim()
        }
        $primary = "$($script:BaseUrl)/$assetFile"
    } elseif ($script:ExporterFlavor -eq 'legacy') {
        # 必须使用本仓库 legacy 资产：官方 v0.23.1 不含 hardware_info，
        # 而默认配置会启用它。平台资产不可用时仍可 -BinaryPath/-BinaryUrl 指定。
        $base = "https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-$arch-legacy.exe"
        $primary = "$ProxyPrefix$base"
    } else {
        $base = "https://github.com/iflyelf/windows_exporter/releases/download/$Version/windows_exporter-$arch.exe"
        $primary = "$ProxyPrefix$base"
    }
    $platform = ''
    if ($PlatformURL -and -not $BinaryUrl) {
        $platform = $PlatformURL.TrimEnd('/') + "/api/external/binary/" + $script:AssetName
    }
    $platformLocal = ''
    if ($platform) { $platformLocal = $platform + '?local=1' }

    $attempts = @()
    if ($BinaryUrl) {
        $attempts += @{ Label = '指定地址'; Url = $primary; Timeout = 600000 }
    } elseif ($script:BaseUrl) {
        # 自定义内网镜像：优先使用，通常为 HTTP。
        $attempts += @{ Label = '内网镜像'; Url = $primary; Timeout = 600000 }
        if ($platformLocal) {
            $attempts += @{ Label = '平台离线缓存'; Url = $platformLocal; Timeout = 30000 }
        }
    } else {
        if ($platformLocal) {
            # local=1 只读平台本地/缓存，不会触发平台现场下载，可快速失败。
            $attempts += @{ Label = '平台离线缓存'; Url = $platformLocal; Timeout = 30000 }
        }
        if ($tls12Supported) {
            $attempts += @{ Label = '直连/代理'; Url = $primary; Timeout = 600000 }
            if ($platform) {
                $attempts += @{ Label = '平台代下载'; Url = $platform; Timeout = 600000 }
            }
        }
    }

    $errors = @()
    foreach ($a in $attempts) {
        Write-Step "下载（$($a.Label)）$($a.Url)"
        if (Test-Path $OutFile) { Remove-Item -Force $OutFile -ErrorAction SilentlyContinue }
        try {
            Save-UrlToFile $a.Url $OutFile $a.Timeout
            if (-not (Test-DownloadedFile $OutFile)) {
                throw '下载文件不存在或过小（可能是错误页面）'
            }
            return
        } catch {
            $errors += ("{0}: {1}" -f $a.Label, $_.Exception.Message)
            Write-Warn "$($a.Label) 失败：$($_.Exception.Message)"
        }
    }

    Remove-Item -Force $OutFile -ErrorAction SilentlyContinue
    $msg = "下载失败：`n" + ($errors -join "`n")
    $asset = $script:AssetName
    if ($script:ExporterFlavor -eq 'legacy') {
        $pkg = "windows_exporter-$arch-legacy.exe"
        $msg += "`n旧系统需要使用 Go 1.20 构建的 windows_exporter v$($script:ReleaseVersion)。" +
                "请在控制台「外部文件」为 $asset 上传离线文件（$pkg），上传后平台直接返回本地文件。" +
                "若平台本身可出网，也可先点该行「刷新缓存」让平台预下载后再重试。"
    } else {
        $pkg = "windows_exporter-$arch.exe"
        $msg += "`n请在控制台「外部文件」为 $asset 上传离线文件（$pkg），" +
                "或改用 -BinaryPath / -BinaryUrl。"
    }
    if (-not $tls12Supported -and $PlatformURL) {
        $msg += "`n当前 .NET 不支持 TLS 1.2，脚本只尝试平台本地/缓存；不会等待平台现场下载。"
    }
    Fail $msg 1
}

function Get-DefaultConfigContent {
    if ($script:ExporterFlavor -eq 'legacy') {
        return @"
# windows_exporter 配置（由 install.ps1 生成）
# config-version: 2
# flavor: legacy（windows_exporter v0.23.1 / Go 1.20，支持 Server 2008 R2 ~ 2012 R2）
# 注意：旧系统通道不支持 physical_disk；hardware_info 默认启用（本仓库 legacy 资产）。
collectors:
  enabled: cpu,cs,logical_disk,memory,net,os,service,system,textfile,hardware_info

collector:
  service:
    services-where: "Name='windows_exporter' OR Name='upgrade-agent'"
"@
    }
    return @"
# windows_exporter 配置（由 install.ps1 生成）
# config-version: 2
# flavor: latest（windows_exporter latest，支持 Windows 10 / Server 2016+）
# 采集器与 textfile 目录可在此调整；修改后重启服务生效。
collectors:
  enabled: cpu,logical_disk,physical_disk,memory,os,hardware_info,net,service,textfile

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
}

function Write-DefaultConfig {
    if ($NoConfig) {
        Write-Step '跳过配置生成（-NoConfig）'
        return
    }
    if ($ConfigPath -and (Test-Path $ConfigPath)) {
        Write-Step "使用自定义配置 $ConfigPath"
        Write-Warn "自定义配置需兼容当前下载通道 $($script:ExporterFlavor)，不兼容会导致服务启动失败"
        Copy-Item -Force $ConfigPath $ConfigFile
        return
    }
    $needWrite = -not (Test-Path $ConfigFile)
    $existing = ''
    if (-not $needWrite) {
        try { $existing = [System.IO.File]::ReadAllText($ConfigFile) } catch { $existing = '' }
        if ($existing.Trim() -eq '') {
            # 空文件（0 字节或只有空白）不是有效配置，按缺失处理重新生成，
            # 否则历史上一次写入失败留下的空文件会被永久“保留”。
            $needWrite = $true
            Write-Warn "现有配置 $ConfigFile 为空，将重新生成"
        } elseif ($existing -match '# windows_exporter 配置（由 install\.ps1 生成）') {
            $flavorMatches = $existing -match ('# flavor:\s*' + [regex]::Escape($script:ExporterFlavor))
            $templateMatches = $existing -match ('# config-version:\s*' + [regex]::Escape($ConfigTemplateVersion))
            if (-not $flavorMatches -or -not $templateMatches) {
                $needWrite = $true
                Write-Warn "现有配置由旧模板/其它下载通道生成，将备份为 $ConfigFile.bak 后重新生成"
            }
        }
    }
    if (-not $needWrite) {
        Write-Step "保留已有配置 $ConfigFile"
        return
    }
    if (Test-Path $ConfigFile) {
        Copy-Item -Force $ConfigFile "$ConfigFile.bak"
    } else {
        Write-Step "生成默认配置 $ConfigFile"
    }
    # 配置里含中文注释：用无 BOM 的 UTF-8 写入。
    # WriteAllText 在 PS 2.0 某些环境下可能静默失败，改用流式写入并检查。
    try {
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        $content = Get-DefaultConfigContent
        [System.IO.File]::WriteAllText($ConfigFile, $content, $utf8NoBom)
        # 验证写入是否成功
        if (-not (Test-Path $ConfigFile)) {
            throw "文件未生成"
        }
        $size = (Get-Item $ConfigFile).Length
        if ($size -eq 0) {
            throw "文件为空（0 字节）"
        }
        Write-Step "配置已按 $($script:ExporterFlavor) 通道生成（$size 字节）"
    } catch {
        Write-Warn "WriteAllText 失败（$_），尝试 StreamWriter"
        $sw = $null
        try {
            $sw = New-Object System.IO.StreamWriter($ConfigFile, $false, (New-Object System.Text.UTF8Encoding($false)))
            $sw.Write((Get-DefaultConfigContent))
            $sw.Close()
            Write-Step "配置已通过 StreamWriter 生成"
        } catch {
            if ($sw -ne $null) { $sw.Close() }
            Fail "无法写入配置文件 $ConfigFile`: $_" 1
        }
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

function Get-ExporterArgs {
    # 两个通道的 textfile 参数名不同：
    #   v0.23.1（旧系统）：--collector.textfile.directory
    #   latest（Go 1.27） ：--collector.textfile.directories
    $textfileFlag = '--collector.textfile.directories'
    if ($script:ExporterFlavor -eq 'legacy') {
        $textfileFlag = '--collector.textfile.directory'
    }
    return '--config.file="' + $ConfigFile + '"' +
           ' --web.listen-address=:' + $ListenPort +
           ' ' + $textfileFlag + '="' + $TextfileDir + '"'
}

function Create-Service {
    # 使用 New-Service 而非 sc.exe create：binPath 含空格与引号时，
    # PowerShell 调用原生 exe 的转义规则很绕，New-Service 直接按原样写入。
    $argLine = Get-ExporterArgs
    $binPath = '"' + $ExePath + '" ' + $argLine

    Write-Step "删除同名旧服务（如存在）"
    Remove-ServiceQuietly

    Write-Step "注册服务 $ServiceName"
    try {
        New-Service -Name $ServiceName -BinaryPathName $binPath -DisplayName $DisplayName -StartupType Automatic -ErrorAction Stop | Out-Null
    } catch {
        Fail "创建服务失败：$($_.Exception.Message)`n请确认以管理员身份运行，且没有同名服务被占用。" 1
    }
    # 可选低权限服务账户（需预先授予「作为服务登录」权限）。
    if ($ServiceAccount -ne '') {
        Write-Step "配置服务运行账户：$ServiceAccount"
        if ($ServicePassword -ne '') {
            & sc.exe config $ServiceName obj= "$ServiceAccount" password= "$ServicePassword" | Out-Null
        } else {
            # gMSA / LocalService 等无需密码
            & sc.exe config $ServiceName obj= "$ServiceAccount" | Out-Null
        }
        if ($LASTEXITCODE -ne 0) {
            Fail "配置服务账户失败（请确认账户已授予“作为服务登录”权限，密码正确）。" 1
        }
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

function Get-ServiceBinPath {
    try {
        $svc = Get-WmiObject -Class Win32_Service -Filter "Name='$ServiceName'" -ErrorAction Stop
        if ($svc) { return $svc.PathName }
    } catch { }
    return ''
}

function Set-ServiceBinPath([string]$BinPath) {
    if (-not $BinPath) { return }
    try {
        $svc = Get-WmiObject -Class Win32_Service -Filter "Name='$ServiceName'" -ErrorAction Stop
        if (-not $svc) { return }
        # Win32_Service.Change 只改 PathName，其余传 $null 表示保持不变（含服务账户）。
        $r = $svc.Change($null, $BinPath, $null, $null, $null, $null, $null, $null, $null, $null, $null)
        if ($r.ReturnValue -ne 0) {
            Write-Warn "更新服务启动参数失败（WMI 返回 $($r.ReturnValue)）"
        }
    } catch {
        Write-Warn "更新服务启动参数失败：$($_.Exception.Message)"
    }
}

function Update-ServiceArgs {
    $binPath = '"' + $ExePath + '" ' + (Get-ExporterArgs)
    Write-Step '更新服务启动参数（保留原服务账户）'
    Set-ServiceBinPath $binPath
}

function Show-ServiceFailureDetails {
    Write-Warn '==== 服务启动诊断开始 ===='
    try {
        $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($svc) { Write-Warn "服务状态: $($svc.Status)" } else { Write-Warn '服务不存在' }
    } catch { }
    try {
        & sc.exe qc $ServiceName 2>&1 | ForEach-Object { Write-Warn "sc qc: $_" }
    } catch { }
    try {
        $events = Get-EventLog -LogName Application -Newest 100 -ErrorAction Stop |
            Where-Object {
                $_.Source -eq 'Service Control Manager' -or
                $_.Source -eq $ServiceName -or
                $_.Message -match 'windows_exporter'
            } |
            Select-Object -First 5
        if (-not $events) {
            Write-Warn '最近事件日志中没有 windows_exporter / 服务控制管理器记录'
        } else {
            foreach ($e in $events) {
                $text = ($e.Message -replace '\s+', ' ').Trim()
                Write-Warn ("事件[{0}] {1}: {2}" -f $e.TimeGenerated, $e.Source, $text)
            }
        }
    } catch {
        Write-Warn "读取事件日志失败：$($_.Exception.Message)"
    }
    try {
        $ver = & $ExePath --version 2>&1
        Write-Warn ("exe --version: {0}（退出码 {1}）" -f (($ver | Out-String).Trim()), $LASTEXITCODE)
    } catch {
        Write-Warn "exe 无法执行（常见于二进制与系统不兼容）：$($_.Exception.Message)"
    }
    $outFile = Join-Path $env:TEMP "windows_exporter-fg-$PID.out"
    $errFile = Join-Path $env:TEMP "windows_exporter-fg-$PID.err"
    try {
        $fgArgs = Get-ExporterArgs
        $p = Start-Process -FilePath $ExePath -ArgumentList $fgArgs -PassThru -NoNewWindow `
             -RedirectStandardOutput $outFile -RedirectStandardError $errFile -ErrorAction Stop
        if ($p.WaitForExit(5000)) {
            Write-Warn "前台运行退出码: $($p.ExitCode)"
        } else {
            Write-Warn '前台运行 5 秒未退出（二进制可启动；服务失败可能与服务账户/权限有关）'
            try { $p.Kill() } catch { }
        }
        Start-Sleep -Milliseconds 300
        foreach ($f in @($outFile, $errFile)) {
            if (Test-Path $f) {
                $lines = @(Get-Content -LiteralPath $f -ErrorAction SilentlyContinue | Select-Object -Last 30)
                foreach ($line in $lines) {
                    if ($line -ne '') { Write-Warn $line }
                }
                Remove-Item -Force $f -ErrorAction SilentlyContinue
            }
        }
    } catch {
        Write-Warn "前台运行失败：$($_.Exception.Message)"
    }
    Write-Warn '==== 服务启动诊断结束 ===='
}

function Do-Install {
    $script:ResolvedArch = Resolve-Arch
    Initialize-Exporter
    $script:PrevBinPath = Get-ServiceBinPath
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

    $configBackupInstall = "$ConfigFile.install-bak"
    if (Test-Path $ConfigFile) { Copy-Item -Force $ConfigFile $configBackupInstall }
    Write-DefaultConfig
    Create-Service
    Install-FirewallRule
    if (-not (Start-AndVerify)) {
        Write-Warn '服务启动失败，收集诊断信息（事件日志 / 前台运行输出）'
        Show-ServiceFailureDetails
        Write-Warn '尝试回滚'
        if (Test-Path $configBackupInstall) {
            Copy-Item -Force $configBackupInstall $ConfigFile
        }
        if (Test-Path $ExeBak) {
            Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 2
            Copy-Item -Force $ExeBak $ExePath
            Set-ServiceBinPath $script:PrevBinPath
            Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
        }
        Fail "服务未能启动。请根据上方诊断处理；可手动在前台运行：`"$ExePath`" $(Get-ExporterArgs)" 1
    }
    Remove-Item -Force $configBackupInstall -ErrorAction SilentlyContinue
    Write-Output ''
    Write-Output "安装完成：$ExePath"
    Write-Output "配置文件：$ConfigFile"
    Write-Output "指标地址：http://<主机IP>:$ListenPort/metrics"
}

function Do-Update {
    $script:ResolvedArch = Resolve-Arch
    Initialize-Exporter
    if ((Get-ServiceState) -eq 'Missing') {
        Fail '未安装 windows_exporter 服务，请先执行 install' 1
    }
    $script:PrevBinPath = Get-ServiceBinPath
    $configBackup = "$ConfigFile.update-bak"
    if (Test-Path $ConfigFile) { Copy-Item -Force $ConfigFile $configBackup }
    Write-Step '备份当前版本'
    if (Test-Path $ExePath) { Copy-Item -Force $ExePath $ExeBak }

    $tmp = Join-Path $env:TEMP "windows_exporter-download-$PID.exe"
    Download-Exporter $tmp
    Stop-ServiceIfRunning
    Copy-Item -Force $tmp $ExePath
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue

    Write-DefaultConfig
    Update-ServiceArgs
    if (-not (Start-AndVerify)) {
        Write-Warn '新版本启动失败，收集诊断信息'
        Show-ServiceFailureDetails
        Write-Warn '回滚到备份'
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
        if (Test-Path $ExeBak) {
            Copy-Item -Force $ExeBak $ExePath
        }
        if (Test-Path $configBackup) {
            Copy-Item -Force $configBackup $ConfigFile
            Remove-Item -Force $configBackup -ErrorAction SilentlyContinue
        }
        Set-ServiceBinPath $script:PrevBinPath
        Start-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if (Wait-ServiceState 'Running' 30) {
            Fail '更新失败，已回滚到旧版本（服务运行中），诊断信息见上' 1
        }
        Fail '更新失败，回滚后服务仍未运行，诊断信息见上' 1
    }
    Remove-Item -Force $configBackup -ErrorAction SilentlyContinue
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
