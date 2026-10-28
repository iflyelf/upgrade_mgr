# upgrade_mgr — 升级控制平台

用于各类 Agent、采集器、插件（均为 `CGO_ENABLED=0` 交叉编译的 Go 静态二进制）的**灰度升级控制平台**。控制台 `upgrade-mgr` + 被控端 `upgrade-agent`，支持按 IP 段灰度、维护窗口、自动回滚。

> 独立运行：**没有 consul_mgr 也能启动**；联合使用时共享同一 Consul 与 FlyIAM。数据库为独立库 `upgrade_mgr`。

---

## 特性

**P0 骨架**
- 独立启动、可选依赖优雅降级（Consul / Casdoor / Redis 不可用仅告警，不阻塞）
- 主机清单本地化（事实来源为 `hosts` 表），Consul 为可选同步来源（含业务分组 account/app_code/group…）
- Consul 同步自动清理已移除的纯 Consul 主机（有 Agent/未完成任务时保留；部分或空清单自动跳过）
- 可切换认证：内置 bcrypt 账号（默认）/ FlyIAM-Casdoor SSO
- 页面设置化（DB 优先 / env 兜底）；多数配置即时生效，启动期绑定项标记为需重启
- IP 段表达式解析：`10.1.255.24-26,10.1.255.38,10.1.255.0/24,10.1.26.5-10.1.26.7`（含排除、展开上限）
- 直接 exec 命令执行器（无 shell，防注入）；HMAC 指令签名；sd_notify 僵死检测（供 Agent 保活）
- 三主题（🌞暖沙米/🌊冷蓝/🌙暗黑）+ Mac 圆角 + H5 自适应，前端 `go:embed` 内嵌单二进制
- **顶栏菜单布局**（对齐 consul_mgr）：一级菜单 + 下拉（人员组织/运维/帮助），
  窄屏自动切换为横向页签；「接入主机示例」在主机页顶部与「帮助」菜单同时提供
- **Redis 页面缓存**：支持单点 / 集群 / 哨兵，页面可配置、热加载、失败自动降级；
  只读 API 命中缓存返回 `X-Cache: HIT`，写操作自动失效
- **Linux / Windows 双示例快速上手（中英双语）**：页面内可切换系统与语言，
  覆盖主机接入 → 组件 → 制品 → 任务；文案由 `web/quickstart.yaml` 统一生成

**P2 任务与灰度**
- 组件/制品完整 CRUD（含 md5/sha256/ELF 校验、Range 下载）、任务模型（IP 段展开+分组过滤+批次划分）、长轮询原子取件（FOR UPDATE SKIP LOCKED）+ HMAC 签名、维护租约、ack/stage/report 闭环、**端到端单机升级验证通过**、组件/制品/任务前端页

**P3 灰度编排与 Agent 增强**
- **灰度编排器**：后台循环扫描 running 任务，按批次推进；批间健康门禁（失败率阈值暂停）、人工推进（promote API）、自动推进（auto_promote）、自动回滚标记
- **Agent 指令验签**：HMAC 签名 + 过期时间校验（防伪造/重放），拒签指令 ack=rejected
- **全 Go 安装器**：`upgrade-agent install --server URL`，探测 init（systemd/upstart）→ 写服务模板（go:embed）→ 启用启动 → 可选 cron 看门狗兜底
- **保活 watchdog**：`upgrade-agent watchdog`（由 cron 每分钟调用），flock 单实例 + 读 state 心跳新鲜度 → 超阈值拉起
- CI（GitHub Actions 多架构交叉编译发布 amd64/arm64/arm）

**P4 自升级与生产完善**
- **Agent 自升级**：服务端按二进制 SHA256 判断旧 Agent → 下发 `self_update` → 下载校验 → 备份 → 替换 → exit(75) 触发重启；新版本心跳按目标哈希自动确认成功；watchdog 检测连续崩溃（≥3次）自动回滚旧版本
- **无需逐台登录**：管理员可在主机页一键升级全部旧 Agent，或开启 `agent.auto_update` 在心跳/注册时自动下发；旧 Agent 的签名路径不匹配会被自动探测并重试，无需手工修改 `agents.signature_modes`
- **Consul Meta 回写**：升级成功后回写服务实例 Meta（`upgraded_version`），供 consul_mgr 展示最新版本
- **enroll-token 生成**：管理员生成一次性注册令牌 + 一键安装命令（`GET /api/agent/enroll-token`）；另支持页面生成永久/自定义到期、可注销的自动化注册令牌
- **主机升级历史**：`GET /api/hosts/{ip}/upgrade-history`，展示逐机升级记录
- **主机管理**：服务端分页（几万台主机不一次性加载）、编辑业务字段、删除主机（未完成任务保护 + Agent 凭据撤销）

**P5 企业级增强**
- **Prometheus 指标**：`/metrics` 端点暴露关键指标（Agent 在线数/版本分布/心跳延迟/任务进度/失败率/指令执行时间/外部文件可用性），支持 Grafana 监控大盘
- **多租户 RBAC**：hosts/components/tasks/roles/teams/service groups/组织架构/审计日志均带 `tenant_id`，
  租户隔离查询，支持 admin/user/viewer 角色权限控制
- **分布式编排器**：基于 PostgreSQL advisory lock，多控制台实例安全并行（每轮仅一个副本推进批次，自动故障转移）；单机与多副本共用同一逻辑，不依赖 Consul
- **制品外部存储**：抽象 Backend 接口，支持本地/S3(MinIO)/阿里云 OSS 可切换，预签名 URL 下载
- **制品并行上传与一致性**：组件共享锁 + 制品身份独占锁，不同版本/架构/OS 可并行上传；删除/清空组件持独占锁，等待在途上传并阻止新写入；PostgreSQL advisory lock 保证多副本互斥
- **运行时配置热更新**：`artifact.max_concurrent_uploads` / `artifact.lock_wait` 页面保存后当前副本立即生效，其他副本约 5 秒内自动同步；连接池按“并发上传数 + 4”自动抬高
- **安全加固**：写请求 CSRF Origin/Referer 校验、CSP/HSTS/no-store 安全头、Redis 缓存仅成功写后失效、Agent 注册令牌事务化消费与租户绑定、RBAC 防提权、用户扩展字段跨租户隔离、主机字段定义写操作收紧、本地上传临时文件 + fsync + 原子 rename

**P6 数据库自动化**
- **自动创建**：首次启动自动创建所有表结构，无需手动执行 SQL
- **版本化迁移**：使用 go:embed 内嵌迁移脚本（001_init, 002_multi_tenant, ...），自动检测并应用未执行的迁移
- **自动维护**：每日凌晨 2 点自动清理过期数据（7 天过期指令/30 天旧指令/90 天旧任务/可配置审计保留期）、VACUUM ANALYZE、索引重建
- **零停机更新**：迁移在事务内执行，失败自动回滚，支持幂等重试

---

## 技术栈

Go 1.26（`CGO_ENABLED=0`，交叉编译 linux/amd64、arm64、arm）· 标准库 net/http（Go 1.22 路由）· PostgreSQL(`lib/pq`) · hashicorp/consul/api · redis/go-redis v9（单点/集群/哨兵）· Prometheus · 前端原生内嵌（后续可替换为 Vue3 构建产物）

Agent 构建矩阵：Linux 普通构建（Go 1.26）、Linux legacy（Go 1.23，旧内核 kernel 2.6.32 等）、
Windows（Go 1.20，Server 2008 R2 ~ Windows 11，amd64/arm64，依赖 `golang.org/x/sys`）。

---

## 快速开始

```bash
# 必填数据库；其余可选
export DATABASE_URL="postgresql://user:pass@host:5432/upgrade_mgr?sslmode=disable"
export ADMIN_USERNAME=admin ADMIN_PASSWORD=your-strong-password
# 生产建议显式设置；留空时程序会生成一次并持久化到数据库，供多副本/重启共享
export SESSION_SECRET="至少16字节的会话密钥"

make build
./bin/upgrade-mgr -c deploy/config/config.yaml
# 访问 http://localhost:8090
```

> 启动后打开页面，按顶部「📘 快速上手」完成 Linux / Windows 主机接入与首个任务；
> 也可直接看下方《快速上手（Linux / Windows） / Quick Start》中英对照。

启用 Consul 同步（可选，或在设置页配置；`datacenter` 留空自动探测）：
```bash
export CONSUL_ENABLED=true CONSUL_ADDRESS=http://10.0.56.118:8500 \
       CONSUL_TOKEN=xxx CONSUL_SYNC_INTERVAL=5m
```

---

<!-- QUICKSTART:START -->
<!-- 本段由 web/quickstart.yaml 生成；请勿手工编辑，修改后运行 `make docs-quickstart`。 -->

## 快速上手（Linux / Windows）

> 约定：
> - 控制台的主机/组件/制品/任务页顶部都有「📘 快速上手」，可在 Linux / Windows 之间切换；下列命令与页面一致。
> - `<控制台>`：实际访问地址，如 http://10.0.88.88:8090 或 https://upgrade.example.com。
> - `<令牌>`：在「主机 → 接入主机 → 生成接入命令」生成的一次性注册令牌（默认 15 分钟有效）。
> - Windows 主机需先把 upgrade-agent-windows-{amd64,arm64}.exe 放到控制台 dist（Release 同时提供 .zip；Docker 需挂载），否则下载会 404。

### Linux（systemd / SysV）

**1. 主机 接入 Linux Agent**

- 在「主机」页选择 Linux 与架构（amd64 / arm64 / arm；旧内核如 CentOS 6 使用 legacy 包），点「生成接入命令」；令牌一次性、默认 15 分钟有效。
- 到目标机用 root 执行：Agent 注册为 systemd 服务（无 systemd 自动回退 SysV），主机随即出现在列表。
- 主机名、OS、ARCH、出口 IP 由 Agent 上报；多网卡主机按实际出口地址匹配任务。
- 「同步 Consul」也能导入主机，但未安装 Agent 的主机下发任务时会被跳过。

```bash
# 控制台会生成完整的一键命令；手工执行等价于：
curl -fsSL -o /tmp/upgrade-agent 'http://<控制台>/api/agent/binary/linux-amd64'
chmod +x /tmp/upgrade-agent
sudo /tmp/upgrade-agent install \
  --server 'http://<控制台>' \
  --enroll-token '<令牌>'

sudo /tmp/upgrade-agent status     # 查看注册/连接状态
sudo /tmp/upgrade-agent diagnose   # 诊断到控制台的链路
```

**2. 组件 选择 Linux 示例**

- 组件页顶部「Linux 示例」提供：只执行命令（安装 / 更新 / 卸载）、脚本（安装 / 更新 / 卸载）、二进制替换。
- 推荐先用「脚本安装」：script_url 指向仓库 examples/node_exporter/install.sh（经 proxy 代理），不需要上传制品。
- install.sh 支持 install / update / uninstall / status：update 备份旧二进制并在启动失败时回滚，uninstall 传 -y 跳过确认。
- 脚本自动识别 systemd / SysV：CentOS/RHEL 7+、Rocky、openEuler、Ubuntu 16.04+、Debian 8+、SLES 12+ 写 systemd；旧内核系统如 CentOS 6、Ubuntu 14.04、Debian 7、SLES 11 写 /etc/init.d。
- 「只执行命令」用 exec + script_url + exec_args（install / update / uninstall -y），不下载制品，适合最小化安装。
- 「二进制替换」用于需要平台备份/回滚的场景：服务注册放在 post_install 脚本里，由平台备份 install_path 下的旧文件。
- 高级 JSON 的 exec_args / exec_env / exec_timeout / work_dir 控制脚本执行；例如用 exec_env 指定 NODE_EXPORTER_VERSION=1.8.2。
- agent.allow_exec_mode 默认开启；如需禁止脚本/exec 模式，可在「设置 → Agent」关闭（关闭后命令任务也会禁用）。

```bash
# 仓库自带单文件安装脚本（内嵌 systemd unit 与 SysV init）
ls -l examples/node_exporter/install.sh

# 可选：先在目标机手动试跑（需要 root）
sudo bash examples/node_exporter/install.sh install
sudo bash examples/node_exporter/install.sh status
sudo bash examples/node_exporter/install.sh update
sudo bash examples/node_exporter/install.sh uninstall -y
```

**3. 制品 上传安装脚本（可选）**

- 脚本示例已填真实 script_url，通常可跳过本页。
- 不想依赖外链时：删除组件高级 JSON 中的 script_url，在制品页上传 examples/node_exporter/install.sh（版本 latest，架构 all）。
- 平台不会解压 tar.gz；请上传可直接执行的脚本或解压后的二进制。
- 如果上传二进制：架构必须与目标机一致（amd64 / arm64 / arm），OS 选 linux 或留空。

**4. 任务 创建并启动**

- 任务页「Linux 示例」提供安装 / 更新 / 卸载组件，点击后自动选中对应组件与版本 latest。
- IP 段支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；先点「预览」确认命中主机与分批。
- 首次建议：灰度策略选「金丝雀」、首批 1 台、取消「自动推进」；首批成功后点「推进」继续。
- 命令任务：把任务类型切到「执行命令/脚本」，解释器留空自动 bash（缺失回退 sh），支持 stdin 与伪终端。
- 命令任务仅管理员可创建/查看，脚本与输出不会展示给普通用户。

### Windows（Server 2008 R2 ~ Windows 11）

**1. 主机 接入 Windows Agent**

- 在「主机」页选择 Windows 与架构（绝大多数为 amd64；Windows on ARM 选 arm64），点「生成接入命令」，在目标机以管理员 PowerShell 执行。
- Server 2008 R2 ~ Windows 11 通用；2008 R2 + PowerShell 2.0 不支持 TLS 1.2 时，页面命令直接从控制台下载 Agent（HTTP 即可），不依赖 GitHub。
- Agent 注册为 Windows 服务 upgrade-agent（LocalSystem，失败 5s/10s/30s 自动重启），并安装每分钟运行的计划任务看门狗。
- 可选 MSI：msiexec /i upgrade-agent-`<版本>`-x64.msi SERVERURL=https://`<控制台>` ENROLLTOKEN=`<令牌>`。
- 可选低权限账户：install --service-account "DOMAIN\svc" --service-password "`<密码>`"（需先授予「作为服务登录」）。
- 卸载："C:\Program Files\upgrade-agent\upgrade-agent.exe" uninstall（或 msiexec /x）。

```powershell
# 管理员 PowerShell；控制台会生成完整命令，手工执行等价于：
$exe = Join-Path $env:TEMP 'upgrade-agent.exe'
Remove-Item -Force -ErrorAction SilentlyContinue $exe
(New-Object System.Net.WebClient).DownloadFile('http://<控制台>/api/agent/binary/windows-amd64', $exe)
Start-Process -FilePath $exe -ArgumentList 'install','--server','http://<控制台>','--enroll-token','<令牌>' -Wait -NoNewWindow

& 'C:\Program Files\upgrade-agent\upgrade-agent.exe' status
& 'C:\Program Files\upgrade-agent\upgrade-agent.exe' diagnose
```

**2. 组件 选择 Windows 示例**

- 组件页顶部「Windows 示例」提供：windows_exporter_script（安装）、windows_exporter_script_update（更新，失败自动回滚）、windows_exporter_script_uninstall（卸载，-Yes 非交互）。
- 示例 script_url 指向 examples/windows_exporter/install.ps1（经 proxy 代理）；Agent 默认用 powershell.exe -NoProfile -ExecutionPolicy Bypass -File 执行。
- install.ps1 支持 install / update / uninstall / status；安装到 C:\Program Files\windows_exporter，服务名 windows_exporter，端口 16666，自动创建防火墙规则。
- textfile 采集目录：C:\monitor\windows_exporter\textfile_inputs；配置文件由脚本生成，可用 -ConfigPath 指定自定义配置。
- 兼容 PowerShell 2.0（Server 2008 R2 起）；Agent 会把命令脚本转为 UTF-8 BOM + CRLF，平台下发 install.ps1 时也会自动补 BOM，避免中文脚本乱码/语法错误。
- Windows 10 / Server 2016 及以上自动使用 latest；Windows 7 / Server 2008 R2 ~ 2012 R2 自动切换 legacy（基于官方 v0.23.1 + Go 1.20，默认启用 hardware_info 移植），也可用 -Flavor latest|legacy 强制。
- 2008 R2 无法直连 GitHub 时：install.ps1 支持 -BinaryPath / -BinaryUrl。
- 平台也无法出网（控制台请求 GitHub 502）时：在「外部文件」为 windows-exporter-`<arch>`（旧系统为 windows-exporter-`<arch>`-legacy）点「上传离线文件」，脚本会自动优先从平台获取；exe 与脚本都可离线上传。
- 2008 R2 上脚本只探测对应通道的平台「本地/缓存」接口（?local=1），未上传时几秒内报错并提示上传，不会等待平台现场下载；平台能出网时可先在外部文件页「刷新缓存」预下载。
- legacy 默认启用 hardware_info（仅本仓库 legacy 资产包含该采集器）；如替换为官方 v0.23.1，请从 collectors.enabled 删除 hardware_info 或使用 -NoConfig 自备配置。
- 低权限运行：install.ps1 -ServiceAccount "NT AUTHORITY\LocalService"（或域账户 + -ServicePassword）。
- 更新示例先备份 windows_exporter.exe.bak，启动失败自动回滚；卸载示例不带 health/version 检查。

```powershell
# 仓库自带单文件 PowerShell 安装脚本（PS 2.0 兼容）
dir examples\windows_exporter\install.ps1

# 可选：在目标机手动试跑（管理员 PowerShell）
powershell -ExecutionPolicy Bypass -File .\install.ps1 install
powershell -ExecutionPolicy Bypass -File .\install.ps1 status
powershell -ExecutionPolicy Bypass -File .\install.ps1 update
powershell -ExecutionPolicy Bypass -File .\install.ps1 uninstall -Yes
```

**3. 制品 上传安装包（可选）**

- 脚本示例已填真实 script_url，通常可跳过本页。
- 不想依赖外链时：删除 script_url，在制品页上传 examples/windows_exporter/install.ps1（版本 latest，架构 all）。
- 上传 windows_exporter.exe 时：OS 选 windows，架构选 amd64 或 arm64（必须与目标机一致）；平台不会解压 zip。
- Server 2008 R2 无法访问 GitHub 时，用「外部文件」代缓存 exe/脚本，再在组件里用平台地址或 -BinaryUrl 指定。

**4. 任务 创建并启动**

- 任务页「Windows 示例」提供安装 / 更新 / 卸载脚本，点击后自动选中对应 Windows 组件与版本 latest。
- 解释器保持 powershell（旧系统用 powershell.exe，新系统也可填 pwsh）；.ps1 会自动加 BOM/CRLF。
- IP 段支持 10.1.255.24-26、10.1.255.0/24；先预览再创建，首次建议金丝雀 1 台并关闭自动推进。
- 命令任务示例（仅管理员）：解释器 powershell，脚本 Get-Service windows_exporter | Select-Object Status,Name。
- Windows 不支持伪终端（TTY）；交互确认改用脚本参数（如 uninstall -Yes）或标准输入。
- 自升级灰度：设置页「Agent → 自动升级灰度比例」按 agent_id 稳定哈希放量（5% → 25% → 100%）。

> 详细参数见下文《Windows Agent（Server 2008 R2 ~ Windows 11）》《node_exporter 安装示例》《windows_exporter 安装示例》《执行命令 / 脚本任务》。

## Quick Start (Linux / Windows)

> Conventions:
> - The Hosts/Components/Artifacts/Tasks pages include a “Quick Start” card with Linux/Windows examples; the commands below match the UI.
> - `<Console>`: reachable URL, e.g. http://10.0.88.88:8090 or https://upgrade.example.com.
> - `<Token>`: one-time enrollment token from Hosts → Enroll host → Generate command (15-minute TTL).
> - For Windows hosts, place upgrade-agent-windows-{amd64,arm64}.exe into the console dist directory (a .zip is also published; mount it in Docker), otherwise downloads return 404.

### Linux (systemd / SysV)

**1. Host: enroll Linux agent**

- On Hosts, choose Linux and architecture (amd64 / arm64 / arm; older kernels like CentOS 6 use the legacy build), then click Generate command. Tokens are one-time and expire in 15 minutes.
- Run the command as root on the target. The agent installs as a systemd service (SysV fallback) and the host appears in the list.
- Hostname, OS, architecture and egress IP are reported by the agent; multi-NIC hosts are matched by their egress address.
- Consul sync can also import hosts, but hosts without an agent are skipped when tasks are dispatched.

```bash
# The console generates this command; manually it is equivalent to:
curl -fsSL -o /tmp/upgrade-agent 'http://<console>/api/agent/binary/linux-amd64'
chmod +x /tmp/upgrade-agent
sudo /tmp/upgrade-agent install \
  --server 'http://<console>' \
  --enroll-token '<token>'

sudo /tmp/upgrade-agent status     # registration/connection status
sudo /tmp/upgrade-agent diagnose   # diagnose connectivity to the console
```

**2. Component: use a Linux example**

- The Components page exposes Linux examples: exec install/update/uninstall, script install/update/uninstall, and binary replacement.
- Start with Script install: script_url points to examples/node_exporter/install.sh (via proxy), so no artifact upload is required.
- install.sh supports install / update / uninstall / status. update backs up the old binary and rolls back on startup failure; uninstall -y skips confirmation.
- The script auto-detects systemd/SysV: CentOS/RHEL 7+, Rocky, openEuler, Ubuntu 16.04+, Debian 8+, SLES 12+ use systemd; older kernel systems like CentOS 6, Ubuntu 14.04, Debian 7 and SLES 11 use /etc/init.d.
- Exec-only examples use exec + script_url + exec_args (install / update / uninstall -y) with no artifact.
- Binary replacement is for platform-managed backup/rollback: register the service in the post_install script and let the platform back up install_path.
- Advanced JSON fields exec_args / exec_env / exec_timeout / work_dir control execution, e.g. NODE_EXPORTER_VERSION=1.8.2 via exec_env.
- agent.allow_exec_mode is enabled by default; disabling it also disables command tasks.

```bash
# Single-file installer with embedded systemd unit and SysV init
ls -l examples/node_exporter/install.sh

# Optional: run manually on the target as root
sudo bash examples/node_exporter/install.sh install
sudo bash examples/node_exporter/install.sh status
sudo bash examples/node_exporter/install.sh update
sudo bash examples/node_exporter/install.sh uninstall -y
```

**3. Artifact: upload installer (optional)**

- Script examples already include a real script_url, so this page is usually optional.
- To avoid external URLs, remove script_url from the component JSON and upload examples/node_exporter/install.sh (version latest, arch all).
- The platform does not extract tar.gz; upload an executable script or an already-extracted binary.
- For binary uploads, arch must match the target (amd64 / arm64 / arm); select OS linux or leave it empty.

**4. Task: create and start**

- The Tasks page offers Linux install/update/uninstall examples that select the component and version latest automatically.
- IP ranges support 10.1.255.24-26 and 10.1.255.0/24, comma-separated. Click Preview to confirm matches and batches first.
- For the first run use canary with 1 host and disable auto-promote; click Promote after the first batch succeeds.
- Command tasks: switch kind to command; interpreter defaults to bash/sh and supports stdin and a pseudo-terminal.
- Command tasks are admin-only; scripts and output are hidden from regular users.

### Windows (Server 2008 R2 – Windows 11)

**1. Host: enroll Windows agent**

- On Hosts, choose Windows and architecture (usually amd64; Windows on ARM uses arm64), generate the command and run it in an elevated PowerShell.
- Works on Server 2008 R2 – Windows 11. On 2008 R2 + PowerShell 2.0 without TLS 1.2, the command downloads the agent directly from the console over HTTP (no GitHub dependency).
- The agent registers as the Windows service upgrade-agent (LocalSystem, restart 5s/10s/30s) and installs a per-minute scheduled-task watchdog.
- Optional MSI: msiexec /i upgrade-agent-`<version>`-x64.msi SERVERURL=https://`<console>` ENROLLTOKEN=`<token>`.
- Optional low-privilege account: install --service-account "DOMAIN\svc" --service-password "`<password>`" (grant “Log on as a service” first).
- Uninstall: "C:\Program Files\upgrade-agent\upgrade-agent.exe" uninstall (or msiexec /x).

```powershell
# Elevated PowerShell; the console generates this command:
$exe = Join-Path $env:TEMP 'upgrade-agent.exe'
Remove-Item -Force -ErrorAction SilentlyContinue $exe
(New-Object System.Net.WebClient).DownloadFile('http://<console>/api/agent/binary/windows-amd64', $exe)
Start-Process -FilePath $exe -ArgumentList 'install','--server','http://<console>','--enroll-token','<token>' -Wait -NoNewWindow

& 'C:\Program Files\upgrade-agent\upgrade-agent.exe' status
& 'C:\Program Files\upgrade-agent\upgrade-agent.exe' diagnose
```

**2. Component: use a Windows example**

- Windows examples: windows_exporter_script (install), windows_exporter_script_update (update with rollback) and windows_exporter_script_uninstall (uninstall -Yes, non-interactive).
- script_url points to examples/windows_exporter/install.ps1 (via proxy); the agent runs it with powershell.exe -NoProfile -ExecutionPolicy Bypass -File.
- install.ps1 supports install / update / uninstall / status. It installs to C:\Program Files\windows_exporter, service windows_exporter, port 16666 and creates a firewall rule.
- Textfile collector directory: C:\monitor\windows_exporter\textfile_inputs; use -ConfigPath for a custom config.
- PowerShell 2.0 compatible (Server 2008 R2+). The agent rewrites command scripts to UTF-8 BOM + CRLF, and the console adds a BOM when serving install.ps1, avoiding encoding/parse errors.
- Windows 10 / Server 2016 and newer use latest automatically; Windows 7 / Server 2008 R2 – 2012 R2 switch to legacy (based on official v0.23.1 + Go 1.20, with the ported hardware_info collector enabled by default). Force it with -Flavor latest|legacy.
- On 2008 R2 without GitHub access, install.ps1 supports -BinaryPath / -BinaryUrl.
- If the console also cannot reach GitHub (502), upload the exe under External files → windows-exporter-`<arch>` (legacy systems: windows-exporter-`<arch>`-legacy) → Upload offline file; the script then prefers the platform copy. Both exe and scripts can be uploaded offline.
- On 2008 R2 the script only probes the selected channel's platform local/cache endpoint (?local=1); if nothing is uploaded it fails within seconds with an upload hint instead of waiting for the console to fetch from GitHub. If the console has outbound access, use Refresh cache beforehand to pre-download.
- hardware_info is enabled by default in legacy (only the legacy asset from this repository includes it). If you substitute the official v0.23.1 binary, remove hardware_info from collectors.enabled or use -NoConfig with your own config.
- Low privilege: install.ps1 -ServiceAccount "NT AUTHORITY\LocalService" (or a domain account with -ServicePassword).
- Update backs up windows_exporter.exe.bak and rolls back on startup failure; uninstall omits health/version checks.

```powershell
# Single-file PowerShell installer (PS 2.0 compatible)
dir examples\windows_exporter\install.ps1

# Optional: run manually in elevated PowerShell
powershell -ExecutionPolicy Bypass -File .\install.ps1 install
powershell -ExecutionPolicy Bypass -File .\install.ps1 status
powershell -ExecutionPolicy Bypass -File .\install.ps1 update
powershell -ExecutionPolicy Bypass -File .\install.ps1 uninstall -Yes
```

**3. Artifact: upload installer (optional)**

- Script examples already include script_url, so this page is usually optional.
- To avoid external URLs, remove script_url and upload examples/windows_exporter/install.ps1 (version latest, arch all).
- For windows_exporter.exe uploads, select OS windows and arch amd64/arm64 matching the target. The platform does not extract zip files.
- On 2008 R2 without GitHub access, mirror exe/scripts with the external-file cache and reference the platform URL or -BinaryUrl.

**4. Task: create and start**

- The Tasks page offers Windows install/update/uninstall examples that select the matching component and version latest.
- Keep interpreter powershell (powershell.exe on old systems; pwsh is also supported). .ps1 files get BOM/CRLF automatically.
- IP ranges support 10.1.255.24-26 and 10.1.255.0/24. Preview first; start with a 1-host canary and auto-promote disabled.
- Admin-only command task example: interpreter powershell, script Get-Service windows_exporter | Select-Object Status,Name.
- Windows has no pseudo-terminal (TTY); use script flags such as uninstall -Yes or stdin for interaction.
- Self-update canary: Settings → Agent → auto-update percentage buckets by a stable agent_id hash (5% → 25% → 100%).

> See “Windows Agent (Server 2008 R2 – Windows 11)”, “node_exporter example”, “windows_exporter example” and “Command/script tasks” below for details.
<!-- QUICKSTART:END -->

---

## 部署方式

> 下载均通过 `https://proxy.xiaonuo.live/?url=` 代理加速，`wget` 支持断点续传(`-c`)。
> Linux 按需把 `amd64` 换成 `arm64` 或 `arm`；旧内核（kernel 2.6.32 等）请使用 `*-legacy` 版本。
> 接入 **Windows 主机**（Server 2008 R2 ~ Windows 11）时，把
> `upgrade-agent-windows-amd64.exe`（或 `arm64`）放到控制台的 `dist` 目录，否则下载 404。
> Release 目前同时提供原始 `.exe` 与 `.zip`（旧版本可能只有 zip）。
>
> **Windows Agent 是否包含在 Release 里？** 包含。CI 的 `agent-windows` 作业会构建
> `upgrade-agent-windows-{amd64,arm64}.exe`，并同时发布 `.exe` 与 `.zip`；
> 但 **Docker 镜像只内置 Linux 当前架构**，接 Windows 主机需按下文把 `dist` 挂进容器。

### 方式一：systemd 直接部署（Linux 宿主机，推荐）

> **本方式的下载、目录布局与 systemd 单元没有变化**；已有部署升级只需替换二进制并
> `systemctl restart upgrade-mgr`。下面补充了新增能力的**可选配置**（不配置即使用默认值）。
> 首次启动会自动应用迁移 020–035（组织/多租户/审计/自动化令牌/node_exporter 外部文件/任务与主机批量删除作业），旧库无需手工执行 SQL。

```bash
# 1. 下载控制台二进制到 /tmp 并解压
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.tar.gz \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-mgr-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-mgr.tar.gz -C /tmp

# 1.1 下载 Linux Agent 二进制（供控制台生成一键安装命令时下载）
wget -q -c --no-check-certificate -O /tmp/upgrade-agent.tar.gz \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-agent.tar.gz -C /tmp

# 1.1.1 【可选】旧内核兼容版本（kernel 2.6.32 等，如 CentOS 6）
#       下载 legacy 版本，安装命令会自动检测内核并选择对应构建
wget -q -c --no-check-certificate -O /tmp/upgrade-agent-linux-amd64-legacy.tar.gz \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64-legacy.tar.gz"
tar -xzf /tmp/upgrade-agent-linux-amd64-legacy.tar.gz -C /tmp

# 1.2 【可选】准备 Windows Agent（仅接 Windows 主机时需要）
#     Release 直接提供 .exe，下载后放入 dist 即可，无需 unzip。
wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-amd64.exe \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.exe"

# 旧 Release 只有 .zip 时，可改用：
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-amd64.zip \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.zip"
# unzip -o /tmp/upgrade-agent-windows-amd64.zip -d /tmp

# 可选：按需补充其他平台/架构（Windows on ARM、Linux arm64、旧内核其他架构）
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-arm64.exe \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-arm64.exe"
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-linux-arm64.tar.gz \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-arm64.tar.gz"
# tar -xzf /tmp/upgrade-agent-linux-arm64.tar.gz -C /tmp
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-linux-386-legacy.tar.gz \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-386-legacy.tar.gz"
# tar -xzf /tmp/upgrade-agent-linux-386-legacy.tar.gz -C /tmp
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-linux-arm64-legacy.tar.gz \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-arm64-legacy.tar.gz"
# tar -xzf /tmp/upgrade-agent-linux-arm64-legacy.tar.gz -C /tmp

# 2. 替换二进制(mv -f 原子覆盖)
mkdir -p /opt/upgrade-mgr/dist
mv -f /tmp/upgrade-mgr-linux-amd64 /usr/local/bin/upgrade-mgr
chmod +x /usr/local/bin/upgrade-mgr
# 文件名必须保持 upgrade-agent-<os>-<arch>[-legacy][.exe]，控制台按此名下发
mv -f /tmp/upgrade-agent-linux-amd64 /opt/upgrade-mgr/dist/upgrade-agent-linux-amd64
# 旧内核兼容版本（如果下载了）
[ -f /tmp/upgrade-agent-linux-amd64-legacy ] && \
  mv -f /tmp/upgrade-agent-linux-amd64-legacy /opt/upgrade-mgr/dist/upgrade-agent-linux-amd64-legacy
mv -f /tmp/upgrade-agent-windows-amd64.exe /opt/upgrade-mgr/dist/upgrade-agent-windows-amd64.exe
# 可选（与上面 1.2 的下载项对应）
# mv -f /tmp/upgrade-agent-windows-arm64.exe /opt/upgrade-mgr/dist/upgrade-agent-windows-arm64.exe
# mv -f /tmp/upgrade-agent-linux-arm64 /opt/upgrade-mgr/dist/upgrade-agent-linux-arm64
# mv -f /tmp/upgrade-agent-linux-386-legacy /opt/upgrade-mgr/dist/upgrade-agent-linux-386-legacy
# mv -f /tmp/upgrade-agent-linux-arm64-legacy /opt/upgrade-mgr/dist/upgrade-agent-linux-arm64-legacy

# 2.1 确认 dist 目录（控制台的 Agent 下载目录，默认 server.agent_binary_dir=./dist，
#     systemd 单元里 WorkingDirectory=/opt/upgrade-mgr，所以实际是 /opt/upgrade-mgr/dist）
ls -l /opt/upgrade-mgr/dist/
# 应至少包含：
#   upgrade-agent-linux-amd64                    Linux x86_64（普通构建）
#   upgrade-agent-linux-amd64-legacy（可选）     CentOS 6 / kernel 2.6.32+
#   upgrade-agent-windows-amd64.exe              Windows Server 2008 R2 ~ Windows 11（x64）
# 需要接入 Windows on ARM / Linux arm64 / CentOS 6 其他架构时，再补充对应文件。
# 缺少目标平台文件时，页面上该主机只会被跳过并提示「二进制不可用 / 下载 404」。

# 3. 准备配置目录和数据库
mkdir -p /etc/upgrade-mgr /data/artifacts
# 创建数据库（PostgreSQL 12+）
createdb upgrade_mgr

# 4. 创建配置文件
cat > /etc/upgrade-mgr/config.yaml <<'EOF'
server:
  host: 0.0.0.0
  port: 8090
  external_url: http://your-server-ip:8090
  session_secret: "请替换为至少16字节的随机密钥；留空会自动生成并持久化到数据库"
  # 审计日志保留天数（默认 180，最小 7；也可在设置页修改）
  audit_retention_days: 180

database:
  dsn: "postgresql://user:pass@localhost:5432/upgrade_mgr?sslmode=disable"

auth:
  provider: builtin
  admin_username: admin
  admin_password: "请替换为强密码"

artifact:
  dir: /data/artifacts
  storage_type: local
  max_concurrent_uploads: 4   # 本实例并发上传上限（1-64）
  lock_wait: 30m              # 跨副本上传/删除同组件制品的锁等待上限

# 可选：Redis 页面缓存（只读 API），也可在「设置 → Redis 缓存」启用并热加载
# 不可用时自动降级为直连 PostgreSQL，不影响启动与业务
redis:
  enabled: true
  mode: single              # single / cluster / sentinel
  host: 127.0.0.1
  port: 6379
  hosts: []                 # cluster：种子节点；sentinel：哨兵地址（host:port）
  master_name: ""           # mode=sentinel 必填
  password: "请替换为实际密码"
  db: 0                     # cluster 固定 0；超出服务器 databases 范围会自动回退并提示
  ttl: 10                   # 只读列表缓存秒数（上限 30，写操作立即失效）
  hosts_ttl: 0              # /api/hosts 单独 TTL（秒）；0=沿用 ttl，上限 300
  prefix: "upgrade_mgr:"    # 与同实例其它系统隔离

# 可选：从 consul_mgr 同步主机（覆盖多个服务组 / 多套 Consul）
# 需在 consul_mgr 的「设置 → 服务间 API」配置同一个令牌
consul_mgr:
  endpoint: ""                # 如 http://consul-mgr.internal:8888
  service_token: ""           # 与 consul_mgr 的 service_api.token 一致
  timeout: 30                 # 请求超时秒数；主机多时放大
  # 服务组在主机页的下拉中选择，不在此配置

agent:
  poll_hold: 20
  require_enroll_token: true
  # 任务仅创建者可操作（默认 true；管理员豁免；团队 owner_only 可覆盖）
  task_owner_only: true
  # 自动升级旧 Agent（默认 false）；灰度比例 0-100，按 agent_id 稳定哈希
  auto_update: false
  auto_update_percent: 100

# Redis 三种模式示例（环境变量写法见下）：
#   单点：   mode=single   host=10.1.230.44 port=7000
#   集群：   mode=cluster  hosts=["10.1.230.44:7000","10.1.230.45:7000","10.1.230.46:7000"]
#   哨兵：   mode=sentinel hosts=["10.1.230.50:26379"] master_name=mymaster
# 环境变量：REDIS_ENABLED / REDIS_MODE / REDIS_HOST / REDIS_PORT / REDIS_ADDRS /
#          REDIS_MASTER_NAME / REDIS_PASSWORD / REDIS_DB / REDIS_CACHE_TTL /
#          REDIS_HOSTS_CACHE_TTL / REDIS_PREFIX
# 页面「设置 → Redis 缓存」可查看实际连接的 DB、TTL、前缀与最近错误，并测试/清空缓存。
# 几万台规模可把 hosts_ttl 调到 60-300 秒：/api/hosts 是最重的查询，
# 任意写请求仍会立即清空缓存，所以页面改动不会看起来没生效。

# 可选：FlyIAM 集成（人员对照、字段同步、组织树同步）
# flyiam:
#   endpoint: "http://flyiam.internal:8000"
#   service_token: "<FlyIAM 管理员 API 令牌或 SERVICE_TOKEN>"
EOF

> **迁移与多租户**：首次启动自动应用 001–035。组织/团队/角色/审计新增
> `tenant_id`，历史数据默认为租户 `1`，单租户部署无需额外操作。
>
> **FlyIAM 组织树同步前置条件**：FlyIAM 需包含
> `GET /api/users/export`、`GET /api/departments/export`（服务令牌认证）；
> 旧版 FlyIAM 会返回 404，升级 FlyIAM 或在「人员组织 → 组织架构」手动同步时按提示排查。
>
> 以上新增配置均可在页面「设置」修改并热加载；环境变量等价覆盖：
> `SERVER_AUDIT_RETENTION_DAYS`、`AGENT_TASK_OWNER_ONLY`、
> `AGENT_AUTO_UPDATE_PERCENT`、`FLYIAM_ENDPOINT`、`FLYIAM_SERVICE_TOKEN`。

# 5. 下载 systemd 单元到 /tmp 再替换
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.service \
  "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/systemd/upgrade-mgr.service"
mv -f /tmp/upgrade-mgr.service /etc/systemd/system/upgrade-mgr.service

# 6. 启动服务
systemctl daemon-reload
systemctl enable --now upgrade-mgr

# 7. 查看状态
systemctl status upgrade-mgr
# 访问 http://your-server-ip:8090
```

> 只有使用旧版 `.zip` 时才需要 `unzip`（`yum install -y unzip` / `apt install -y unzip`）；推荐直接用 `.exe`。
> 也可以只下载当前要接入的平台：只接 Linux 主机就不必放 Windows 的 `.exe`，反之亦然。
> Docker 镜像已内置 linux/amd64 的 Agent，Windows Agent 需按下文单独放入 `dist` 目录。

#### Windows 目标机接入（Agent 以 Windows 服务运行）

1. 控制台「主机」页 →「接入主机」→ 操作系统选 **Windows** → 选架构（绝大多数为 `amd64`）→
   生成接入命令；
2. 把命令粘贴到目标机的**管理员 PowerShell** 中执行（Server 2008 R2 自带的 PowerShell 2.0 即可）：

```powershell
# 控制台生成的就是下面这种形式（令牌一次性、15 分钟有效，每台机器生成一次；
# 换行在 PowerShell 里可直接分行粘贴，也可用分号连成一行）
$ErrorActionPreference = 'Stop'
$exe = Join-Path $env:TEMP 'upgrade-agent.exe'
Remove-Item -Force -ErrorAction SilentlyContinue $exe
(New-Object System.Net.WebClient).DownloadFile('http://your-server-ip:8090/api/agent/binary/windows-amd64', $exe)
Start-Process -FilePath $exe -ArgumentList 'install','--server','http://your-server-ip:8090','--enroll-token','<token>' -Wait -NoNewWindow
```

3. 验证：

```powershell
Get-Service upgrade-agent            # 状态应为 Running，启动类型 Automatic
Get-Content "$env:ProgramData\upgrade-agent\state.json"   # 注册凭据（仅管理员可读）
```

- 如果是在 **cmd** 里操作，先执行 `powershell -NoProfile -ExecutionPolicy Bypass` 再粘贴命令；
- 服务以 `LocalSystem` 运行，重启后自动恢复；失败恢复动作 5s/10s/30s 自动重启；
  另注册计划任务 `upgrade-agent-watchdog` 每分钟兜底；
- 默认路径：`%ProgramFiles%\upgrade-agent\upgrade-agent.exe`、`%ProgramData%\upgrade-agent\state.json`；
- 卸载：`& "$env:ProgramFiles\upgrade-agent\upgrade-agent.exe" uninstall`；
- **平台地址填写**：Windows 主机若只能通过专线/VPN 隧道访问平台，在主机页「编辑」里填
  「连接地址覆盖」，保存后 Agent 约 30 秒内自动切换（详见下文「连接地址覆盖与连通性测试」）；
- **证书**：若控制台开启了 HTTPS/双向 TLS，安装命令追加
  `--tls-ca C:\path\ca.crt`（双向 TLS 再加 `--tls-cert` / `--tls-key`），
  证书路径会写入 state。注意 Windows Server 2008 R2 默认未启用 TLS 1.2，需先打 KB3140245。

> 控制台必须能提供 `upgrade-agent-windows-amd64.exe`：即上文步骤 1.2/2 放到
> `/opt/upgrade-mgr/dist/` 下的文件；否则 Windows 主机会下载 404。

**Windows 主机下载失败 / 连不上平台怎么办**

生成命令里的下载地址就是「控制台对外地址」，Windows 主机必须能访问它。若出现
`DownloadFile ... 无法连接到远程服务器`，按顺序排查：

```powershell
# 1. Windows 主机 → 平台，端口是否可达（这一步失败就是网络问题，与 Agent 无关）
Test-NetConnection 10.1.230.50 -Port 8090

# 2. 若不通，检查：平台是否监听 0.0.0.0、防火墙/安全组是否放行、是否跨网段/跨区域
#    服务端确认监听：
#      ss -lntp | grep 8090        # 应为 0.0.0.0:8090 或 *:8090
#      firewall-cmd --list-ports   # 或 ufw status

# 3. 平台地址写错或需走别的入口时，用“Windows 主机能访问到的地址”重新生成命令
#    例如内网地址、域名，或隧道/VPN 提供的地址：
#      --server http://10.8.0.1:8090
```

要点：

- **命令里的地址必须是 Windows 主机视角可达的地址**。服务端的 `external_url`
  如果填的是公网域名，而 Windows 主机在内网，就会连不上；反之亦然。
- **没有公网可达性时**：用隧道（WireGuard/IPSec/专线/frp 等）让 Windows 主机能访问平台，
  然后直接用隧道地址安装：`--server http://<隧道地址>:8090`。
  隧道本身由隧道软件建立，平台不参与。
- **已经用旧地址装过**（服务已注册）：在控制台主机页「编辑」填「连接地址覆盖」
  （`server_url_override`），保存后 Agent 约 30 秒自动切换；不需要重装。
- 平台侧可先用 `POST /api/hosts/{id}/tunnel-test` 或页面「测试连接地址」验证地址本身可服务。

> 命令生成逻辑已加保护：下载失败会打印「下载失败: …」并提示用 `Test-NetConnection`
> 自检，不会再继续执行 `Start-Process` 报「系统找不到指定的文件」。

### 方式二：Docker（推荐用于快速体验）

```bash
# 1. 创建工作目录
mkdir -p upgrade-mgr && cd upgrade-mgr

# 2. 下载 docker-compose.yml
wget -q -c --no-check-certificate -O /tmp/docker-compose.yml \
  "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/docker/docker-compose.yml"
mv -f /tmp/docker-compose.yml ./docker-compose.yml

# 3. 创建配置文件（参考上面 systemd 方式）
mkdir -p config data/artifacts logs
cat > config/config.yaml <<'EOF'
# 配置内容同上
EOF

# 4. （可选）放入 Windows Agent 等其他平台的二进制
#    镜像内置的只有 Linux 当前架构；要接 Windows 主机必须挂载 dist 目录，
#    并把文件命名为 upgrade-agent-windows-amd64.exe。
mkdir -p dist
wget -q -c --no-check-certificate -O dist/upgrade-agent-windows-amd64.exe \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.exe"
# 旧 Release 只有 .zip 时：
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-amd64.zip \
#   "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.zip"
# unzip -o /tmp/upgrade-agent-windows-amd64.zip -d dist

# 5. 在 docker-compose.yml 中放开 dist 挂载（见下方说明）
#      volumes:
#        - ./dist:/opt/upgrade-mgr/dist:ro

# 6. 启动
docker compose up -d

# 7. 查看日志
docker compose logs -f
```

> Windows 主机接入：容器内 `server.agent_binary_dir` 为 `/opt/upgrade-mgr/dist`，
> 所以必须把宿主机的 `./dist` 挂到该路径（`deploy/docker/docker-compose.yml` 里已预留注释）。
> 只在容器里放 Linux Agent 时，Windows 主机下载会 404。

镜像仓库：
- 华为云 SWR（推荐，国内可访问）: `swr.cn-east-3.myhuaweicloud.com/iflyelf/upgrade-mgr:latest`
- DockerHub（可选）: `iflyelf/upgrade-mgr:latest`

---

## 构建

```bash
make build          # 控制台
make build-agent    # Agent
make cross          # 交叉编译 amd64/arm64/arm 到 dist/
make agent-legacy   # 用 Go 1.23 编译 CentOS 6 兼容 Agent（amd64/386/arm64）到 dist/*-legacy
make test-agent-legacy  # 用 Go 1.23 跑 Agent 测试，防止误用 1.24+ API
make agent-windows  # 用 Go 1.20 编译 Windows Agent（amd64/arm64）到 dist/*.exe
make test-agent-windows # 用 Go 1.20 交叉编译 + vet 校验 Windows Agent
make test           # 单测（集成测试需 DATABASE_URL / CONSUL_ADDRESS）
```

### CentOS 6 / 老内核 Agent

只做静态编译（`CGO_ENABLED=0`）不够。静态编译只去掉了对 glibc 的依赖，Go 运行时本身对内核版本仍有要求：Go 1.24 起官方最低支持 Linux 3.2，CentOS 6 的内核是 2.6.32，不在支持范围内。

Agent 只依赖标准库，所以用独立 modfile `build/agent-legacy.mod`（`go 1.23`）配合 `GOTOOLCHAIN=go1.23.12` 单独编译，主 `go.mod` 不受影响。Go 1.23 是官方最后一个支持内核 2.6.32 的版本。

- 产物：`dist/upgrade-agent-linux-{amd64,386,arm64}-legacy`，均为静态链接
- CI：`agent-legacy` job 用 Go 1.23 运行测试并构建
- **自升级变体选择**：新 Agent 注册/心跳会携带 `build_variant`；旧 Agent 未上报时，服务端按内核判断：`2.x` 使用 `*-legacy`，其余使用普通构建。混合 fleet 请同时执行 `make cross` 与 `make agent-legacy`，否则缺少对应产物时该主机只会被跳过并在页面提示原因。
- ⚠️ Go 1.23 已停止安全维护，legacy 产物只建议用于确实无法升级内核的主机
- ⚠️ 还没有在真实 CentOS 6 主机上跑过，上线前请先实测 `install` / `run` / `watchdog`
- CentOS 6 没有 systemd，也没有 `/etc/os-release`。服务管理走 upstart（`initctl`，缺失时回退 SysV `service`），系统识别回退到 `/etc/centos-release` / `/etc/redhat-release`

### Windows Agent（Server 2008 R2 ~ Windows 11）

Windows 的支持下限是 **Windows Server 2008 R2**，因此使用 Go 1.20（最后一个仍支持该系统的
Go 版本）配合独立 modfile `build/agent-windows.mod` 编译。单套二进制同时覆盖
Server 2008 R2 / 2012 / 2012 R2 / 2016 / 2019 / 2022 与 Windows 7 / 8.1 / 10 / 11，
**不需要** Windows legacy 变体（组件 windows_exporter 另有 legacy 通道，见 5.1）。

- 支持架构：`amd64`、`arm64`（Windows on ARM）
- 产物：`dist/upgrade-agent-windows-{amd64,arm64}.exe`，静态链接、无 CGO
- 唯一第三方依赖：`golang.org/x/sys`（SCM 服务宿主与 `svc/mgr` 服务管理），`go.mod` 固定在 `go 1.20`
- 服务形态：注册为 **Windows 服务**（SCM，LocalSystem），失败恢复动作 5s/10s/30s 自动重启；
  自升级通过 `os.Exit(75)` + 「非崩溃失败也重启」触发
- 看门狗：`schtasks` 每分钟以 SYSTEM 运行 `upgrade-agent watchdog`，独占文件句柄保证单实例
- 默认路径：`%ProgramFiles%\upgrade-agent\upgrade-agent.exe`、
  `%ProgramData%\upgrade-agent\state.json`、备份目录 `%ProgramData%\upgrade-agent\backup`

**安装方式**（管理员 PowerShell；Server 2008 R2 自带 PowerShell 2.0 亦可）：

```powershell
# 控制台「接入主机」选择 Windows 后生成的命令形如：
$ErrorActionPreference = 'Stop'
$exe = Join-Path $env:TEMP 'upgrade-agent.exe'
Remove-Item -Force -ErrorAction SilentlyContinue $exe
(New-Object System.Net.WebClient).DownloadFile('https://<server>/api/agent/binary/windows-amd64', $exe)
Start-Process -FilePath $exe -ArgumentList 'install','--server','https://<server>','--enroll-token','<token>' -Wait -NoNewWindow
```

> `install` 会写入 state、创建服务 `upgrade-agent` 并启动。重复执行会先停服务、删除并重建，
> 因此可安全重装；已注册凭据默认保留，需要重新注册请加 `--force`。
> 卸载：`upgrade-agent.exe uninstall`。

**脚本任务与组件部署**：

| 项 | Linux | Windows |
|---|---|---|
| 默认解释器 | bash → sh | `powershell.exe`（缺失回退 `cmd.exe`） |
| `interpreter` 白名单 | bash/sh/dash/ksh/zsh | powershell/pwsh/cmd |
| 脚本扩展名 | `.sh` | `.ps1`（`.cmd`/`.bat` 自动走 cmd） |
| 伪终端（TTY） | `script(1)` | 不支持，自动降级为普通执行 |
| 部署命令首参数 | `/bin/sh`、`systemctl`… | `powershell.exe -NoProfile -Command ...`、`sc.exe`… |
| `install_path` 校验 | 绝对路径 + 黑名单（/etc、/boot…）+ 白名单根 | 盘符绝对路径 + 禁止 UNC/`..` + 系统目录黑名单 + 程序目录白名单 |

组件服务管理在 Windows 上使用 `golang.org/x/sys/windows/svc/mgr`（不解析本地化的 `sc.exe` 输出），
`service_name` 直接填 SCM 服务名即可。PowerShell 脚本建议显式 `exit` 非零码，
否则 Windows 不会把脚本内未捕获异常以外的失败反映到退出码上。

**服务运行账户（最小权限）**

默认以 LocalSystem 运行（安装/启停服务、写 Program Files 都需要较高权限）。
如需低权限账户，先授予其「作为服务登录」权限，再安装：

```powershell
upgrade-agent.exe install --server https://<server> --enroll-token <token> `
  --service-account 'DOMAIN\svc_upgrade' --service-password '<password>'
```

`windows_exporter` 示例脚本同样支持 `-ServiceAccount` / `-ServicePassword`；
卸载时会删除防火墙规则（Agent 只主动出站，不创建入站规则）。

**MSI 安装包**

CI 的 Windows 作业会生成 `dist/upgrade-agent-<version>-x64.msi`（WiX v3）：

```powershell
msiexec /i upgrade-agent-0.1.0-x64.msi SERVERURL=https://upgrade.example.com ENROLLTOKEN=<一次性令牌>
```

- 安装到 `%ProgramFiles%\Upgrade Agent`，注册并启动服务 `upgrade-agent`；
- `ENROLLTOKEN` 建议使用 15 分钟有效期的注册令牌；
- 卸载：`msiexec /x upgrade-agent-0.1.0-x64.msi`（自动停服并删除服务）。

**真机回归流水线**

`.github/workflows/windows-e2e.yml`：

- `windows-latest`：每次改动 Agent 相关代码即编译、vet、真实执行 PowerShell/cmd 用例
  （输出、退出码、stdin、中文脚本 BOM）、PowerShell 7 兼容性、构建 MSI；
- **Windows Server 2008 R2**：自托管 runner（标签 `server2008r2`），
  仓库变量 `ENABLE_WIN2008R2_RUNNER=true` 后启用；
- **Windows arm64**：自托管 runner（标签 `arm64`），仓库变量 `ENABLE_WINARM64_RUNNER=true` 后启用。

首次接入 2008 R2 / arm64 机器时，用管理员 PowerShell 执行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows_runner_setup.ps1 `
  -RepoURL https://github.com/iflyelf/upgrade_mgr `
  -RunnerToken <runner 注册令牌> -Labels server2008r2
```

> Server 2008 R2 需 .NET Framework 4.6.2+；Runner 版本过高可能不再支持该系统，脚本默认 2.311.0。

**自动升级灰度**

设置页「Agent → 自动升级灰度比例」支持 0-100：按 `agent_id` 的稳定哈希选择，
同一主机多次心跳结果一致。建议按 5% → 25% → 100% 分批放量。

**执行资源限制**

- 单次命令脚本上限 1MB（服务端另有 200KB 限制）；
- 单次命令超时上限 24h，超过自动收敛；
- stdout/stderr 各保留最多 4MB，超出部分截断并追加提示（实时日志流不受影响）。

---

## CI/CD 与镜像发布

主流水线为 `.github/workflows/ci-cd.yml`；另有：

- `.github/workflows/windows-e2e.yml`：Windows 真机（windows-latest / 2008 R2 / arm64）；
- `.github/workflows/web-e2e.yml`：Playwright 前端端到端。

主流水线说明（原 `build.yml` 与 `publish.yml` 已合并，避免同一事件重复编译）：

- **Pull Request**：跑 PostgreSQL 集成测试、`go vet`、前端语法检查，并编译三种架构做校验，不发布任何产物。
- **push main / 手动触发**：测试通过后编译多架构二进制与 Legacy Agent，打包发布到 GitHub Release 的 `latest`，并构建推送镜像。
- **push tag `v*`**：复用同一套编译产物，创建对应版本号的 Release；不会移动 `latest` 镜像标签。

推送代码到 `main` 或在 Actions 页面手动触发 `.github/workflows/ci-cd.yml` 即构建，二进制与镜像统一发布到 `latest`：

| 产物 | 位置 |
|---|---|
| 控制台二进制 | `upgrade-mgr-linux-{amd64,arm64,arm}.tar.gz` |
| Agent 二进制 | `upgrade-agent-linux-{amd64,arm64,arm}.tar.gz` |
| Agent Legacy（旧内核 kernel 2.6.32 等） | `upgrade-agent-linux-{amd64,386,arm64}-legacy.tar.gz` |
| Agent Windows | `upgrade-agent-windows-{amd64,arm64}.exe`（同时提供 `.zip`） |
| 华为云 SWR 镜像 | `swr.cn-east-3.myhuaweicloud.com/iflyelf/upgrade-mgr:latest` |
| DockerHub 镜像（可选） | `iflyelf/upgrade-mgr:latest` |

二进制发布到 GitHub Release 的 `latest` 标签，下载统一走 `https://proxy.xiaonuo.live/?url=` 加速。

镜像凭据通过 GitHub Secrets 注入，**不要写入仓库**：

| Secret | 说明 |
|---|---|
| `SWR_USERNAME` | 华为云 SWR 用户名，格式 `cn-east-3@<Access Key Id>` |
| `SWR_PASSWORD` | 华为云 SWR 登录密码 |
| `DOCKER_USERNAME` | DockerHub 用户名（可选） |
| `DOCKER_PASSWORD` | DockerHub 密码/令牌（可选） |

> 同一分支仅保留最新一次构建（自动取消旧构建）；工作流结束后自动清理 1 天前的旧运行记录，最少保留 3 次。

---

## 部署指南

### 1. 准备数据库

```bash
# PostgreSQL 12+
createdb upgrade_mgr
# 迁移会在首次启动时自动执行，无需手动导入 SQL
```

### 2. 启动控制台

**生产环境配置示例**（`config.yaml`，以 Nginx/HTTPS 为例；Nginx 不是必需组件，直连示例见下）：

```yaml
server:
  host: 0.0.0.0
  port: 8090
  external_url: https://upgrade.example.com  # Agent/浏览器实际访问地址，见下方说明
  web_embedded: true
  session_secret: "your-64-byte-random-secret"
  trust_proxy: false  # 仅可信反向代理后开启
  cookie_secure: "auto"

database:
  dsn: postgresql://user:pass@postgres.internal:5432/upgrade_mgr?sslmode=require
  max_open_conns: 50
  max_idle_conns: 10

auth:
  provider: builtin  # builtin（本地账号）或 casdoor（FlyIAM SSO）
  admin_username: admin
  admin_password: ""  # 留空则首次启动随机生成；建议用环境变量 ADMIN_PASSWORD 覆盖

# 仅 auth.provider=casdoor 时需要；builtin 模式可全部留空
casdoor:
  endpoint: "http://10.0.56.118:8000"          # 服务端访问 Casdoor 的地址
  public_endpoint: "https://flyiam.example.com" # 浏览器访问地址（和 endpoint 相同可留空）
  client_id: "your-client-id"
  client_secret: "your-client-secret"
  organization: flyiam
  application: flyiam
  # redirect_uri: "https://upgrade.example.com/api/auth/callback"  # 留空按 external_url 自动推导
  certificate: ""                              # 可选（预留）

artifact:
  dir: /data/artifacts
  storage_type: local  # local/s3/oss
  max_concurrent_uploads: 4   # 1-64；每个上传占用 1 条 advisory lock 连接
  lock_wait: 30m              # 跨副本上传/删除锁等待上限（1s-24h）

agent:
  poll_hold: 20
  require_enroll_token: true  # 生产环境强烈建议开启
  allow_exec_mode: true       # 允许 exec/script 模式与命令任务（默认开启；仅需二进制替换时可关闭）
  auto_update: false          # 心跳/注册发现旧 Agent 时自动自升级（需 dist 中已有目标二进制）

consul:  # 可选：同步主机清单；编排器不依赖 Consul
  enabled: true
  address: consul.internal:8500
  token: ""  # 建议用环境变量 CONSUL_TOKEN 覆盖
  datacenter: ""  # 留空自动探测；多数据中心需显式指定
  cleanup_missing: true  # 同步后清理已从 Consul 移除的纯 Consul 主机（默认开启）
```

> **配置优先级**：运行期「设置页」写入的 `app_settings` > 环境变量 > 配置文件 > 内置默认值。
> 配置文件中的 `${VAR}` **不会被自动展开**，敏感项请留空并通过环境变量注入。常用变量：`SERVER_EXTERNAL_URL`、`TRUST_PROXY`、`COOKIE_SECURE`、`ADMIN_PASSWORD`、`SESSION_SECRET`、`CONSUL_ENABLED`、`CONSUL_SYNC_INTERVAL`、`CONSUL_TOKEN`、`CONSUL_CLEANUP_MISSING`、`CASDOOR_CLIENT_SECRET`、`ARTIFACT_STORAGE_TYPE`、`ARTIFACT_MAX_CONCURRENT_UPLOADS`、`ARTIFACT_LOCK_WAIT`、`ARTIFACT_S3_*`、`ARTIFACT_OSS_*`、`EXTERNAL_BINARY_PROXY`、`EXTERNAL_BINARY_PROXY_HOSTS`（可写入 systemd 的 `EnvironmentFile` 或 K8s Secret）。

**配置文件 vs 设置页**：绝大多数配置都能在「设置」页直接改，配置文件只用于首次启动的引导。
分三类（页面上有对应标记）：

| 类别 | 页面标记 | 典型项 | 说明 |
|---|---|---|---|
| 保存即生效（多副本自动同步） | 无 | `artifact.max_concurrent_uploads`、`artifact.lock_wait` | 当前副本立即热更新；其他副本约 5 秒内轮询 DB 自动同步，无需重启 |
| 保存即生效 | 无 | `server.external_url`、`consul.*`、`redis.*`、`agent.*`、`consul_mgr.*`、`flyiam.*`、外部文件白名单 | 保存后立即作用于当前副本；Consul/Redis 会自动重建连接 |
| 保存后需重启 | 🔴 需重启 | 监听地址/端口、`trust_proxy`、`cookie_secure`、`metrics_token`、`session_secret`、认证方式、Casdoor、制品后端 | 这些值在启动时被绑定到中间件/连接池/认证提供方上，改内存不影响已建对象 |
| 只读展示 | 灰色 只读 | `database.*` | 启动顺序是「先连库 → 再读设置」，数据库参数不可能由页面提供，只展示当前生效值 |

> 只读项即使绕过页面直接调 `PUT /api/settings` 也会被服务端拒绝，不会写进 `app_settings`。
>
> 数据库密码、`auth.admin_password` 不在设置页出现：前者属于只读的数据库配置，
> 后者仅在启动时用于确保管理员账号存在，日常改密请用「用户菜单 → 修改密码」。

**`server.external_url` 怎么填**：

填写 **Agent/浏览器实际访问本服务的地址**，不是监听地址（`host`/`port`）。

> **页面可直接修改**：登录控制台 → 设置 → 服务器 → “对外访问地址 external_url”，保存后写入
> `app_settings`（优先级高于配置文件和环境变量）。新生成的 Agent 安装命令**立即**使用新地址，
> 无需重启；接入卡片会显示当前命令使用的地址，留空时回退为当前请求的 Host。
>
> 注意：Casdoor 回调地址和 `cookie_secure: auto` 在服务启动时计算，修改 `external_url` 后
> 如需同步这两项，请重启服务。
>
> Nginx/HTTPS 域名场景示例：`https://upgrade.example.com`（不要带结尾斜杠和路径）。

它会影响：

- 控制台生成的一键安装命令（下载 Agent、`--server` 参数）
- Casdoor OAuth 回调地址推导
- `cookie_secure: auto` 时是否给会话 Cookie 加 `Secure`

按部署方式选择：

| 部署方式 | 填写示例 |
|---|---|
| 局域网直连（无域名） | `http://10.0.88.88:8090` |
| Nginx/HTTPS 反向代理 | `https://upgrade.xiaonuo.live` |
| Docker 端口映射 | `http://10.0.88.88:8090` |

规则：

- 必须带协议头：`http://` 或 `https://`
- 不要带结尾斜杠，不要带路径（如 `/upgrade`）
- **不要填 `localhost` / `127.0.0.1`**，否则其他机器上的 Agent 无法访问
- 端口不是 80/443 时必须写端口
- `server.port` 是服务监听端口；反向代理场景下 `external_url` 是代理后的域名，通常不带 8090
- 反向代理后如需获取真实客户端 IP，设置 `trust_proxy: true`，并配置 `trusted_proxies`

> 临时不确定时可以留空：生成安装命令会回退使用当前请求的 Host（按 `http://` 拼接）。但 HTTPS、反向代理或 Casdoor 场景务必显式填写，否则协议/回调地址会出错。

**不用 Nginx 可以吗？**

可以。控制台自带 HTTP 服务、内嵌前端和 Agent 长轮询接口，不依赖 Nginx，也不使用 WebSocket。无 Nginx 的最小配置：

```yaml
server:
  host: 0.0.0.0          # 或绑定具体内网 IP，缩小暴露面
  port: 8090
  web_embedded: true     # 默认 true，直接提供前端页面
  external_url: "http://10.0.88.88:8090"
  trust_proxy: false     # 没有反向代理时必须保持 false
  cookie_secure: "auto"  # HTTP 下自动为 false，登录正常
```

Docker 环境变量方式：

```bash
SERVER_EXTERNAL_URL=http://10.0.88.88:8090
TRUST_PROXY=false
COOKIE_SECURE=auto
```

Agent 安装命令里的 `--server` 使用同一地址：

```bash
/tmp/upgrade-agent-linux-amd64 install \
  --server http://10.0.88.88:8090 \
  --enroll-token '<token>'
```

注意：

- 防火墙/安全组放行 8090，且 Agent 机器能直接访问该地址。
- 纯 HTTP 下不要设置 `cookie_secure: true`，否则浏览器不回传 Cookie，表现为登录后仍跳回登录页。
- 纯 HTTP 传输的密码、会话 Cookie、Agent 令牌和指令都是明文，仅适合可信内网；公网部署仍建议加 TLS。
- 如使用 Casdoor，`external_url` 与 Casdoor 应用回调必须一致；Casdoor 是否允许 `http://` 回调取决于其配置。

**Casdoor 对接说明**：
- `endpoint`：本服务后端访问 Casdoor 的地址（用于换取 token、拉取用户信息）。
- `public_endpoint`：浏览器跳转授权页用的地址；Casdoor 内外网地址不同时才需填写，留空则复用 `endpoint`。
- `client_id` / `client_secret`：Casdoor 应用的凭据。
- `organization` / `application`：Casdoor 组织名与应用名（FlyIAM 默认均为 `flyiam`）。
- 回调地址：留空时自动使用 `${server.external_url}/api/auth/callback`，需在 Casdoor 应用里配置相同的 Redirect URL。

**systemd 单元**（`/etc/systemd/system/upgrade-mgr.service`）：

```ini
[Unit]
Description=Upgrade Manager
After=network.target postgresql.service

[Service]
Type=simple
User=upgrade
WorkingDirectory=/opt/upgrade-mgr
ExecStart=/opt/upgrade-mgr/upgrade-mgr -c /opt/upgrade-mgr/config.yaml
EnvironmentFile=/opt/upgrade-mgr/.env
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 3. Nginx 反向代理（HTTPS）

```nginx
upstream upgrade_mgr {
    server 127.0.0.1:8090;
}

server {
    listen 443 ssl http2;
    server_name upgrade.example.com;

    ssl_certificate /etc/ssl/certs/upgrade.crt;
    ssl_certificate_key /etc/ssl/private/upgrade.key;

    location / {
        proxy_pass http://upgrade_mgr;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # 长轮询超时
        proxy_read_timeout 30s;
    }

    # 制品下载放宽超时
    location ~ ^/api/artifacts/.*/download {
        proxy_pass http://upgrade_mgr;
        proxy_read_timeout 600s;
        client_max_body_size 2G;
    }

    # 制品上传
    location = /api/artifacts {
        proxy_pass http://upgrade_mgr;
        client_max_body_size 2G;
    }
}
```

### 4. 安装 Agent

```bash
# 方式1: 使用控制台生成的一键命令（推荐）
# 登录控制台 → 设置页 → Agent 注册令牌 → 复制安装命令 → 目标机器执行

# 方式2: 手动安装（下载均通过 proxy.xiaonuo.live 加速）
# 按需把 amd64 换成 arm64 / arm；CentOS 6 等老内核请使用 *-legacy 包
wget -q -c --no-check-certificate -O /tmp/upgrade-agent.tar.gz \
  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-agent.tar.gz -C /tmp
chmod +x /tmp/upgrade-agent-linux-amd64
# 无 Nginx 直连时把 --server 换成 http://<服务器IP>:8090
/tmp/upgrade-agent-linux-amd64 install --server https://upgrade.example.com --enroll-token <token>

# 检查服务状态
systemctl status upgrade-agent  # systemd
# 或
status upgrade-agent            # upstart
```

**Windows 主机（手动安装）**

```powershell
# 在管理员 PowerShell 中执行；Server 2008 R2 的 PowerShell 2.0 可用
$dir = "$env:TEMP\upgrade-agent-install"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'upgrade-agent-windows-amd64.exe'

# 从控制台自身下载，2008 R2 可用 HTTP，不依赖 GitHub/TLS 1.2。
# 控制台必须已在 dist 放入 upgrade-agent-windows-amd64.exe。
$server = 'http://your-server-ip:8090'   # 换成控制台实际地址
$url = "$server/api/agent/binary/windows-amd64"
(New-Object System.Net.WebClient).DownloadFile($url, $exe)

& $exe install --server $server --enroll-token <token>
Get-Service upgrade-agent
```

> 更简单的方式是在控制台「接入主机」选择 Windows 后复制一键命令（同样走
> `/api/agent/binary/windows-amd64`，无需手工下载）。
> 已启用 TLS 1.2 / .NET 4.5+ 的机器也可直接从 Release 下载 `.exe`；2008 R2 默认
> 建议走平台 HTTP，避免 `proxy.xiaonuo.live` 的 HTTPS 握手失败。
> 若只有旧 Release 的 `.zip`：PowerShell 5+ 用 `Expand-Archive`；PowerShell 2.0
> 用 `Shell.Application` 解压后再执行 `upgrade-agent-windows-amd64.exe`。
> Windows 的默认安装路径、服务名、卸载方式见上文「Windows 目标机接入」。

> **一键安装说明**：控制台生成的安装命令从 `server.agent_binary_dir`（默认 `./dist`）下载
> 对应平台的 Agent：Linux 为 `upgrade-agent-linux-<arch>`，Windows 为
> `upgrade-agent-windows-<arch>.exe`。systemd 部署请按上面步骤把二进制放到
> `/opt/upgrade-mgr/dist/`（服务的 `WorkingDirectory`），Docker 镜像只内置了 Linux 当前架构的
> Agent；接 Windows 主机需额外挂载 `dist` 目录。若自定义目录，请设置环境变量 `AGENT_BINARY_DIR`。

> **注册状态说明**：`install` 会在启动服务前把 `--server` / `--enroll-token` 写入
> `/var/lib/upgrade-agent/state.json`（0600；Windows 为
> `%ProgramData%\upgrade-agent\state.json`），因此 systemd/upstart/SCM 启动 `run` 时无需再带令牌。
> 已注册且凭据完整的 state 不会被重装覆盖；如需换服务器或重新注册，先执行
> `systemctl stop upgrade-agent && rm -f /var/lib/upgrade-agent/state.json`，
> 再在控制台生成新令牌（一次性，默认 15 分钟有效）并重新执行安装命令。
> 若日志出现 `HTTP 401`，通常是令牌已过期或已被使用；`HTTP 429` 是连续失败触发的限流。
>
> **可重复执行**：同一条安装命令可反复执行。安装器会先删除 `/tmp/upgrade-agent` 重新下载，
> 已注册的 `state.json` 会保留 `AgentID`/`Token`/`Secret`，不会重复注册；systemd 会先
> `reset-failed` 再 `restart`，因此失败重启计数不会阻止重装。
>
> **强制重新注册**：页面“接入主机”卡片勾选“强制重新注册”后生成的命令会带 `--force`，
> 覆盖目标机已有 state 并用新令牌重新注册。也可手动执行：
> `/tmp/upgrade-agent install --server '<url>' --enroll-token '<新令牌>' --force`。
> `--force` 必须同时提供 `--enroll-token`；普通重装请勿勾选，否则会生成新的 Agent 身份。

#### 连接地址覆盖与连通性测试

> **先厘清职责**：隧道由隧道软件（WireGuard / IPSec / frp / 专线 / SSH 端口转发等）
> 负责建立，平台**不参与建隧道**。平台是 Agent 主动外连服务端（长轮询），隧道只需
> 保证「Agent → 平台」可达，指令下发与日志回传复用同一条连接，无需平台反连主机。
>
> 因此大多数情况下**不需要**在平台里配置任何隧道参数：
> - 隧道透明（路由/内网 DNS 让原地址自动走隧道）→ 什么都不用配；
> - 安装时直接用隧道地址 `--server http://10.8.0.1:8090` → 也不用配。
>
> 只有当「同一批主机已在用 A 地址运行，但其中部分主机只能走 B 入口，又不方便重装
> 或逐台登录」时，才需要「连接地址覆盖」临时改写 Agent 的连接入口。

**字段与用途**

| 字段 | 含义 | 是否必需 |
|---|---|---|
| `hosts.tunnel_ip` | 主机在隧道网段里的 IP（如 `172.16.0.9`） | 可选，仅展示/筛选 |
| `hosts.server_url_override` | 临时改写该主机 Agent 连接平台的地址 | 可选，见上文条件 |
| `hosts.agent_server_url` | Agent 自报的当前连接地址 | 自动上报，用于确认切换 |
| `hosts.agent_default_server_url` | 安装时的默认地址 | 自动上报，用于补齐协议/端口 |

页面操作：主机页「编辑」→ 填「连接地址覆盖」→ 保存。

- 支持 `10.8.0.5`、`10.8.0.5:8090`、`https://mgr.example.com` 三种写法；
  只填 IP 时沿用安装时的协议与端口（默认地址无端口则按协议补 80/443）。
- 保存后 Agent 在下一次心跳（约 30 秒）自动切换并持久化到 state（`server_url`），
  后续心跳、取指令、制品下载全部走新地址。
- 清空该字段 → 心跳响应不再下发 → Agent 自动回退到 `default_server_url`。
- **失效保护**：覆盖地址连续 3 次心跳失败（约 90 秒）自动回退默认地址，并在 5 分钟内
  不再切回同一地址，避免填错导致 Agent 永久失联；之后会自动重新尝试。

**Agent 多地址 fallback（推荐）**

比服务端下发更可靠：安装时用逗号分隔多个地址，Agent 在默认地址不可达时自动轮换。

```bash
upgrade-agent install \
  --server 'https://mgr.example.com,http://10.8.0.1:8090' \
  --enroll-token <token>
```

- 第一个为当前地址，其余为备用；地址列表保存在 `state.json`（`server_urls`）。
- 默认地址连续 2 次心跳失败 → 轮换到下一个；覆盖地址连续 3 次失败 → 回退默认地址。
- 轮换是循环的，隧道/入口恢复后会自动回到可用地址。
- 该能力不依赖服务端配置，也不会因为服务端推送错误地址而失联。

**现场排查：`upgrade-agent status`**

```bash
upgrade-agent status                 # 人类可读
upgrade-agent status --json          # 便于脚本/巡检采集
upgrade-agent status --state /x.json # 指定状态文件
```

输出包含：版本/系统/init、出口 IP、注册状态、当前连接地址（并标注来源：
默认地址 / 服务端覆盖 / 备用地址）、默认地址、备用地址列表、最近心跳、连续崩溃次数、
待补报条数、**最近一次切换（从哪到哪、原因、时间）**、TLS 是否启用。
只读取本地 state，不访问服务端，因此断网也能用；绝不输出 token/secret。
未注册时退出码为 `2`，便于巡检脚本区分。

**手动拨测**

主机页「编辑」→ 点「测试连接地址」，服务端会按顺序执行
DNS 解析 → TCP 连接 → TLS 握手（https）→ `GET /health`，并逐步返回结果。

**保存后自动校验**：保存「连接地址覆盖」后会自动做两件事，无需手动点按钮：

1. **平台侧拨测**（异步）：立即写入 `hosts.tunnel_check_*`，列表显示「✓/✗ 平台侧自检」；
2. **主机侧自检**：服务端在心跳响应里下发 `tunnel_check_url`，Agent 从主机内部回连并
   把结果放在下一次心跳回报（`source=agent`），列表结论改为「主机侧自检」。
   10 分钟内未回报（主机离线/旧 Agent）则清除标记，保留平台侧结论。

列表对未通过的覆盖地址标红并在悬停时显示阶段明细；清空覆盖会自动清除检查结果。

- 通过：目标地址上的服务自报 `upgrade_mgr`，说明地址、端口、反向代理与证书都没问题。
- 失败：直接给出对应阶段的错误与排查建议（端口未放行、TLS 证书不匹配、反向代理指向了别的服务等）。
- 可以测试尚未保存的候选地址：先改输入框再点测试，结果满意后保存。
- **平台侧 vs 主机侧**：平台跑在容器里、隧道接口只挂在宿主机时，平台侧拨测可能失败
  而主机侧实际可达。此时以「主机侧自检」结论为准，平台侧失败仅作参考。

接口：`POST /api/hosts/{id}/tunnel-test`（管理员，租户隔离，审计 `host.tunnel_test`）

```json
// 请求（body 可省略，省略时测试已保存的 server_url_override）
{"target": "10.8.0.5:8090", "timeout_seconds": 5}
```

#### Agent 自升级（无需逐台登录）

Agent 注册/心跳会上报 `build_version`、自身二进制 SHA256（`build_hash`）和构建变体
（`build_variant`）。服务端从 `server.agent_binary_dir` 读取目标二进制并计算 SHA256，
二者一致就认为已是最新，不会重复下发。旧 Agent 没有这些字段，视为需要升级。

**触发方式**：

1. **手动批量/单台**：管理员在「主机」页点击「升级全部旧 Agent」，或在主机行点击
   「升级 Agent」。只下发仍不是目标哈希的 Agent；已有在途指令的主机会跳过。
2. **自动**：设置页把 `agent.auto_update` 设为 `true`（或配置 `agent.auto_update: true`）。
   Agent 注册/心跳时服务端发现旧版本即自动下发，每个 Agent 有 30 分钟冷却，避免失败后
   每次心跳反复重试。建议先确认 `dist` 中已放好目标版本再开启。

**执行流程**：服务端创建 `self_update` 指令 → Agent 下载并校验 SHA256 → 备份当前二进制
→ 原子替换 → `exit(75)` → systemd/upstart 重启 → 新 Agent 心跳上报新 `build_hash` →
服务端用指令中的目标哈希精确匹配并标记 `success`、清除维护窗口。Agent 不需要在退出前
上报 success，因此即使 `exit(75)` 立即退出也能正确闭环。

**旧 Agent 签名路径自动探测**：旧 Agent 注册时默认按 legacy `id` 签名。若目标 Agent 实际
只支持 `signed_path`，首次会以「签名不匹配」拒绝；服务端自动把该 Agent 的 legacy 签名路径
切换为 `signed_path` 并重排一次，第二次仍拒绝才标记失败，避免死循环。整个过程无需手工
修改 `agents.signature_modes`。

**载荷字段兼容**：`self_update` / `upgrade` 等指令的载荷会省略空的 `command` 字段。
v0.512d1f9 这一版 Agent 曾把 `Command` 写成值类型，序列化时总会补出
`"command":{"script":""}`；服务端通过 `optional_command` 能力识别该版本，并按旧字节
签名，否则该版本连自我升级都会被拒（「signer: 签名不匹配」）。修复后的 Agent 上报
`optional_command`，服务端恢复正常签名。

**相关接口**：

```http
POST /api/agents/self-update           # {host_ids:[1,2]} 或 {"all_outdated":true}，管理员
GET  /api/agents/self-update           # 目标版本、可用二进制、最近 50 条升级记录
```

下载地址由服务端生成绝对 URL：`/api/agent/binary/<os>-<arch>`（`linux` / `windows`），
Linux legacy 追加 `-legacy`；Windows 文件名带 `.exe`。服务端按主机上报的 `os_family`
自动选择平台二进制，同一个 fleet 可混合 Linux 与 Windows 主机。
若使用 Nginx/域名，请正确设置 `server.external_url`，否则 Agent 可能无法访问下载地址。

### 5. windows_exporter 安装示例（Windows 主机）

与 `node_exporter` 示例完全对齐：仓库自带单文件 `examples/windows_exporter/install.ps1`
（内嵌默认配置），支持 `install / update / uninstall / status` 四个动作，并可在控制台
「组件」页点示例一键填好。

- 二进制来源：`iflyelf/windows_exporter` 的 `latest` 发布（`amd64` / `arm64`），
  经 `proxy.xiaonuo.live` 代理下载，兼容 Server 2008 R2 ~ Windows 11 的 PowerShell 2.0；
- 安装位置：`C:\Program Files\windows_exporter`，服务名 `windows_exporter`
  （SCM，自动启动 + 失败重启 5s/10s/30s）；
- 监听端口：**16666**（默认），防火墙入站规则自动创建；
- textfile 目录：`C:\monitor\windows_exporter\textfile_inputs`
  （原 `C:\zabbix_agentd\...` 已按你的要求改为 `monitor`）；
- 配置：`C:\Program Files\windows_exporter\config.yaml`，仓库示例见
  `examples/windows_exporter/config.yaml`（含 `service.exclude` 大清单，可直接用）。

| 动作 | 说明 |
|---|---|
| `install` | 安装或覆盖升级：下载 exe → 备份旧版本 → 替换 → 建服务 → 建防火墙规则 → 启动 |
| `update` | 备份旧 exe → 替换 → 重启；启动失败自动回滚旧版本 |
| `uninstall` | 停止并删除服务、防火墙规则、安装目录（`-Yes` 跳过确认，`-KeepData` 保留配置） |
| `status` | 输出服务状态、启动类型、版本、路径与 `/metrics` 连通性 |

**控制台操作**（两种方式，与 Linux 示例一一对应）：

1. **脚本方式（推荐）**：「组件」页 → Windows 分组 →「脚本安装 / 脚本更新 / 脚本卸载」
   → 保存；再到「任务」页选中对应组件启动。组件名分别为
   `windows_exporter_script` / `windows_exporter_script_update` / `windows_exporter_script_uninstall`。
2. **命令任务方式**：「任务」页类型选「执行命令/脚本」→ Windows 分组 →「安装脚本 / 更新脚本 / 卸载脚本」。
   生成的脚本会自动下载 `install.ps1` 并以 `install / update / uninstall -Yes` 执行，
   解释器预填 `powershell.exe`。

手工在目标机执行：

```powershell
# 管理员 PowerShell（PowerShell 2.0 亦可）
$url = 'https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/windows_exporter/install.ps1'
$f = Join-Path $env:TEMP 'windows_exporter-install.ps1'
(New-Object System.Net.WebClient).DownloadFile($url, $f)

& $f install                       # 安装/覆盖升级
& $f update                        # 升级（失败自动回滚）
& $f status                        # 查看状态
& $f uninstall -Yes                # 卸载（-KeepData 保留配置）
```

常用参数：

```powershell
& $f install -ListenPort 9182                  # 改端口
& $f install -TextfileDir 'D:\metrics'        # 改 textfile 目录
& $f install -ConfigPath 'C:\dist\config.yaml' # 使用自定义配置
& $f install -Arch arm64                       # Windows on ARM
& $f install -NoFirewall                       # 不创建防火墙规则
```

> `-Version latest` 默认取 `iflyelf/windows_exporter` 的 latest；`-Flavor legacy`
> 固定取该仓库的 `windows_exporter-<arch>-legacy.exe`（v0.23.1 + Go 1.20）。
> 也可用环境变量覆盖：`WINDOWS_EXPORTER_VERSION`、`WINDOWS_EXPORTER_FLAVOR`、
> `WINDOWS_EXPORTER_PROXY`、`WINDOWS_EXPORTER_BASE`、`WINDOWS_EXPORTER_LISTEN_PORT`。

#### 5.1 平台/目标机都无法出网（Server 2008 R2 常见）

Server 2008 R2 的 PowerShell 2.0/CLR 2.0 不支持 TLS 1.2，无法直连 GitHub；
且 Go 1.21 起构建的 windows_exporter 不再支持 Server 2008 R2。`install.ps1`
会按系统版本自动选择通道：

- Windows 10 / Server 2016+ → `latest`：`windows-exporter-amd64` / `-arm64`；
- Windows 7 / 8 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 → `legacy`：
  `windows-exporter-amd64-legacy` / `-arm64-legacy`（官方 v0.23.1 + Go 1.20，
  并包含本仓库移植的 `hardware_info` 采集器）。

如果控制台所在网络也不能访问 GitHub，平台代缓存会返回 **502**。此时在页面上传
对应通道的离线文件即可：

1. 在一台可上网的机器下载：
   - 旧系统：`windows_exporter-amd64-legacy.exe`
     （`https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64-legacy.exe`）；
   - 新系统：`windows_exporter-amd64.exe`（或 `arm64`）。
2. 控制台 →「外部文件」→ 找到 `windows-exporter-amd64-legacy`（旧系统）或
   `windows-exporter-amd64`（新系统）→ 点「上传离线文件」，选择下载好的 exe。
   升级后的服务端会在已有基础条目旁自动补 legacy 条目（迁移 028）。
3. 直接重跑组件安装/任务。`install.ps1` 会请求对应通道的
   `http://<控制台>/api/external/binary/<asset>?local=1`：
   - 已上传：立即返回本地文件，**不需要平台出网**；
   - 未上传：几秒内返回 404，脚本报错并提示上传，不会再等平台现场下载 GitHub；
   - 也可先在外部文件页点「刷新缓存」让平台预下载（平台能出网时），之后 `?local=1` 直接命中缓存。
4. 如需离线 `install.ps1` 本身，可把脚本上传到 `windows-exporter-install.ps1` 同名条目，
   或继续作为组件制品上传。
5. `legacy` 默认配置已启用 `hardware_info`，安装完成后即可在 `/metrics` 看到
   `node_hardware_*` 指标。若自行替换为**官方 v0.23.1**（不含该采集器），
   请从 `config.yaml` 的 `collectors.enabled` 删除 `hardware_info`，或使用
   `-NoConfig` 配合自备配置；否则服务会因未知采集器启动失败。
   推荐直接使用本仓库 legacy 资产，或 `-ConfigPath examples\windows_exporter\config-legacy.yaml`。

列表「本地离线」列会显示已上传大小；「清除离线文件」后回退到平台缓存/上游下载。

> **关键修复**：GitHub Release 资产会 302 到 `release-assets.githubusercontent.com`，
> 旧版默认域名白名单没有它，平台会以「重定向目标被拒绝」返回 502。当前默认已包含
> `release-assets.githubusercontent.com`、`github-releases.githubusercontent.com`、
> `objects.githubusercontent.com`；外部文件缓存默认先经
> `https://proxy.xiaonuo.live/proxy?url=` 拉取，失败再直连兜底。
> 可在「设置 → 外部文件代缓存」调整 `server.external_binary_proxy` /
> `server.external_binary_proxy_hosts`；如果配置过 `server.external_binary_hosts`，
> 请把上述域名一并加入，或留空使用默认值。

组件健康检查与版本检查使用 PowerShell（与 Agent 的 Windows 解释器一致）：

```
health_cmd : [["powershell.exe","-NoProfile","-Command","if ((Get-Service windows_exporter -ErrorAction Stop).Status -ne 'Running') { exit 1 }"]]
version_cmd: [["powershell.exe","-NoProfile","-Command","(Get-Item 'C:\Program Files\windows_exporter\windows_exporter.exe').VersionInfo.FileVersion"]]
```

> 只在 Windows 主机上执行：任务按 `os_family=windows` 选主机（本平台任务筛选支持），
> 或直接填 Windows 主机的 IP。Linux 主机执行 PowerShell 脚本会明确报错。

### 5. node_exporter 安装示例（可选，推荐脚本安装）

控制台「组件」页点「填入示例：脚本安装 / 脚本更新 / 脚本卸载」会自动填好组件；
`examples/node_exporter/install.sh` 是**单文件自包含**安装脚本，已内嵌
systemd unit 与 SysV init，上传后不需要同目录的其他文件。

#### 5.1 脚本执行开关（默认开启）

`script` 模式会下载并执行上传的脚本，属于任意命令执行。`agent.allow_exec_mode`
默认开启，开箱即可使用；如需收紧权限，可到「设置 → Agent」把它设为 `false` 并保存，
关闭后 `script/exec/replace_exec` 模式与命令任务都会被拒绝。单实例保存后即时生效；
多副本环境需让各副本重新加载设置（滚动重启或刷新）。

> 这是安全开关，仅建议在可信环境开启；必须配合 RBAC，只给可信管理员上传/创建任务的权限。

#### 5.2 组件：填入脚本安装示例

「组件」页的脚本模式提供三个示例，与「任务」页示例一一对应，都只填 `script_url`，
用 `exec_args` 区分动作：

| 组件示例按钮 | 组件名 | 显示名称 | `exec_args` | 适用场景 |
| --- | --- | --- | --- | --- |
| 填入示例：脚本安装 | `node_exporter_script` | `Node Exporter（脚本安装）` | `["install"]` | 首次安装或覆盖升级 |
| 填入示例：脚本更新 | `node_exporter_script_update` | `Node Exporter（脚本更新）` | `["update"]` | 备份旧二进制，启动失败自动回滚 |
| 填入示例：脚本卸载 | `node_exporter_script_uninstall` | `Node Exporter（脚本卸载）` | `["uninstall","-y"]` | 非交互停止并删除服务、文件 |

> 脚本卸载示例不带服务名、`health_cmd`、`version_cmd`：卸载后服务已不存在，
> 保留这些字段会让平台在卸载后尝试启服并做健康检查，任务必然失败。

点「新建组件」→「填入示例：脚本安装」→ 保存。自动填好的关键字段：

| 字段 | 值 |
| --- | --- |
| 名称 | `node_exporter_script` |
| 显示名称 | `Node Exporter（脚本安装）` |
| 部署模式 | `script` |
| 安装路径 | `/opt/monitor/exporter`（仅说明；实际路径固定在脚本内） |
| 服务名 | `node_exporter` |
| 服务管理器 | `auto` |
| 脚本下载地址 | 已预填真实可用的 `script_url`，无需上传制品 |
| 高级配置 JSON | 已预填 `script_url`、`exec_env`、`exec_timeout`、`health_cmd` 与 `version_cmd`，可直接保存 |
| post_install 脚本 | 已预填一段可选的“安装后检查”示例，可直接修改或删除 |

示例使用的真实脚本地址（通过 `proxy.xiaonuo.live` 代理本仓库 `main` 分支文件）：

```text
https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh
```

可直接打开验证：
<https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh>

> 下载统一使用 `proxy.xiaonuo.live`；如代理不可用，可换用内部镜像或直连 GitHub。

点“填入脚本安装示例”后，高级配置 JSON 的完整内容如下，可直接参考修改：

```json
{
  "exec_timeout": "10m",
  "exec_env": ["NODE_EXPORTER_VERSION=latest"],
  "script_url": "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh",
  "version_cmd": [["/opt/monitor/exporter/node_exporter", "--version"]],
  "health_cmd": [["pgrep", "-x", "node_exporter"]]
}
```

目标机需要能访问 `proxy.xiaonuo.live`。脚本本身也会通过同一代理下载
`iflyelf/node_exporter` 的 `latest` 静态二进制（当前验证为 `1.12.1`，仅提供
`amd64` / `arm64`）。如果不想依赖外链，删除 `script_url` 后按 5.3 上传
`examples/node_exporter/install.sh` 即可。

`post_install 脚本` 文本框同时填入：

```sh
#!/bin/sh
# post_install 脚本示例（可选）：在安装脚本执行后由 Agent 运行。
# 可在这里补充配置、注册服务或做安装后检查；不需要时整段删除。
if [ -f /etc/systemd/system/node_exporter.service ]; then
  echo "node_exporter systemd unit found"
elif [ -f /etc/init.d/node_exporter ]; then
  echo "node_exporter SysV init found"
else
  echo "WARN: node_exporter service file not found"
fi
```

`script` 模式不会替换 `install_path`，也不会做平台侧二进制备份；安装、升级、
服务注册全部由脚本完成。首次安装时服务还不存在，Agent 会跳过停服，脚本执行完
后再检查并启动服务。

脚本模板默认已填 `health_cmd`（`pgrep -x node_exporter`）与 `version_cmd`
（`/opt/monitor/exporter/node_exporter --version`）。不需要平台层检查时，可从高级配置
JSON 中删除对应字段。

脚本更新示例（`node_exporter_script_update`）与安装示例同源，`exec_args` 为
`["update"]`，脚本会先备份旧二进制再替换，启动失败自动回滚；健康检查、版本确认与
安装示例一致。脚本卸载示例（`node_exporter_script_uninstall`）`exec_args` 为
`["uninstall","-y"]`，**不带**服务名、`health_cmd`、`version_cmd`，避免卸载后平台
再次启服或健康检查失败。

##### script 模式：自定义下载与执行

`script` 模式不设置 `script_url` 时从制品库下载脚本；设置后直接从该 HTTP(S) 地址
下载。node_exporter 示例已经预填真实地址：

```json
{
  "script_url": "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh",
  "exec_args": ["amd64"],
  "exec_env": ["NODE_EXPORTER_VERSION=latest"],
  "exec_timeout": "10m"
}
```

`exec_args` 示例里的 `amd64` 只用于演示；不传时脚本会用 `uname -m` 自动识别架构。

- `script_url`：绝对 `http://` / `https://` 地址。设置后不再查找/上传制品，任务版本
  仅用于展示与记录；Agent 直接从该地址下载。为空时沿用制品库。
- `exec_args` / `exec_env` / `exec_timeout`：传给下载后的脚本，分别控制参数、
  环境变量和执行超时。
- `work_dir`：可选的工作目录，必须已存在；不填则继承 Agent 进程目录。
- Agent 不会把自身的 Agent token 发送到自定义 `script_url`；请确保目标机能直接
  访问该地址（必要时在 URL 中自带凭据）。私有脚本仍建议上传为制品。
- `script_url` 需要同步更新目标机 Agent。旧版 Agent 不认识该字段，会因没有制品
  而下载失败（不会向自定义地址发送 Agent 凭据）。
- 自定义地址下载默认没有 MD5/SHA256 校验；需要完整性校验时请使用制品上传。

`post_install_script` 是独立的脚本文本框，主要用于 `replace` / `replace_exec` 等需要
“替换二进制后再注册服务”的场景；`script` 模式如果也配置了，会在安装脚本之后执行。
Agent 会在 `post_install` 命令之后把脚本写入
备份目录的临时文件，用 bash/sh 执行；脚本失败会触发平台回滚。脚本内容不会写入
高级配置 JSON。该脚本同样属于任意命令执行，只应允许可信管理员编辑组件。
使用前需同步更新目标机 Agent：旧版 Agent 不认识该字段，会跳过脚本。

#### 5.3 组件：只执行命令示例（exec + script_url）

「组件」页与「任务」页的示例一一对应，都是 **`script_url` + 一个动作参数**，
不再需要把整段 shell 写进 `exec_args`：

| 组件示例按钮 | 组件名 | `exec_args` | 适用场景 |
| --- | --- | --- | --- |
| 填入示例：只执行命令（安装） | `node_exporter_cmd` | `["install"]` | 首次安装或覆盖升级 |
| 填入示例：只执行命令（更新） | `node_exporter_update` | `["update"]` | 备份旧二进制，启动失败自动回滚 |
| 填入示例：只执行命令（卸载） | `node_exporter_uninstall` | `["uninstall","-y"]` | 停止并删除服务、文件 |

「任务」页对应提供「填入示例：安装组件 / 更新组件 / 卸载组件」，选中上述任一组件即可，
动作由组件自身配置决定。

安装示例的关键字段：

| 字段 | 值 |
| --- | --- |
| 部署模式 | `exec` |
| `script_url` | `https://proxy.xiaonuo.live/?url=.../install.sh` |
| `exec_args` | `["install"]`（更新用 `["update"]`，卸载用 `["uninstall","-y"]`） |
| 安装路径 | `/opt/monitor/exporter/node_exporter`（可选；exec 模式可不填） |
| 服务名 | 留空（服务由脚本自行启停，避免平台重复操作） |
| 健康检查 | `pgrep -x node_exporter` |
| 版本确认 | `/opt/monitor/exporter/node_exporter --version` |

执行流程：

1. 服务端发现 `exec` 模式且配置了 `script_url`，跳过制品库直接下发。
2. Agent 先下载 `script_url` 到本地临时文件（不带平台凭据），
   再按 `exec_args` 作为脚本参数执行，等价于 `sh install.sh install`。
3. 后续与 `script` 模式一致：不备份、不替换二进制，服务启停与回滚由脚本负责。

> 卸载示例用 `uninstall -y` 跳过确认（exec 模式没有 stdin），且**不带**
> `health_cmd` / `version_cmd`：卸载后服务已不存在，保留健康检查必然失败。

> 若确实需要在内联命令里使用管道、重定向、进程替换，`exec_args` 仍可直接写
> `["-c","..."]`（Agent 直接 exec，不经过 shell）。但这类写法可读性差，
> 推荐优先使用 `script_url`。以上模式同样受 `agent.allow_exec_mode` 控制。

#### 5.4 制品（可选）：上传安装脚本

脚本示例默认使用 5.2 中真实可用的 `script_url`，通常不需要本节。如果不想依赖
外链，先删除组件高级配置 JSON 里的 `script_url`，再进入「制品」页：

1. 组件选 `node_exporter_script`（脚本安装示例的组件名）。
2. 版本填 `latest`（创建任务时必须选同一个版本）。
3. 架构选 `all（通用脚本）`。脚本会在目标机执行 `uname -m` 自动识别架构。
4. 上传仓库里的 `examples/node_exporter/install.sh`。
5. `os_family` 留空即可。

> 不需要另外上传 node_exporter 二进制。脚本会自己从
> `proxy.xiaonuo.live` 代理下载 iflyelf/node_exporter latest 静态编译二进制。

此时 Agent 从制品库下载脚本，不再访问 `script_url`；任务版本需要和制品版本一致。

#### 5.5 任务：安装到目标机

1. 「任务」页选组件 `node_exporter_script`（脚本安装）或 `node_exporter_cmd`（只执行命令-安装），版本 `latest`。
2. 填 IP 段，例如 `10.1.255.24-26` 或 `10.1.255.0/24`。
3. 先点「预览」确认命中主机和分批，再「创建任务」→「启动」。
4. 想先装一台：策略选「金丝雀」、首批 1 台、取消「自动推进」；首批成功后点「推进」。

Agent 执行顺序：下载安装脚本 → 跳过平台备份 → `pre_install` → 停旧服务
（存在时）→ 用 Shell 执行脚本（优先 bash，缺失时 sh）→ 检查/启动服务 → `post_install`
（命令数组；如配置了 `post_install_script` 则随后执行）→ 健康检查（如配置了 `health_cmd`）
→ 版本确认（如配置了 `version_cmd`）。脚本输出会记录在任务日志里。

#### 5.6 脚本做了什么

5.2 的 `script_url` 直接指向本仓库 `main` 分支的
`examples/node_exporter/install.sh`，所以不需要先上传制品。`install.sh` 是单文件脚本，
支持 `install` / `update` / `uninstall` / `status` 四个动作，会：

- 按 `uname -m` 识别 `amd64` / `arm64` / `arm` / `386`（也可用
  `NODE_EXPORTER_ARCH`、`--arch` 或第一个参数覆盖）。
- 从 `https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/node_exporter/releases/download/latest/...`
  下载静态二进制，安装到 `/opt/monitor/exporter/node_exporter`。
- 创建 metrics 目录 `/opt/monitor/exporter/metrics`。
- 检测目标机 init：
  - systemd：写 `/etc/systemd/system/node_exporter.service`，
    `daemon-reload`、`enable`、`restart`。
  - SysV/upstart：写 `/etc/init.d/node_exporter`，用
    `chkconfig` / `update-rc.d` / `insserv` 注册，再启动。
- 输出服务状态并请求 `http://127.0.0.1:16666/metrics` 做初步验证。

四个动作的差异：

| 动作 | 行为 |
| --- | --- |
| `install`（默认） | 下载并安装/覆盖二进制，写入 unit/init，启用并启动服务 |
| `update` | 备份旧二进制到 `node_exporter.bak`，替换后重启；新版本启动失败自动回滚旧二进制 |
| `uninstall` | 停止服务、注销自启、删除 unit/init 与二进制；默认需确认，`-y` 跳过确认，`--keep-data` 保留 metrics/日志 |
| `status` | 输出安装路径、版本与服务状态，未安装时退出码为 3 |

支持的发行版（实际以目标机 init 检测结果为准）：

| 发行版 | 适用版本 | init | 服务文件 |
| --- | --- | --- | --- |
| CentOS / RHEL | 6.x | upstart + SysV | `/etc/init.d/node_exporter` |
| CentOS / RHEL / Rocky / openEuler | 7.x+ / 8.x+ / 20.03+ | systemd | `/etc/systemd/system/node_exporter.service` |
| Ubuntu | 14.04 | upstart + SysV | `/etc/init.d/node_exporter` |
| Ubuntu | 16.04+ | systemd | `/etc/systemd/system/node_exporter.service` |
| Debian | 7.x | SysV | `/etc/init.d/node_exporter` |
| Debian | 8.x+ | systemd | `/etc/systemd/system/node_exporter.service` |
| SLES | 11 SP1-SP4 | SysV | `/etc/init.d/node_exporter` |
| SLES / openSUSE | 12.x+ / 15.x+ | systemd | `/etc/systemd/system/node_exporter.service` |

也可以在目标机手动执行同一个脚本：

```bash
sudo bash examples/node_exporter/install.sh install
sudo bash examples/node_exporter/install.sh update      # 升级，失败自动回滚
sudo bash examples/node_exporter/install.sh status      # 查看状态与版本
sudo bash examples/node_exporter/install.sh uninstall   # 交互确认；-y 跳过确认，--keep-data 保留数据

# 指定架构/版本（可选）
sudo bash examples/node_exporter/install.sh install --arch arm64 --version latest
sudo NODE_EXPORTER_ARCH=arm64 NODE_EXPORTER_VERSION=latest \
  bash examples/node_exporter/install.sh
```

#### 5.7 验证与注意事项

- systemd：`systemctl status node_exporter`，日志 `journalctl -u node_exporter`。
- SysV/upstart：`service node_exporter status` 或
  `/etc/init.d/node_exporter status`，日志 `/var/log/node_exporter.log`。
- 指标：`wget -qO- http://127.0.0.1:16666/metrics | head`。
- 远程抓取放行 `16666/tcp`：firewalld 用
  `firewall-cmd --add-port=16666/tcp --permanent && firewall-cmd --reload`；
  Ubuntu 可用 `ufw allow 16666/tcp`；SUSE 可用 YaST 防火墙。

注意：

- `script` 模式执行下载的脚本（以及「命令任务」执行任意 Shell 命令），服务端被攻破时
  风险等同于任意命令执行；务必依赖 `agent.allow_exec_mode` 和 RBAC，只允许可信管理员操作。
  该开关默认开启，生产环境如需最小权限请主动关闭。
- `script` / `exec + script_url` 模式不做平台侧备份/自动回滚；需要平台备份和回滚时，改用
  「填入示例：node_exporter_binary（二进制替换）」，按主机架构上传二进制。该示例把
  服务注册逻辑放在独立的 `post_install_script` 字段中，Agent 在 post_install
  阶段写入临时文件并执行，不需要再往高级配置 JSON 里塞大段 shell。
- `script_url` 只支持绝对 HTTP(S) 地址，且不会携带 Agent 凭据；目标机必须能
  直接访问该地址。node_exporter 示例依赖 `proxy.xiaonuo.live`，内网受限时请删除
  `script_url` 并改用 5.3 的制品上传。
- 自定义地址下载不校验 MD5/SHA256；示例脚本在运行时自行下载二进制。对完整性校验
  有要求时，用制品上传安装脚本，并按需给二进制制品填写 MD5/SHA256。
- `script_url` 设置后任务不再依赖制品；任务版本仍会记录，但不会用于匹配文件。
  需要按任务版本下载不同脚本时，请为不同版本准备不同的组件/`script_url`，
  或继续使用制品库。
- 脚本会覆盖已有的同名 unit/init，以保证服务参数与当前示例一致；如需保留自定义
  服务文件，请先备份或改脚本。
- 当前 iflyelf latest 通常只提供 `amd64` / `arm64`；`arm` / `386` 若下载 404，
  请自行静态编译后改用二进制替换模式上传。
- systemd unit 的 `MemoryMin` / `MemoryMax` 默认已注释；如需限制，按机器规格取消注释。
  `MemoryMin` 需要 cgroup v2，小内存机器强行设置可能导致启动失败。
- 默认参数包含 `--collector.processes` / `--collector.tcpstat`；若换成不支持这两个
  参数的更老版本，启动报 `unknown collector` 时删除这两个参数。
- `examples/node_exporter/node_exporter.service` 和 `.init` 现在保留作参考；
  实际平台安装只需要上传 `install.sh`。

### 6. 执行命令 / 脚本任务

除了组件任务，「任务」页的「任务类型」还可以选 **执行命令/脚本（command）**：
选中主机后下发一段 Shell 脚本，一次性执行并回收输出，适合卸载旧监控、清理残留、
临时巡检等场景。命令任务复用同一套任务列表、批次灰度、暂停/取消/重试与实时日志。

使用步骤：

1. 「任务」页把「任务类型」切到「执行命令/脚本」。
2. 填「命令脚本」（原样保存到目标机临时文件后执行，支持管道、重定向、进程替换）。
3. 按需填写：
   - **解释器**：留空自动 `bash`，缺失时回退 `sh`；只允许 `bash/sh/dash/ksh/zsh`。
   - **脚本参数**：按空格分隔，作为 `$1`、`$2`… 传入。
   - **标准输入**：如 `Y\n`，等价于 here-string，用于交互确认。
   - **分配伪终端（PTY）**：脚本必须直接读写 `/dev/tty` 时勾选。
   - **超时**：默认 `10m`，支持 `30m` 等 Go duration 格式。
4. 填 IP 段，点「预览」→「创建任务」→ 列表里点「启动」。

「任务」页在命令任务模式下提供三个示例按钮，覆盖安装脚本的三种动作：

| 按钮 | 脚本动作 | 说明 |
| --- | --- | --- |
| 填入示例：安装脚本 | `bash "$f" install` | 首次安装或覆盖升级（幂等），不需要 stdin |
| 填入示例：更新脚本 | `bash "$f" update` | 备份旧二进制，启动失败自动回滚，不需要 stdin |
| 填入示例：卸载旧监控 | `bash "$f" uninstall` | 交互确认，标准输入填 `Y\n` |

三个示例共用的骨架（以卸载为例）：

```bash
#!/bin/bash
set -e
url='https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh'
f=$(mktemp /tmp/node_exporter-install.XXXXXX.sh)
trap 'rm -f "$f"' EXIT
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$f"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$f" "$url"
else
  echo "需要 curl 或 wget 下载安装脚本" >&2; exit 1
fi
bash "$f" uninstall
```

只把最后一行换成 `install` / `update` / `status` 即可切换动作。卸载示例需配合
标准输入 `Y\n`：`install.sh` 的 `uninstall` 动作直接从 stdin 读取确认，无需
「分配伪终端」。若脚本用 `read -e -p "[Y/N]:"` 确认后又用 `dd if=/dev/tty`
等待回车，可勾选「分配伪终端（PTY）」，这种「PTY + 标准输入」组合可以正常通过。

> 不要把 `script -q -c "bash <(curl ...) uninstall" /dev/null` 直接填进「命令脚本」：
> `script(1)` 的 `-c` 参数会交给 `/bin/sh` 解析，而进程替换 `<(...)` 是 bash 语法，
> 在 dash/ash 环境下会报 `Syntax error: "(" unexpected`。上面的「先下载再执行」写法
> 与 shell 无关，最稳妥。

执行日志会随阶段上报实时写入任务详情，页面每 4 秒自动刷新；执行完成后按退出码
标记成功/失败，失败主机可以直接「重试」。

前提与限制：

- `agent.allow_exec_mode` 必须开启（**默认开启**）；关闭后创建与下发都会拒绝。
- 需要新版 Agent（上报 `run_command` 能力）。旧 Agent 会被自动跳过，并在该主机日志里
  写明「Agent 版本过低，不支持执行命令任务；请先升级 Agent」。
- 命令任务不绑定组件/制品，版本固定记为 `command`，也不写 Consul Meta。

API 创建示例：

```json
{
  "kind": "command",
  "name": "卸载旧监控",
  "selector": { "ip_expr": "10.1.255.24-26" },
  "strategy": { "mode": "count", "batch_size": 10, "auto_promote": true },
  "version": "command",
  "command": {
    "script": "bash /tmp/node_exporter-install.sh install",
    "interpreter": "/bin/bash",
    "args": [],
    "stdin": "Y\n",
    "tty": true,
    "timeout": "30m"
  }
}
```

---

## 架构设计

### 总体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                          控制台 (upgrade-mgr)                    │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐       │
│  │  认证    │  │  任务    │  │  组件    │  │ Agent GW │       │
│  │ (内置/SSO)│  │ 灰度编排 │  │  制品    │  │ 长轮询   │       │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘       │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │              PostgreSQL (hosts/tasks/commands)            │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                             │
                             │ HTTP/HTTPS (长轮询)
                             ▼
        ┌────────────────────────────────────────────┐
        │        Agent 集群 (upgrade-agent)           │
        │  ┌──────┐  ┌──────┐  ┌──────┐  ┌──────┐  │
        │  │ host1│  │ host2│  │ host3│  │ host4│  │
        │  └──────┘  └──────┘  └──────┘  └──────┘  │
        │    服务A      服务A      服务B      服务C   │
        │  Linux(systemd/upstart) · Windows(SCM)     │
        └────────────────────────────────────────────┘
```

控制台只主动**下发**指令，所有连接均由 Agent 发起（长轮询）。因此：

- 主机无需开放入站端口，NAT/多网卡/隧道场景只需保证 Agent 能访问控制台；
- 控制台需提供目标平台对应的 Agent 二进制（`server.agent_binary_dir`）；
- Windows 主机以 SCM 服务运行，与 Linux 的 systemd/upstart 处于同一套编排流程。

### 核心特性

| 特性 | 说明 |
|------|------|
| **灰度编排** | 金丝雀/定数/百分比分批，批间健康门禁，自动/人工推进 |
| **安全加固** | HMAC 指令签名+过期校验，Agent 验签拒伪造/重放 |
| **原子升级（replace）** | 备份→停服→替换→启服→健康检查，失败自动回滚；script 模式由安装脚本自行处理备份/回滚 |
| **崩溃自愈** | Watchdog 心跳检测，连续崩溃自动回滚旧版本 |
| **断点续传** | 制品下载支持 HTTP Range，大文件续传节省带宽 |
| **制品并行上传** | 组件共享锁 + 制品身份独占锁，不同制品并行；删除/清空组件与上传互斥；跨副本 PostgreSQL advisory lock |
| **配置热更新** | 制品上传并发/锁等待保存即生效，多副本约 5 秒同步；连接池按上传并发数自适应 |
| **零依赖** | 静态编译单二进制，无 Python/Ruby 依赖 |

### 数据模型

**核心表**：
- `components`：组件定义（被升级的服务/二进制）
- `artifacts`：制品仓库（版本×架构的二进制文件或安装脚本；`arch=all` 表示通用脚本）
- `hosts`：主机清单（可选同步 Consul）
- `upgrade_tasks`：升级任务（批量升级编排）
- `task_hosts`：任务关联主机（逐机状态追踪）
- `task_batches`：批次元数据
- `commands`：Agent 待执行的指令
- `agents`：Agent 实例

**状态机**：
- 任务：draft → running → (paused) → success/failed/cancelled
- 主机：pending → running → success/failed/rolled_back/timeout/skipped
- 批次：pending → running → success/failed
- 指令：pending → dispatched → accepted/rejected → running → success/failed

---

## API 参考

### 认证

所有需要认证的 API 使用 Cookie Session（内置认证）或 Bearer Token（Casdoor SSO）。

**登录**：
```http
POST /api/auth/login
Content-Type: application/json

{
  "username": "admin",
  "password": "your-password"
}
```

### 组件管理

```http
GET /api/components                       # 列出组件
POST /api/components                      # 创建组件
GET /api/components/{id}                  # 获取详情
PUT /api/components/{id}                  # 更新组件
GET /api/components/{id}/references       # 查询引用该组件的任务（删除前核对）
DELETE /api/components/{id}               # 删除组件（被任务引用时拒绝）
DELETE /api/components/{id}?cascade_tasks=true  # 连带删除引用任务后删除组件
```

删除组件的行为：

- 不带 `cascade_tasks`：只要仍有任务引用（含已成功/已取消的历史任务）就拒绝，并返回引用数量。
  先在「任务」页用“按组件筛选”核对，再逐个删除任务即可。
- 带 `cascade_tasks=true`：在同一事务内先删引用任务（批次、逐机状态、指令日志随之级联），
  再删组件；**存在 running / paused 任务或未完成指令时仍拒绝**，需先取消任务。
- 前端会先调用 `GET /api/components/{id}/references` 展示引用任务清单，并通过“确认框 + 输入组件名”
  做二次确认；无论成功失败都会写审计日志（`component.delete`，详情包含 `cascade_tasks`、
  `deleted_tasks`、`referenced_tasks`）。
- 制品文件在预检通过后才清理，避免组件删不掉但文件已丢。

### 制品管理

```http
GET /api/artifacts?component_id=1      # 列出制品
POST /api/artifacts                     # 上传制品（multipart/form-data）
GET /api/artifacts/{id}/download        # 下载制品（支持 Range）
```

上传并发与锁模型：

- 每个副本最多同时处理 `artifact.max_concurrent_uploads` 个上传（默认 4，范围 1-64）；槽位在解析 multipart 前获取，避免排队请求先写满临时文件。
- 上传持「组件共享锁 + 制品身份独占锁」：同组件不同制品可并行，同一 `component/version/arch/os` 仍串行，避免文件与 MD5/SHA256 元数据错配。
- 删除/清空组件持组件独占锁：等待在途上传完成并阻止新上传，避免删完又被写回。
- 跨副本通过 PostgreSQL advisory lock 互斥，锁等待上限 `artifact.lock_wait`（默认 30m，范围 1s-24h）。
- 每个并发上传占用 1 条数据库连接；`database.max_open_conns` 会自动抬到至少“并发上传数 + 4”。
- 存储路径为 `<component_id>/<version>-<os_family>-<arch>/<filename>`；OS 维度参与路径，避免不同系统同名文件互相覆盖。

### 任务管理

```http
POST /api/tasks/preview          # 预览任务（展开 IP、批次划分）
POST /api/tasks                  # 创建任务
GET /api/tasks                   # 列出任务（支持 kind / component_id / status 筛选）
GET /api/tasks?component_id=1    # 只看引用指定组件的任务（配合删除组件前核对）
GET /api/tasks/{id}              # 任务详情（批次/逐机/日志，host_page/host_size 分页）
POST /api/tasks/{id}/start       # 启动任务
POST /api/tasks/{id}/pause       # 暂停任务
POST /api/tasks/{id}/promote     # 推进下一批（手动）
POST /api/tasks/{id}/cancel      # 取消任务
POST /api/tasks/{id}/retry       # 重新执行失败/卡住主机（可指定 task_host_ids）
DELETE /api/tasks/{id}           # 删除任务（draft/终态且无活动指令）
```

### Agent 网关

```http
POST /api/agent/register              # 注册（需 enroll token）
POST /api/agent/heartbeat             # 心跳（需 Bearer 认证）
POST /api/agent/commands/poll         # 长轮询取指令
POST /api/agent/commands/{id}/ack     # 指令确认
POST /api/agent/commands/{id}/stage   # 阶段上报
POST /api/agent/commands/{id}/report  # 结果上报
GET /api/agent/enroll-token           # 生成一次性注册令牌（管理员）
POST /api/agent/enroll-tokens         # 创建自动化令牌（管理员）
GET /api/agent/enroll-tokens          # 自动化令牌列表（脱敏）
POST /api/agent/enroll-tokens/{id}/revoke  # 注销自动化令牌
POST /api/agent/enroll-tokens/{id}/rotate  # 轮换自动化令牌
```

自动化令牌与一次性令牌并存：

- **一次性令牌**：保持原有行为，默认 15 分钟、单次使用、每台机器一个；`consumeEnrollTokenTx` 的单次消费逻辑不变。
- **自动化令牌**：页面「主机 → 接入主机 → 自动化令牌」生成，可永久有效或自定义到期时间；可设置最大使用次数（0=不限）、来源 CIDR；页面可注销/轮换，注销只阻止新注册，不影响已运行 Agent。
- 创建时完整 token 只返回一次，列表仅显示 `token_prefix`；数据库只保存 SHA-256。
- 注册接口按 token 类型消费：一次性令牌检查 `used_at IS NULL`，自动化令牌原子累加 `used_count` 并检查 `max_uses`，两者都受 `enabled/expires_at/CIDR` 约束。

创建自动化令牌示例：

```json
{
  "name": "prod-ops-automation",
  "permanent": true,
  "max_uses": 0,
  "allowed_cidrs": ["10.0.0.0/8"],
  "os": "linux",
  "arch": "amd64"
}
```

`permanent` 为 `false` 时可改用 `expires_in: "720h"` 或 `expires_at: "2026-12-31T23:59:59+08:00"`；`max_uses=0` 表示不限制。

注册与心跳请求可携带 `signature_modes`（`id` / `signed_path` / `v2`），服务端据此选择
legacy `signature` 的 Path；旧 Agent 不传该字段时按 `id` 处理。注册/心跳还可携带
`build_version`、`build_hash`（自身二进制 SHA256）、`build_variant`（空/`legacy`），
用于服务端判断是否需要自升级。旧 Agent 不传时保留数据库原值。

### Agent 自升级

```http
POST /api/agents/self-update           # 管理员；{"host_ids":[1,2]} 或 {"all_outdated":true}
GET  /api/agents/self-update           # 管理员；目标版本、可用二进制、最近升级记录
```

- `POST` 返回 `enqueued`（已下发数）、`skipped_count`、`skipped`（最多 200 条原因）。
- 可选 `force=true` 即使哈希一致也重新下发。
- 服务端按主机上报的 `os_family` 选择目标二进制：`windows` 主机取
  `upgrade-agent-windows-<arch>.exe`（amd64/arm64，无 legacy），其余取
  `upgrade-agent-linux-<arch>[-legacy]`。`variant=legacy` 只影响 Linux 主机。
- 同一 Agent 同时只允许一条未结束的 `self_update`（数据库唯一索引兜底）。
- 自升级成功由新 Agent 心跳中的 `build_hash` 与指令目标 SHA256 精确匹配确认。

### 主机管理

```http
GET /api/hosts?page=1&size=50           # 分页列出主机（size 最大 500，返回 total/pages）
PUT /api/hosts/{id}                     # 编辑主机业务字段（管理员）
DELETE /api/hosts/{id}                  # 删除主机（管理员）
GET /api/hosts/{ip}/upgrade-history     # 主机升级历史
POST /api/hosts/sync                    # 从 Consul 同步主机
POST /api/hosts/{id}/tunnel-test        # 从平台侧拨测隧道地址（管理员）
```

**主机编辑**

- 可维护字段：`hostname`、`account`、`app_code`、`biz_group`、`department`、`vendor`、`host_type`。
- 网络字段（可选）：`tunnel_ip`（主机侧隧道 IP，仅展示/筛选）、`server_url_override`
  （连接地址覆盖：临时改写 Agent 连接平台的入口，保存后自动切换并触发自检）。
  两者都不参与隧道建立。
- 未提交的字段保持原值，传空字符串表示清空。
- `ip`、`os_family`、`arch`、`agent_status` 等由 Agent 上报，不允许页面修改。
- Agent 重新注册会覆盖 `hostname`；Consul 同步返回非空业务字段时会覆盖对应字段。

**主机删除**

- 存在 `pending` / `running` 的任务或指令时返回 `409`，避免编排中的主机被抽走。
- 删除主机会级联删除 `task_hosts`、`commands` 历史，并删除关联 `agents` 记录以撤销 Agent 凭据。
- 编辑/删除均带租户隔离；`superadmin` 可跨租户操作。

**Consul 自动清理**

- 默认开启（`consul.cleanup_missing=true`，设置页 Consul 分组可关闭）。
- 同步结束后，删除「已从 Consul 移除」的 `source='consul'` 主机。
- 只删 `agent_id` 为空、没有 `agents` 记录、没有未完成任务/指令的主机；Agent 主机、手工主机不受影响。
- 任一服务实例查询失败，或本轮 Consul 未返回任何主机时，**整轮跳过清理**，防止 Consul ACL/网络/配置异常导致误删。
- 返回字段：`upserted`（写入/更新数）、`deleted`（清理数）、`cleanup_skipped`（跳过原因）。
- 环境变量：`CONSUL_CLEANUP_MISSING=false` 可关闭。

**几万台主机的分页**

- 列表由后端分页，页面默认 50 条/页，可选 20/50/100/200/500，支持页码跳转。
- 后端按 `tenant_id + ip`、`tenant_id + app_code` 等复合索引（迁移 010）排序/过滤。
- 关键字搜索使用 `ILIKE '%kw%'`，会扫描当前租户的数据；几万台规模可用，达到百万级建议引入 `pg_trgm` 或外部检索。

---

## 监控与告警

### Prometheus 指标

```
# Agent 指标
upgrade_mgr_agents_total{status="online"}         # 在线 Agent 数
upgrade_mgr_agents_heartbeat_lag_seconds          # 心跳延迟直方图

# 任务指标
upgrade_mgr_tasks_total{status="running"}         # 任务数（按状态）
upgrade_mgr_task_hosts_total{task_id,status}      # 逐任务主机状态
upgrade_mgr_task_failure_rate{task_id}            # 任务失败率 (0-100)
upgrade_mgr_task_duration_seconds                 # 任务耗时直方图

# 指令指标
upgrade_mgr_commands_total{type,status}           # 指令计数
upgrade_mgr_command_execution_seconds{type}       # 指令执行时间

# 制品指标
upgrade_mgr_artifact_downloads_total{component_id,version,arch}
upgrade_mgr_artifact_download_bytes_total
```

### Grafana 示例查询

```promql
# Agent 离线率
(1 - upgrade_mgr_agents_total{status="online"} / sum(upgrade_mgr_agents_total)) * 100

# 任务成功率
upgrade_mgr_task_hosts_total{status="success"} / sum(upgrade_mgr_task_hosts_total) * 100

# P95 心跳延迟
histogram_quantile(0.95, rate(upgrade_mgr_agents_heartbeat_lag_seconds_bucket[5m]))
```

---

## 安全建议

1. **HTTPS 强制**：生产环境务必配置 Nginx HTTPS，避免 token 明文传输
2. **网络隔离**：控制台与 Agent 通信走内网，避免公网暴露
3. **强密码**：修改默认管理员密码，显式配置 32+ 字节随机 `session_secret`（留空会生成并持久化到数据库）
4. **注册令牌**：生产环境开启 `require_enroll_token`；一次性令牌保持单次使用与短有效期。自动化令牌按自动化平台/租户单独创建，建议设置 `allowed_cidrs` 和 `max_uses`，泄露或不再使用时立即在页面注销/轮换。
5. **最小权限**：数据库用户仅授予必要权限，避免使用 superuser
6. **审计日志**：定期检查 `audit_logs` 表，监控敏感操作
7. **备份恢复**：每日备份数据库，定期演练恢复流程
8. **反向代理一致性**：写请求会校验 Origin/Referer；反向代理必须保留原始 Host，并正确配置 `server.external_url` / `server.cors_origins`

---

## 常见问题

### Q1: Agent 离线/心跳超时？

```bash
# 检查 Agent 服务
systemctl status upgrade-agent

# 检查日志
journalctl -u upgrade-agent -f

# 手动拉起
systemctl restart upgrade-agent
```

### Q2: 制品上传失败？

```bash
# 检查磁盘空间
df -h /data/artifacts

# 检查 Nginx body size 限制
client_max_body_size 2G;

# 二进制制品检查 ELF；Shell 安装脚本可忽略
file your-binary
ldd your-binary  # 应为静态链接（not a dynamic executable）
```

### Q3: 升级失败如何回滚？

- 方式1：备份自动保留在 `backup_dir`，手动回滚
- 方式2：创建回滚任务，指定旧版本
- 注意：`script` 模式不创建平台备份；需要自动回滚请使用 `replace` / `replace_exec` 模式。

### Q4: 如何清理过期数据？

自动维护任务每日凌晨 2 点自动执行：
- 清理 7 天前过期的 pending 指令
- 清理 30 天前完成的指令
- 归档 90 天前完成的任务
- VACUUM ANALYZE 优化表

---

## 容器水平扩展（多副本）与滚动重启

服务端 HTTP API 基本无状态，**单机和多副本使用同一套代码**：编排器每 5 秒用 PostgreSQL advisory lock 抢占一次执行权，抢不到就跳过，因此不需要 Consul，也不会重复推进任务。默认 `docker-compose.yml` 仍是单副本编排，不能直接 `--scale`。

### 必须满足的条件

| 条件 | 要求 | 原因 |
|---|---|---|
| 数据库 | 所有副本使用同一个 PostgreSQL，账号需有 `app_settings` 读写权限 | 任务、指令、Agent、审计、设置都在 DB；advisory lock 和会话密钥持久化也依赖它 |
| `SESSION_SECRET` | 建议所有副本显式设置相同值（32+ 字节）；留空时程序会生成一次并持久化到 `app_settings`，多副本/重启共享 | 会话 Cookie 和 Casdoor OAuth state 都用它做 HMAC；滚动重启时不要随意更换 |
| 制品存储 | `artifact.storage_type: s3` 或 `oss`；或所有副本挂载同一个 RWX 共享目录 | 默认 `local` 写在容器本地盘，上传和下载命中不同副本会 404 |
| 编排器 | 无需额外配置；数据库连接可用即可 | PostgreSQL advisory lock 自动保证同一时刻只有一个副本推进批次 |
| Agent 二进制 | 使用镜像内置的 `/opt/upgrade-mgr/dist/upgrade-agent-linux-<arch>`，或共享 `agent_binary_dir`；接 Windows 主机还需放入 `upgrade-agent-windows-<arch>.exe` | 一键安装命令需要从服务端目录读取二进制，缺失对应平台文件会 404 |
| 连接池 | `database.max_open_conns` 应 ≥ `artifact.max_concurrent_uploads + 4`（上传并发默认 4，即下限 8；配置值更小时启动/热更新会自动抬高，建议 10+） | 每个并发上传持有一条 advisory lock 连接；编排器、维护、Consul 同步也需要连接 |

推荐配置：

```yaml
server:
  session_secret: "所有副本一致的 32+ 字节随机串"  # 也可留空，由 DB 持久化共享
artifact:
  storage_type: s3          # 或 oss；尽量不用 local
  max_concurrent_uploads: 4
  lock_wait: 30m
  s3_endpoint: obs.cn-east-3.myhuaweicloud.com
  s3_region: cn-east-3
  s3_bucket: upgrade-mgr
  s3_use_ssl: true
database:
  max_open_conns: 50
consul:                     # 可选，仅用于主机清单同步，不参与编排主备
  enabled: true
  address: consul.internal:8500
  sync_interval: 5m
```

S3/OSS 凭据可用环境变量注入，避免写进配置文件：`ARTIFACT_STORAGE_TYPE`、`ARTIFACT_S3_ENDPOINT`、`ARTIFACT_S3_REGION`、`ARTIFACT_S3_BUCKET`、`ARTIFACT_S3_ACCESS_KEY`、`ARTIFACT_S3_SECRET_KEY`、`ARTIFACT_S3_USE_SSL`，以及对应的 `ARTIFACT_OSS_*`。

### Kubernetes 滚动发布

仓库提供 `deploy/k8s/upgrade-mgr.yaml` 和 `deploy/k8s/secret.example.yaml`：

- Deployment 默认 3 副本，`maxUnavailable: 0`、`maxSurge: 1`，新 Pod 就绪后才终止旧 Pod
- 就绪探针 `GET /ready`（检查数据库），存活探针 `GET /health`
- `terminationGracePeriodSeconds: 40` + `preStop sleep 5`，覆盖 Agent 20 秒长轮询
- PDB `maxUnavailable: 1`，节点维护不会一次驱逐多个副本
- 清单默认 `trust_proxy: false`；放在 Ingress/Nginx 后时改为 `true`，并只填实际代理 CIDR
- **单机模式**：把 Deployment 的 `replicas` 改为 `1` 即可；不需要 Consul。若使用 `artifact.storage_type: local`，把 `emptyDir` 换成持久化 PVC（多副本需要 RWX 共享 PVC）

使用步骤：

```bash
# 0. 创建命名空间（主清单也包含 Namespace，可跳过此步直接 apply 主清单）
kubectl create namespace upgrade-mgr --dry-run=client -o yaml | kubectl apply -f -

# 1. 复制 Secret 模板并填入真实数据库/S3/管理员密码（不要提交填好的文件）
cp deploy/k8s/secret.example.yaml /tmp/upgrade-mgr-secret.yaml
vi /tmp/upgrade-mgr-secret.yaml
kubectl apply -f /tmp/upgrade-mgr-secret.yaml

# 2. 修改 ConfigMap 里的 external_url、S3/OSS 地址和桶名后应用
vi deploy/k8s/upgrade-mgr.yaml
kubectl apply -f deploy/k8s/upgrade-mgr.yaml

# 3. 查看滚动状态
kubectl -n upgrade-mgr rollout status deployment/upgrade-mgr
```

### 变更后滚动重启所有副本

**推荐路径：设置页修改 → 滚动重启。** 除制品上传运行时配置外，设置页会把值写入数据库并只立即更新当前副本内存；滚动重启后所有副本在启动时重新加载 `app_settings`，最终一致。制品上传并发/锁等待已支持跨副本自动同步：当前副本立即生效，其他副本约 5 秒内同步，无需重启。

```bash
kubectl -n upgrade-mgr rollout restart deployment/upgrade-mgr
kubectl -n upgrade-mgr rollout status deployment/upgrade-mgr
```

如果改的是 ConfigMap / Secret / YAML：

```bash
kubectl apply -f /tmp/upgrade-mgr-secret.yaml   # 若改了 Secret
kubectl apply -f deploy/k8s/upgrade-mgr.yaml
# K8s 不会因为 ConfigMap/Secret 变化自动重启 Pod，必须显式 rollout restart
kubectl -n upgrade-mgr rollout restart deployment/upgrade-mgr
```

注意事项：

1. **DB 设置优先级最高**：`app_settings` 中的值会覆盖 YAML/环境变量。若要从 YAML/env 接管某个配置，先删除对应 DB 行（设置页保存过的键才有行），再滚动重启：
   ```sql
   DELETE FROM app_settings WHERE key = 'consul.address';
   ```
2. **不要在同一次滚动中更换 `SESSION_SECRET`**：新旧副本密钥不同会导致用户会话和 Casdoor state 校验失败。需要轮换时单独安排窗口，并接受所有用户重新登录。
3. **数据库迁移必须向后兼容**：新副本启动时可能先执行迁移，而旧副本仍在跑。新增列/表通常安全；删除/重命名列请用 expand-contract 或停机窗口。
4. **切换制品存储前先迁移历史制品**：从 `local` 切到 S3/OSS 时，旧制品不会自动上传；否则旧版本下载会 404。
5. **切换认证方式会有短暂不一致**：滚动期间旧副本仍用旧 provider，新副本用新 provider。建议在维护窗口操作，或确认两种登录方式都能接受。

### 启动后确认

- 所有副本日志出现 `✅ 灰度编排器已启动（PostgreSQL advisory lock，单机/多副本通用）`。
- 所有副本日志出现 `ℹ️ 制品上传配置: 并发上限=4，跨副本锁等待=30m0s`；页面保存后当前副本会输出 `🔄 制品上传配置已热更新`，其他副本会输出 `🔄 已同步其他副本的制品上传配置`。
- 确认制品后端没有出现“降级为 local”告警；多副本使用本地盘会导致上传/下载命中不同副本时 404。
- `GET /ready` 返回 200 且 `{"status":"ready"}`；`GET /health` 只表示进程存活。
- 多副本下任意副本被终止，其他副本下一轮会自动抢到编排锁，任务继续推进。
- 手动 Consul 同步与定时同步并发时，只有一个会执行；另一个返回 409 或直接跳过。

### 副本间不一致的点（可接受但需知悉）

- **设置页跨副本热更新仅覆盖制品上传运行时配置**：`artifact.max_concurrent_uploads` / `artifact.lock_wait` 由各副本约 5 秒轮询 DB 自动同步；其余设置仍只更新当前副本内存和数据库，多副本请改完后滚动重启全部副本。
- **限流是进程内内存**：每个副本各算一份，N 个副本的实际阈值约为 N 倍；公网建议在 Ingress/WAF 做全局限流。
- **指标按副本独立**：Prometheus 需要抓取所有 Pod/容器，不能只抓一个。
- **会话撤销有最多 30 秒本地缓存**：改密/禁用后，其他副本最迟约 30 秒后拒绝旧会话。
- **Consul 同步已加跨副本锁**：同一时刻只有一个副本写库；其他副本跳过本轮，不影响一致性。

### 容器编排注意事项

- PostgreSQL advisory lock 是**会话级锁**；如果数据库前面有 PgBouncer，请使用 `session pooling`，不要用 transaction pooling，否则加锁/解锁可能落到不同后端连接。
- 默认 `deploy/docker/docker-compose.yml` 带 `container_name` 和固定宿主机端口，不能直接 `docker compose up --scale`；多副本请用 Kubernetes/ECS/Nomad，或去掉 `container_name`、改用 `expose` 并由前置负载均衡转发。
- Agent 使用 HTTP 长轮询，单次最长 `agent.poll_hold` 秒（默认 20）；负载均衡/Ingress 空闲超时需大于 20 秒（Nginx 默认 60 秒可以）。
- 优雅关闭等待最多 30 秒；Kubernetes 建议 `terminationGracePeriodSeconds: 40`（清单已配置）。
- 健康检查用 `GET /health` 做存活、`GET /ready` 做就绪；数据库不可用时 `/ready` 返回 503，应用启动阶段数据库不可用会直接退出。
- 不部署 Consul 也可以多副本：Consul 只影响主机清单同步，不影响编排、会话和制品。

---

## 已知限制与问题

### 关键限制

1. **Go 版本与内核兼容性**：控制台和默认 Agent 用 Go 1.26 构建，要求 Linux 内核 ≥ 3.2。CentOS 6（内核 2.6.32）请使用 `make agent-legacy` 产出的 Go 1.23 版 Agent，见上文「CentOS 6 / 老内核 Agent」。

2. **多实例部署**：编排器已改为 PostgreSQL advisory lock，不再依赖 Consul Leader；多副本需共享数据库、共享/对象制品存储，并建议使用一致的 `SESSION_SECRET`，见「容器水平扩展（多副本）与滚动重启」。

3. **配置热更新**：`artifact.max_concurrent_uploads` / `artifact.lock_wait` 支持跨副本约 5 秒自动同步，保存后无需重启；其余设置页项只对当前副本即时生效（同时写入 DB），多副本需在修改后滚动重启全部副本。`app_settings` 优先级高于 YAML/环境变量，切回配置文件前需清理对应 DB 行。部分配置（如认证方式、制品目录）本来就需要重启。

4. **制品下载认证**：当前制品下载端点需 Agent Bearer 认证，匿名下载已禁用。

5. **CentOS 6 兼容性**：legacy Agent 已用 Go 1.23 编译并通过单测，但尚未在真实 CentOS 6 主机实测；Go 1.23 已停止安全维护。

6. **制品上传锁等待与连接占用**：等待跨副本锁最长 `artifact.lock_wait`（默认 30m）；每个并发上传持有一条 PostgreSQL 连接，连接池会按“上传并发数 + 4”自动抬高，但不应超过 PostgreSQL `max_connections`。

7. **旧制品路径兼容**：新上传路径包含 `os_family` 维度；历史制品仍按数据库中的原路径下载，不会被自动迁移。同一身份重新上传后会改用新路径，旧对象可能成为孤儿文件，建议后续增加清理任务。

### 2026-10-26 安全加固、制品并行上传与配置热更新

- **制品并行上传**：上传改为「组件共享锁 + 制品身份独占锁」。同一组件下不同版本/架构/OS 的制品可并行，同一 `component/version/arch/os` 仍串行；删除/清空组件持独占锁，等待在途上传并阻止新写入。跨副本仍由 PostgreSQL advisory lock 保证互斥。
- **存储路径唯一性**：路径改为 `<component>/<version>-<os>-<arch>/<filename>`，把 `os_family` 纳入目录，避免并行上传时不同系统同名文件互相覆盖。
- **运行时热更新**：`artifact.max_concurrent_uploads`（默认 4，1-64）与 `artifact.lock_wait`（默认 30m，1s-24h）页面保存后当前副本立即生效；其他副本约 5 秒内轮询 DB 自动同步，无需重启。
- **连接池自适应**：启动时与热更新后自动保证 `database.max_open_conns >= artifact.max_concurrent_uploads + 4`；下调上传并发后按新的“上传数 + 4”重新计算，不会无限增长。
- **设置原子保存**：一次保存多个设置改为单事务，全部成功才提交，提交后一次性更新内存；任一项失败整批回滚，避免部分生效。
- **安全加固**：写请求 CSRF Origin/Referer 校验、CSP/HSTS/no-store 安全响应头、Redis 缓存仅在成功写请求后失效、Agent 注册事务化与 enroll token 原子消费、RBAC 防提权、用户扩展字段跨租户隔离、主机字段定义写操作收紧、本地上传临时文件 + fsync + 原子 rename、Agent 安装器参数与文件名路径穿越修复。
- **H5 移动端**：窄屏表格卡片化、`safe-area` 适配、16px 输入框防 iOS 缩放、主题色 meta 同步；320/390/768/1366 宽度实测无横向溢出。
- **Agent 版本分布修复**：统计优先使用 `build_version`，旧 Agent 回退 `agent_version`；是否最新按目标二进制 SHA256 判断，并在注册/心跳时同步 `agent_version`，解决升级后仍显示 `0.1.0` 的问题。
- **主机接入入口前置**：完整「接入主机（安装 Agent）」卡片重新放到主机页顶部，保留「帮助 → 接入主机示例」入口；空列表提示同步更新。
- **外部文件缓存加速代理**：新增 `server.external_binary_proxy`（默认 `https://proxy.xiaonuo.live/proxy?url=`）与 `server.external_binary_proxy_hosts`。缓存下载、测试下载、刷新缓存均优先走代理，失败再直连兜底；内部镜像域名不在代理列表时保持直连，避免内网地址被发往公网代理。
- **自动化注册令牌**：主机页新增「自动化令牌」页签，可生成永久或自定义到期令牌，支持最大次数、来源 CIDR、页面注销/轮换；一次性令牌 15 分钟/单次使用逻辑保持不变。迁移 030/031。
- **验证**：`go vet ./...`、`go test -race -count=1 ./...`（真实 PostgreSQL 16）通过；Playwright 12/12（含移动端 `mobile.spec.js`）；两实例实测 A 保存后 B 在 5 秒内同步且 PID 不变。

### 2026-10-23 隧道自检自动化与主机侧回连

- 保存「连接地址覆盖」后自动触发校验：平台侧异步拨测写回结果，同时标记待主机侧自检。
- 心跳响应新增 `tunnel_check_url`，Agent 从主机内部回连拨测并在下一次心跳回报
  （`tunnel_check` 字段），服务端写入 `hosts.tunnel_check_*`（迁移 018）。
- 主机侧结论优先于平台侧：解决「平台在容器内、隧道接口只在宿主机」时平台侧误判的问题；
  10 分钟未回报自动清除待检查标记。
- 主机列表新增自检标签（通过/未通过/检查中），失败标红并悬停显示阶段明细；
  手动拨测同样写入该状态；清空隧道地址自动清除结果。
- Agent 端异步拨测（不阻塞心跳），同一地址 5 分钟内不重复、失败原因压缩后上报。

### 2026-10-23 隧道拨测与可选双向 TLS

- 新增 `POST /api/hosts/{id}/tunnel-test`（管理员、租户隔离、审计 `host.tunnel_test`）：
  从平台侧按 DNS → TCP → TLS → `GET /health` 逐步拨测隧道地址，识别端口不通、证书不匹配、
  反向代理指向错误服务等问题；支持测试尚未保存的候选地址。
- 新增 `internal/pkg/tunnelprobe`（拨测实现）与主机编辑页「测试隧道连通性」按钮，
  结果逐阶段展示耗时、详情、证书签发者/到期时间与排查提示。
- 可选 HTTPS / 双向 TLS：控制台配置 `server.tls_cert/tls_key/tls_client_ca/tls_client_auth`
  后以 HTTPS 监听，`require` 时强制校验 Agent 客户端证书。
- Agent 新增 `--tls-ca/--tls-cert/--tls-key`（install/run 均支持，写入 state 持久化），
  TLS 客户端同时用于心跳、取指令、制品与自升级下载；最低 TLS 1.2。

### 2026-10-23 多网卡出口 IP 与隧道地址

- Agent 新增 UDP 路由探测出口 IP（`internal/pkg/netinfo`）：多网卡主机按访问服务端的路由选网卡，
  注册/心跳上报 `exit_ip`；探测失败沿用上一次值，不覆盖为空。
- 主机列表新增「出口 IP」「主机隧道 IP」「平台隧道地址」「当前连接地址」，搜索与任务 IP 段选择同时匹配
  `hosts.ip` 与 `hosts.exit_ip`；`hosts.ip` 仍是注册来源地址，作为主机身份不被 Agent 覆盖。
- 连接地址覆盖：`server_url_override`（原 `server_tunnel_url`，迁移 019 重命名）由心跳响应下发，
  Agent 自动切换并持久化到 state；清空后回退安装时的默认地址。该字段不参与隧道建立。
- 失效保护：隧道连续 3 次心跳失败自动回退默认地址，并在 5 分钟内不再切回同一地址，避免填错隧道导致失联。
- 连接地址只填 IP 时按 Agent 上报的默认地址补齐协议与端口；只接受 http/https，拒绝带凭据的 URL。
- 新增迁移 `017_host_network`、`internal/pkg/hostnet`（地址校验/解析）与前端隧道编辑表单。

### 2026-10-23 Windows Agent 支持

- Agent 拆分平台实现：`osinfo`（注册表 ProductName + RtlGetNtVersionNumbers）、`svcmgr`（`x/sys/windows/svc/mgr`）、`watchdog`（独占文件句柄 + `DETACHED_PROCESS`）、`installer`（SCM 服务 + 恢复动作 + `schtasks`）、`agentpath`（Program Files/ProgramData 默认路径）、`winsvc`（服务宿主）。
- 新增 `--service` 与 `winsvc.RunIfService`：由 SCM 启动时以服务模式运行，前台手工执行时保持普通进程行为。
- 执行器按平台选择解释器：Windows 优先 `powershell.exe`（`-NoProfile -ExecutionPolicy Bypass -File`），`.cmd/.bat` 走 `cmd.exe /c`；TTY 在 Windows 自动降级；`install_path` 使用盘符 + 系统目录黑名单 + 程序目录白名单校验，并拒绝 UNC 与 `..`。
- 自升级在 Windows 上改为「旧 exe 改名 `.old` → 新 exe 就位 → exit 75」，由 SCM 恢复动作重启，启动时清理 `.old`；备份目录改用 `agentpath.BackupDir()`，Windows 跳过 POSIX 权限位检查。
- 服务端 `agentupdate.Target` 增加 `os`，新增 `ResolveOS`/`ChooseOS`/`parseBinaryName`；下发按 `hosts.os_family` 选择平台二进制；`/api/agent/binary/{name}` 支持 `windows-{amd64,arm64}`（`.exe`）。
- 接入命令支持 `?os=windows&arch=amd64`，生成 PowerShell 2.0 兼容命令（`System.Net.WebClient`）；前端新增操作系统选择，切换时联动可用架构。
- 新增 `build/agent-windows.mod`（Go 1.20 + `x/sys v0.18.0`）、Makefile `agent-windows` / `test-agent-windows`、CI Windows 作业与 `.exe`/`.zip` 发布产物。

### 2026-10-22 执行命令/脚本任务

- 新增 `kind=command` 一次性命令任务：不绑定组件/制品，选中主机后下发任意 Shell 脚本，复用任务列表、批次灰度、暂停/取消/重试与详情日志。
- 迁移 `016_command_tasks`：`upgrade_tasks` 增加 `kind`、`command JSONB`，`component_id` 改为可空；`agents` 增加 `capabilities`，注册/心跳上报 `run_command` 能力。
- Agent 新增 `run_command` 指令：脚本写入临时文件，按白名单（`bash/sh/dash/ksh/zsh`）执行；`tty=true` 用 `script -q -c` 分配伪终端，`stdin` 支持交互确认；输出按 2KB/800ms 分块经阶段上报实时回传。
- 旧 Agent 缺少 `run_command` 能力时，下发阶段跳过主机并在日志写明「Agent 版本过低」；命令任务同样受 `agent.allow_exec_mode` 保护，创建和下发双重校验。
- `agent.allow_exec_mode` 默认值改为 `true`：`script/exec/replace_exec` 模式与命令任务开箱可用，可在「设置 → Agent」关闭。
- 前端任务页新增任务类型切换、命令脚本/解释器/参数/stdin/PTY/超时表单与「卸载旧监控」示例；任务列表显示类型，详情展示脚本内容，命令任务日志默认展开。

### 2026-10-22 任务执行详情、阶段日志与删除

- `GET /api/tasks/{id}` 重写：返回任务汇总、批次进度、逐机状态/当前阶段和指令累计日志；主机按 `host_page`/`host_size` 分页（默认 50，最大 500），只查询当前页主机的日志，避免几万台主机导致响应体膨胀。
- 任务列表新增“详情日志”入口：弹窗展示批次进度、逐机阶段、最新指令状态和每台主机的执行日志；运行中/已暂停任务每 4 秒自动刷新，终态停止轮询。
- Agent 阶段上报现在同时更新 `commands.log`、`task_hosts.stage` 和 `hosts.last_stage`，任务详情可看到“下载 → 备份 → 停服 → 替换 → 启动 → 验证”的完整链路；终态指令不再追加阶段日志。
- 取消任务时同步把未完成批次置为 `canceled`，详情页不会继续显示“执行中”。
- 任务取消/删除、阶段上报、结果上报与逐机下发统一锁顺序 `task_batches → task_hosts → commands → upgrade_tasks`，避免并发取消时残留新指令或事务互相等待；启动/暂停/取消增加状态条件与影响行数检查，防止覆盖终态。
- 新增 `DELETE /api/tasks/{id}`：仅允许 `draft` 或终态（`success/failed/canceled/rolled_back`），`running/paused` 需先取消；即使终态，只要仍有 `pending/dispatched/accepted/running` 指令也拒绝删除。
- 删除走租户作用域并写审计 `task.delete`，级联清理 `task_batches/task_hosts/commands`；前端对可删除状态展示“删除”按钮并二次确认。

### 2026-10-22 页面自动刷新、登录回车、CI 整合与并发审计

- 任务列表与主机列表新增自动刷新：有升级动作时 4 秒、空闲时 15 秒；数据未变化不重建 DOM，自动刷新遇到输入框聚焦会跳过，避免打断填写。任务详情弹窗同样按数据指纹去重，并恢复展开的日志与滚动位置。页面切到后台时暂停轮询，重新可见时立即刷新。
- 任务列表新增“进度”列，直接展示成功占比；主机列表与任务列表标注“自动刷新中”。
- 登录页支持用户名/密码框按回车提交，登录中禁用按钮，失败后保留用户名并自动聚焦密码框。
- 合并 `.github/workflows/build.yml` 与 `publish.yml` 为单一 `ci-cd.yml`：PR 跑 PostgreSQL 集成测试 + `go vet` + 前端语法检查；main/tag 复用同一套编译产物发布，不再重复编译；整仓只保留一个旧运行记录清理任务。
- 死锁审计：主机删除原先先锁 `hosts` 再级联删除子表，与 Agent 上报的 `task_hosts → commands → hosts` 加锁顺序相反。现在删除前先按 `task_hosts → commands → hosts` 加锁，消除潜在死锁。
- Consul 清理同样改为先挑候选主机、再在事务内按 `task_hosts → commands → hosts` 逐台加锁复查后删除，并按 500 台分批，避免大批主机离线时一个长事务阻塞上报。
- `ReapExpired` 的租约过期判断改用数据库 `NOW()`，`Dequeue` 写入租约也改为数据库时间并回读，避免应用与数据库时钟偏差导致空转或误回收。
- 新增迁移 `015_reconcile_indexes`：补终态指令对账索引与维护窗口清理索引，降低高频轮询/对账的扫描量。
- 修正任务自动刷新只拉取 `/api/tasks`，组件/制品沿用缓存，减少无意义请求。
- 补齐站点图标 `favicon.ico` 与 `apple-touch-icon.png`；静态资源处理器增加 `.ico/.png/.webmanifest` 的 Content-Type，避免图标被 SPA 回退成 HTML。

### 2026-10-22 任务重新执行与制品下载 416 修复

- 新增 `POST /api/tasks/{id}/retry`（管理员，审计 `task.retry`）：重新执行失败/超时/取消/跳过/已回滚的主机，默认跳过已成功主机；`task_host_ids` 可指定单台，`force=true` 才允许强制重跑执行中/已成功主机。
- 重试在事务内取消该主机未结束的旧指令、把逐机记录重置为 `pending`、批次恢复 `pending`、任务恢复 `running`，由编排器重新下发，无需手工改库或逐台登录。
- 用 `task_hosts.started_at` 作为重试基线：对账只同步基线之后的终态指令，避免刚重置的主机被历史 `failed/timeout` 立刻覆盖；被取消/过期旧指令的迟到阶段与结果上报一律忽略。
- 任务详情逐机表新增“操作”列：失败/超时/取消/跳过/回滚显示“重新执行”，执行中显示“强制重新执行”；任务列表操作区新增“重新执行失败/卡住主机”。
- Agent 下载器修复 HTTP 416 卡死：本地 `.tmp` 残留文件大于远端文件时，`Range` 续传起点越界。现在收到 416 会删除残留并按完整下载重试；下载前若已有完整且校验和匹配的文件直接复用，校验失败删除后自动重下；执行成功后清理临时制品。
- ⚠️ 下载器修复位于 Agent 侧，存量主机需更新 Agent 后生效；页面“重新执行”可立即用于处理已失败/卡住的任务。

### 2026-10-22 Agent 指令签名兼容：服务端升级不再要求重装 Agent

- 服务端同时下发两套签名：
  - `signature`：legacy 签名，Path 按 Agent 上报的能力选择 `id` 或 `signed_path`；
  - `signature_v2`：新版，Path 为 `ID|类型|过期时间`，新 Agent 优先校验。
- Agent 注册与心跳新增 `signature_modes` 能力标识（`id` / `signed_path` / `v2`），服务端据此自动选择 legacy 签名路径，不再依赖 Agent 版本推断；能力在心跳中刷新，Agent 原地升级后也无需重新注册。
- 未上报 `signature_modes` 的旧 Agent 默认按 `id` 处理。**此前只校验指令 ID 的旧 Agent 无需重装**即可执行新服务端下发的指令；服务端滚动升级不再要求全量替换 Agent。
- 新 Agent 同时兼容旧服务端：`signature_v2` 缺失时，依次尝试新版 `SignedPath` 与旧版 ID 签名。
- `signature_v2` 存在但校验失败时**不会回退**到旧签名；只有字段缺失时才进入旧服务端兼容路径（篡改 `signature_v2` 会被拒绝）。
- 新 Agent 仍保留强校验：篡改类型、延长过期时间都会被拒绝。旧服务端/旧 Agent 链路的安全强度仍取决于传输层，建议启用 HTTPS，并建议新装或后续轮换时升级 Agent；但 Agent 升级不再是服务端升级的前置条件。
- 新增迁移 `012_agent_signature_modes`：`agents.signature_modes VARCHAR(128) NOT NULL DEFAULT 'id'`。旧数据自动回填 `id`，旧服务端可忽略该列，支持滚动升级/回滚。

### 2026-10-22 Consul 同步自动清理

- 新增 `consul.cleanup_missing`（默认 `true`）：同步后删除已从 Consul 移除的纯 Consul 主机。
- 安全保护：只删 `source='consul'` 且无 `agent_id`/`agents` 记录的主机；有未完成任务/指令的主机保留；服务实例查询失败或本轮清单为空时跳过清理。
- Agent 注册时同步把 `source` 更新为 `agent`，避免已装 Agent 的主机被 Consul 清理。
- 同步接口返回 `deleted` / `cleanup_skipped`，页面提示清理结果；定时同步日志包含清理数量。
- 迁移 011 增加清理扫描与保护判断所需索引。

### 2026-10-22 主机管理：分页、编辑、删除

- 主机列表改为服务端分页（默认 50 条/页，可选 20/50/100/200/500），支持页码跳转；后端返回 `total/pages`，页码越界自动落到最后一页。
- 新增 `PUT /api/hosts/{id}`：管理员可维护 `hostname/account/app_code/biz_group/department/vendor/host_type`；IP/OS/ARCH/Agent 状态仍由 Agent 上报。
- 新增 `DELETE /api/hosts/{id}`：存在未完成任务/指令时返回 `409`；否则删除主机、级联清理 `task_hosts/commands`，并撤销关联 Agent 凭据。
- 编辑/删除均走租户作用域，只能操作本租户主机；`superadmin` 可跨租户。
- 迁移 010 增加 `(tenant_id, ip)` 等复合索引，优化几万台主机的分页排序与常用过滤。

### 2026-10-21 修复：Agent 注册 401 与空 state

- `install` 在启动服务前把 `ServerURL` / `EnrollToken` 原子写入 `state.json`（0600），
  解决 systemd/upstart 的 `run` 不带 `--enroll-token` 导致每次启动都用空令牌注册（HTTP 401）的问题。
- Agent 注册成功后清空一次性 `EnrollToken`；`saveState` 改为 tmp + fsync + rename，避免进程中断留下 0 字节文件。
- 缺少令牌或凭据残缺时直接返回可操作的错误，不再向服务端反复发起注定失败的注册（避免触发 HTTP 429）。
- watchdog 遇到空/损坏/未注册的 state 时直接报错，不再误判为崩溃并触发二进制回滚。
- 已注册且 `AgentID` / `Token` / `Secret` 完整的 state 在重装时保留，避免丢失 Agent 身份。
- 新增 `install --force` 与页面“强制重新注册”选项：显式覆盖旧 state 并重新注册，需同时提供新令牌。
- `server.external_url` 可在设置页修改，新生成的 Agent 安装命令立即使用新地址；接入页面显示当前命令使用的地址，支持 Nginx 域名场景。
- 保存 `server.external_url` 时校验协议/主机名，禁止路径、查询参数、片段、用户信息和引号；非法值整批设置不落库。

### 本轮修复（2026-10-19）

**✅ 已修复（34 项）**：

*安全与认证*：
- 指令签名已覆盖 Type 和 ExpireAt（`cmd.SignedPath()`），防篡改指令类型或延长过期时间
- `/metrics` 支持可选 Bearer Token 认证（`METRICS_TOKEN` 环境变量），未配置时回退为登录会话
- `/api/settings` 改用 `RequireSuperAdmin`，租户管理员无法修改全局配置
- 会话撤销：基于 `local_users.updated_at` 时间戳（30s 缓存），改密/禁用/改角色后旧会话失效
- 登录时序攻击：用户不存在时执行 dummy bcrypt 比较，响应时间一致
- 默认管理员密码随机生成（18 字节 base64url），首次启动打印一次
- Casdoor OAuth state 绑定 HttpOnly cookie，防 CSRF
- X-Forwarded-For 从右往左跳过受信代理（`trusted_proxies` 配置），防 IP 伪造

*租户隔离*：
- 批次推进加租户隔离（`dispatch.go` 的 Pause/Promote/Cancel 加上 `tenant_id` 条件）
- 新增迁移 007：`enroll_tokens.tenant_id` 字段，Register 使用 token 的 tenant_id
- agentgw 新增 `ConsumeEnrollToken` 方法，Report/Ack 添加终态保护

*编排器核心重构*：
- 租约回收：dispatched/accepted/running 超时自动置为 timeout，pending 过期置为 expired
- 批次下发改为全局 advisory lock + 单主机事务（FOR UPDATE），防多副本/滚动重启重复下发
- HealthWait 实现：检查上一批次 finished_at，未到等待时间不下发
- 失败批次处理：超阈值时批次置为 failed 任务转 paused，Promote 后跳过 failed 批次继续
- Cancel 完整实现：取消未完成的 commands 和 task_hosts，更新任务状态
- 百分比灰度至少分配 1 台（`task.go` 整数除法归零修复，新增 3 个测试用例）

*并发与性能*：
- 编排器改为 PostgreSQL advisory lock，移除 Consul Leader 选举依赖；单机/多副本统一，滚动重启可自动接管
- 批次通过/失败与任务暂停同一事务提交，避免重启窗口误推进下一批
- 空 `SESSION_SECRET` 自动生成并持久化到 `app_settings`，多副本/重启共享
- 新增 `GET /ready` 就绪探针，`/health` 仅作存活探针
- `setting.Apply` 热更新用 `Snapshot()` 方法消除竞态
- 迁移系统死锁修复：`migrate.go` 重构为 querier 接口，全程在持锁连接上执行
- `/8` 等大网段展开限制为 65536 条（`ipx.go` 预先计算），防 OOM
- `rows.Err()` 检查补全（`orchestrator.go`、`metrics.go` 等 6 处）
- metrics gauge 清理：任务结束后 Reset，传递 context

*Agent 基础设施*：
- `/api/agent/binary/{name}` 路由，架构白名单 + 路径穿越防护，enroll 安装命令不再 404
- 限流器优雅关闭（`Stop()` 已接入 `main.go`）
- AgentPoll 响应 `ctx.Done()`，优雅关闭不再最长等 60s

*其他*：
- 迁移 006 去掉重复索引 `idx_commands_task_id`
- CI 工作流加 `permissions: contents: read`，`action-gh-release` 升至 v2
- `centos6_verify.sh` 用 `mktemp -d` 防 TOCTOU 符号链接攻击

**⚠️ 待修复（Agent 端核心缺陷）**：
- 心跳被长时间升级阻塞，watchdog 误判拉起第二个实例 → 需独立 goroutine + 单实例 flock
- 指令重放攻击 → 需持久化 nonce 去重
- 跨文件系统 rename 报 EXDEV（下载、备份、自升级）→ 临时文件需放目标同目录
- 进程组清理缺失（超时 kill 只杀父进程）→ 需 `Setpgid` + kill(-pgid)
- 健康检查失败时回滚语义不清（`IgnoreError` 被忽略）→ 需修正
- 备份无上限堆积 → 只保留最近 N 个
- upstart 模板 `respawn limit unlimited` 在 CentOS 6（0.6.5）可能不支持 → 改为 `respawn limit 10 5`
- installer 模板 ServerURL 未转义，含特殊字符会出错

**📋 低优先级**：
- 制品上传跨租户覆盖风险（storage key 需包含 tenant 前缀）
- Component 删除先删 DB 再删存储，中间窗口期不一致
- Local 存储临时文件名固定，并发冲突
- 审计日志异步写入，关闭时可能丢失（需 Flush）
- CORSOrigins 配置项存在但无中间件使用（可删配置或补中间件）
- 前端 logout 等调用 204 响应时 `r.json()` 会报错（需 catch）

---

## 目录结构

```
cmd/server/   控制台入口          cmd/agent/    被控端入口
internal/
  config/ setting/ svc/ auth/ middleware/ handler/
  logic/{agentgw,component,artifact,sync,task}
  agent/{watchdog,svcmgr,downloader,binswap,executor,installer,selfupdate,
         agentpath,osinfo,winsvc,tlsclient}
  pkg/{ipx,runner,notify,signer,consul,httpx,safepath,pglock,
       netinfo,hostnet,tunnelprobe,...}
  migrate/       数据库迁移
  storage/       制品存储（local/s3/oss）
  metrics/       Prometheus 指标
  maintenance/   自动维护
web/dist/     内嵌前端
deploy/config/ 配置示例
examples/     安装示例（node_exporter 单文件 install.sh，内嵌 systemd unit 与 SysV init）
scripts/      工具脚本
```

---

## 进度

- [x] **P8**：安全加固、制品并行上传、运行时配置热更新、设置原子保存、H5 移动端完善
- [x] **P7**：Windows Agent（Server 2008 R2 ~ Windows 11，amd64/arm64）：SCM 服务宿主与安装/卸载、失败自动重启、计划任务看门狗、PowerShell/cmd 脚本执行、Windows 自升级（`.old` 让位 + exit 75）、服务端 OS 维度二进制解析与接入命令、CI 与发布打包
- [x] **P0**：骨架、配置分层、独立启动、PG schema、IP 解析、Consul 可选同步、内置认证、三主题内嵌前端
- [x] **P1**：协议定义、Agent 核心包、Agent 主循环（注册/心跳/poll）、服务端 Agent 网关 API
- [x] **P2**：组件/制品完整 CRUD、任务模型、长轮询原子取件、维护租约、ack/stage/report 闭环、端到端验证
- [x] **P3**：灰度编排器、Agent 指令验签、全 Go 安装器、保活 watchdog、CI 多架构交叉编译
- [x] **P4**：Agent 自升级、Consul Meta 版本回写、enroll-token 生成 API、主机升级历史 API
- [x] **P5**：Prometheus 指标、多租户 RBAC、分布式编排器（PostgreSQL advisory lock）、制品外部存储（S3/OSS）
- [x] **P6**：数据库自动化（自动创建/版本化迁移/自动维护/零停机更新）

---

## 许可证

MIT © iflyelf

---

## 贡献与支持

如发现问题或有改进建议，请提交 [GitHub Issues](https://github.com/iflyelf/upgrade_mgr/issues)。

### 2026-10-23 连接地址覆盖重命名与多地址 fallback

- 将 `hosts.server_tunnel_url` 重命名为 `hosts.server_url_override`（迁移 019，原值保留），
  语义明确为「临时改写 Agent 连接入口」，并统一文档/接口/前端措辞：该字段不参与隧道建立。
- Agent 支持 `--server` 逗号分隔多个地址（`install` / `run` 均可），保存为 `state.server_urls`：
  默认地址连续 2 次心跳失败自动轮换到下一个备用地址，循环尝试；覆盖地址连续 3 次失败仍回退默认。
- 备用地址轮换不依赖服务端配置，避免「服务端推送错误覆盖地址」导致主机失联。
- 前端主机编辑改为「连接地址覆盖 (server_url_override)」，提示中明确隧道由隧道软件负责；
  列表标签改为「连接覆盖 / 已连接到覆盖地址」。

### 2026-10-24 修复 Windows 外部文件 502 + 平台离线上传

- **根因**：GitHub Release 资产会 302 到 `release-assets.githubusercontent.com`，
  默认上游域名白名单未包含该域名，平台代缓存因「重定向目标被拒绝」返回 502。
- 默认域名白名单新增 `release-assets.githubusercontent.com`、
  `github-releases.githubusercontent.com`、`objects.githubusercontent.com`。
- 外部文件下载失败时自动经 `proxy.xiaonuo.live` 重试一次（GitHub / raw 地址）。
- **平台离线上传**：`POST/DELETE /api/external/binaries/{name}/upload`（管理员），
  文件保存到 `<agent_binary_dir>/.external-local/`；`/api/external/binary/{name}`
  **优先返回本地文件**，平台完全不需要出网。外部文件页新增「上传离线文件 / 清除离线文件」，
  列表展示「本地离线」大小。
- `install.ps1`：不支持 TLS 1.2 时只请求平台 `?local=1`（本地/缓存，不触发上游下载）；
  未上传时快速失败并提示上传，避免 WebClient 约 100 秒超时；直连仅在有 TLS 1.2 时尝试。
- `/api/external/binary/{name}?local=1`：只读本地/缓存，绝不触发上游下载。
- 新增单测：离线上传/优先下载/删除、HTML 伪脚本拒绝、默认重定向域名、代理回退。

### 2026-10-24 修复 Windows 命令任务下载 install.ps1 超时 / 双扩展名

- 命令示例不再依赖 `WebClient.DownloadFile`（2008 R2 默认 100 秒超时）：
  - 先请求平台 `?local=1`（20 秒，只读本地/缓存）；
  - 未命中再请求平台代下载（180 秒，平台自身上限 10 分钟）；
  - 仅当本机支持 TLS 1.2 时才回退 `proxy.xiaonuo.live`；
  - `UPGRADE_AGENT_SERVER_URL` 为空时用控制台页面地址兜底；
  - 下载后校验非 HTML，并确保 UTF-8 BOM。
- 子进程 `powershell.exe -File` 执行并透传退出码，避免下载/解析失败仍标记成功。
- **Agent 修复**：`prepareScriptPath` 对已是 `.ps1` 的文件原地规范化，不再生成
  `run-command-xxx.ps1.ps1`；无扩展名的下载制品仍生成 `.ps1` 副本。
- 新增 Windows E2E 断言：`.ps1` 原地处理、无扩展名生成副本。

### 2026-10-24 修复 2008 R2 执行 install.ps1 语法错误（UnexpectedToken）

- **根因**：PowerShell 2.0 对**无 BOM** 的 `.ps1` 按系统 ANSI（简体中文为 GBK）解码。
  平台以 `text/plain` 原样下发 UTF-8 脚本，中文注释被拆成乱码字节，字符串引号失衡，
  解析器在第 232 行报 `UnexpectedToken`；外层命令又用 `exit $LASTEXITCODE`
  吞掉退出码，任务被误标为成功。
- 平台下载接口对脚本**统一补 UTF-8 BOM**（本地离线、缓存、上游三种来源都生效），
  仓库中的 `examples/windows_exporter/install.ps1` 也改为带 BOM 保存。
- Windows 命令示例改为**子进程执行** `powershell.exe -File`，并显式传递退出码；
  下载后先检查 BOM，缺失则用 UTF-8 重写，避免乱码与“失败却成功”。
- 新增单测：脚本接口返回 BOM，且 `?local=1` 与普通下载一致。

### 2026-10-24 Windows Server 2008 R2 legacy 通道

- **根因**：`iflyelf/windows_exporter` latest 基于上游 v0.32 / Go 1.27，Go 1.21 起
  不再支持 Windows 7 / Server 2008 R2；服务能注册但启动即失败。
- `iflyelf/windows_exporter` 新增 `.github/workflows/legacy.yml`：基于官方 v0.23.1 +
  Go 1.20.14 构建 `windows_exporter-{amd64,arm64}-legacy.exe`，并移植
  `hardware_info` 采集器（WMI，指标名一致）。
- `install.ps1` 按系统版本自动选通道（`-Flavor auto|latest|legacy`）：
  Windows 10 / Server 2016+ 用 latest；Win7 / 2008 R2 ~ 2012 R2 用 legacy。
- 修正最新版 CLI：textfile 参数为 `--collector.textfile.directories`（legacy 为
  `--collector.textfile.directory`）；两套默认配置分别使用 `include` / `services-where`。
- 服务启动失败时自动输出事件日志、`sc qc`、`--version` 和前台运行输出，避免只有
  「服务无法启动」而无从排查。
- 外部文件默认白名单新增 `windows-exporter-{amd64,arm64}-legacy`（指向 iflyelf 仓库）；
  迁移 028 为已有白名单自动补条目，页面可上传离线 legacy exe。
- legacy 默认配置启用 `hardware_info`（`node_hardware_*`）；配置模板升级到
  `config-version: 2`，旧的脚本生成配置会自动备份并重新生成。
- legacy 直连/内网镜像文件名统一为 `windows_exporter-<arch>-legacy.exe`，确保
  默认配置与含 `hardware_info` 的二进制匹配；官方 v0.23.1 需自备配置。
- Windows 组件示例的 `version_cmd` 改为 `windows_exporter.exe --version`，
  legacy 构建没有 Windows FileVersion 资源也能正确显示版本。

### 2026-10-24 安全与运维补强（审计 / 多租户 / 索引 / 探针）

- **创建用户密码策略**：`rbac.Create` 统一调用 `ValidatePassword`（≥8 位、≤72 字节），
  管理员创建账号不再能设置弱密码。
- **审计日志**：
  - 新增 `GET /api/audit-logs`（租户隔离、用户/操作/资源/结果/日期/关键字过滤、分页）；
  - 新增 `GET /api/audit-logs/export`（CSV，UTF-8 BOM，最多 2 万条）；
  - 前端新增「审计日志」页（仅管理员）；
  - 审计表新增 `tenant_id`，登录事件按用户名回填租户；
  - 设置项 `server.audit_retention_days`（默认 180，最小 7），维护任务自动清理。
- **索引**：迁移 026 增加 `upgrade_tasks(component_id)`、
  `(tenant_id,status,created_at)`；可选 `pg_trgm` 任务名 GIN 索引（无权限时自动跳过）。
- **任务状态筛选**：任务列表新增状态下拉（后端原本已支持 `status`）。
- **健康探针**：SPA 兜底只对浏览器页面导航生效；`/healthz`、`/readyz`、
  `/api/*` 与缺失静态资源一律 404，避免探针把 200 HTML 误判为健康。
- **多租户隔离**：迁移 027 为 `roles` / `teams` / `service_groups` /
  `org_departments` / `audit_logs` 增加 `tenant_id`：
  - 名称唯一约束改为租户内唯一；
  - 组织层查询从 ctx 推导租户（superadmin 可跨租户）；
  - 团队/服务组/角色/组织架构/审计均按租户隔离，内部后台调用保持兼容。

### 2026-10-24 Windows Agent 增强（灰度 / 看板 / 资源限制 / MSI）

- **自升级灰度**：`agent.auto_update_percent`（0-100），按 `agent_id` 稳定哈希选择，
  同一主机结果稳定；设置页可改。
- **版本分布看板**：新增 `GET /api/agents/stats` 与主机页「Agent 版本分布」卡片
  （版本/OS/架构、离线数、旧版本数）；Prometheus 新增
  `upgrade_mgr_agents_version_info`。
- **执行资源限制**：单命令脚本上限 1MB、超时上限 24h、stdout/stderr 各保留 4MB
  （超出截断并提示，实时流不受影响）。
- **最小权限服务账户**：`upgrade-agent install` 新增
  `--service-account` / `--service-password`；`windows_exporter` 示例脚本新增
  `-ServiceAccount` / `-ServicePassword`（需预先授予「作为服务登录」）。
- **MSI**：新增 `build/windows/agent.wxs` + `scripts/build_windows_msi.ps1`，
  CI 构建 `upgrade-agent-<version>-x64.msi`；安装时传
  `SERVERURL` / `ENROLLTOKEN`。
- **真机回归**：
  - `.github/workflows/windows-e2e.yml` 增加 PowerShell 7 用例、MSI 构建；
  - 新增 Windows arm64 自托管 runner 作业（`ENABLE_WINARM64_RUNNER=true`）；
  - 新增 `scripts/windows_runner_setup.ps1`（Go 1.20 + Runner，PS2 兼容解压）；
  - 2008 R2 作业继续由 `ENABLE_WIN2008R2_RUNNER=true` 控制。
- 新增示例告警：`deploy/k8s/prometheus-rules.example.yaml`。

### 2026-10-24 前端体验（分页 / 路由 / 表格 / 移动端 / i18n / E2E）

- **分页与搜索**：制品列表改为服务端分页（`/api/artifacts/paged`，支持组件/关键字）；
  组件列表增加关键字搜索与客户端分页（保持全量供下拉复用）。
- **URL 路由**：页面视图、主机/任务/组件/制品/审计的筛选与页码写入 hash，
  刷新/前进后退可恢复；登录后按 URL 直接进入对应页面。
- **表格工具**：主机/任务/组件/制品/审计表支持列显隐、列宽拖拽与记忆；
  组件表支持点击表头排序（客户端全量数据）。
- **移动端与可访问性**：窄屏单列布局、横向滚动标签/表格、弹窗自适应；
  tabs 增加 `role=tab`、表头 `scope=col`、弹窗 `role=dialog`、焦点可见样式。
- **英文模式**：顶栏/登录页可切换中英文，覆盖导航、按钮、表头、状态与常用标签
  （长帮助文本仍为中文）。
- **Playwright E2E**：`web/e2e` + `.github/workflows/web-e2e.yml`，覆盖登录失败/成功、
  导航与刷新恢复、创建组件、审计/组织页面、英文切换。

### 2026-10-24 任务列表按创建者筛选

- `GET /api/tasks` 支持 `created_by` 精确筛选；
- 新增 `GET /api/tasks/creators`：返回当前可见任务的创建者去重列表（最多 500），
  非管理员自动排除「仅创建过命令任务」的用户，避免泄露；
- 任务页新增「创建者」下拉（本人标注「（我）」），与组件/类型/搜索、分页可组合，
  清除筛选时一并重置。

### 2026-10-24 FlyIAM 组织树同步

**FlyIAM 侧（同仓库 `flyiam`，提交 `5b44d6f`）**新增服务间接口：

| 接口 | 说明 |
|------|------|
| `GET /api/users/export?page=&pageSize=` | 分页导出用户（`X-Total-Count` 为总数） |
| `GET /api/departments/export` | 从用户 `deptIdLv0/1/2`、`deptNameLv0/1/2` 推导组织树，含人数 |

**upgrade_mgr 侧**：

- `internal/pkg/flyiam`：`FetchUsers` 改为分页拉取并按用户名去重（兼容不支持分页的旧实现）；
  新增 `FetchDepartments`。
- 迁移 `025`：`org_departments` 组织快照表（`dept_id` 唯一、父级/层级/路径/人数）。
- `POST /api/flyiam/sync-departments`（管理员）：拉取上游组织树并**整体替换**本地快照
  （事务内先清空后写入，上游删除的部门不会残留）。上游返回空组织时**保留本地快照**
  并返回 502，避免数据源字段映射缺失导致页面被清空。
- `GET /api/org/departments`（管理员）：按完整路径返回，供页面展示。
- 前端「人员组织 → 组织架构」：缩进展示部门树、人数、完整路径与部门 ID，
  提供「从 FlyIAM 同步组织」按钮。
- 同时修复 `loadOrg` 的任务下标错位：组件已加载时会把 host-groups 误当组件列表。
- 单测：客户端分页/组织解析、组织快照替换与排序、同步接口落库。

> 前置条件：FlyIAM 需部署包含上述两个导出接口的版本（否则同步返回 404）。

### 2026-10-24 任务详情可见性、updated_by 与 Windows 真机回归

- **任务详情/日志可见性**：仅创建者、同团队成员、管理员可见；列表返回 `can_view`，
  前端无权限时隐藏「详情日志」并显示「无权限」。命令任务仍仅管理员可见。
- **updated_by 同步**：启动 / 暂停 / 推进 / 取消 / 重试都会写入当前操作者
  （`updated_by`）；后台/匿名调用（无 actor）保留最后一次人工操作人。
- **Windows 真机 E2E 流水线**：新增 `.github/workflows/windows-e2e.yml`
  - `windows-latest`：每次改动 Agent 相关代码即编译、`go vet`、真实执行
    PowerShell/cmd 用例（输出、退出码、stdin、中文脚本 BOM 预处理）；
  - Windows Server 2008 R2：自托管 runner（标签 `self-hosted,windows,server2008r2`），
    仓库变量 `ENABLE_WIN2008R2_RUNNER=true` 后启用；
  - 新增 `scripts/windows_agent_smoke.ps1`（PowerShell 2.0 兼容）验证
    `version` / `status` / `diagnose` CLI。
- **修复**：命令任务脚本在 Windows 上未做 UTF-8 BOM + CRLF 预处理，
  含中文的脚本会按系统 ANSI 解码而乱码/语法错误；现已与安装脚本统一。
- 新增 Windows-only E2E 测试（PowerShell/cmd 真实执行、BOM 校验），
  以及详情可见性、`TeamMates`/`SharesTeam`、`updated_by` 单测。

### 2026-10-24 团队成员添加改为多选

- 团队详情内改为从本地用户列表多选添加（自动排除已在团队的用户），
  不再手填用户名，避免拼写错误；全部用户已加入时给出提示。

### 2026-10-24 任务列表服务端分页

- `GET /api/tasks` 新增 `page` / `size`（默认 50，上限 200）/ `q`（任务名模糊搜索），
  响应由数组改为 `{list, total, page, size, pages}`（API 消费方需同步调整）。
- 前端任务页新增「任务名搜索」与上一页/下一页；切换筛选时回到第 1 页；
  自动刷新只刷新当前页，避免大任务历史全量拉取。
- 新增单测：q 搜索 + 分页边界 + 页码越界夹紧。

### 2026-10-24 非管理员任务访问打通（重要修复）

**问题**：任务相关路由此前全部挂 `RequireAdmin`，导致服务层已实现的「组件 deploy 权限 /
主机分组范围 / owner 策略」对非管理员完全不生效——非管理员根本进不来。

- **路由调整**：`/api/tasks` 的创建/预览/启动/暂停/推进/取消/重试/删除改为登录即可访问，
  细粒度校验全部下沉到服务层。
- **组件任务**：创建与预览要求对该组件有 `deploy` 权限（团队 → 服务组授权），
  并受授权主机分组（`app_code` / `biz_group`）限制；**启动/重试时再次校验**，
  防止授权被回收后仍能继续操作既有任务。
- **命令任务**：脚本与执行输出可能含敏感信息，仅管理员可创建/预览/查看；
  非管理员的任务列表自动过滤命令任务，详情按「不存在」处理。
- **owner 策略**：`agent.task_owner_only` 默认改为 **开启**（此前默认关闭）；
  团队 `owner_only` 可覆盖，其中 `off` 只允许**同一 off 团队**成员互相操作
  （新增 `org.CanShareTasks`，跨团队仍按仅本人处理）。
- **前端**：
  - 任务列表/详情返回 `can_operate`，按结果决定是否展示操作按钮；
  - 新增 `GET /api/my/permissions`，非管理员只看到有 `deploy` 权限的组件，
    无权限时给出「人员组织 → 团队」授权指引；
  - 顶部标签按权限显示：人员组织/外部文件仅管理员，设置仅 superadmin；
  - 命令任务选项仅管理员可见。
- **新增单测**：`/api/my/permissions`（管理员/团队授权）、任务列表可见性与
  `can_operate`、非管理员禁止命令任务、启动/重试的 deploy 复核、同团队共享。
- **测试数据清理修复**：多处测试用 `defer db.Close()`，其执行早于 `t.Cleanup`
  （关闭连接后清理 SQL 失败），会残留团队/组件/主机等数据并污染后续用例。
  统一改为 `t.Cleanup(func(){ db.Close() })`（LIFO 保证先删数据再关连接）。

### 2026-10-24 团队级 owner 策略、多选标签与 FlyIAM 一键开通

- **团队级任务操作策略**：迁移 `024` 为 `teams` 增加 `owner_only`：
  - `inherit`（默认，跟随设置页全局开关 `agent.task_owner_only`）
  - `on`（该团队成员只能操作自己创建的任务，比全局更严格）
  - `off`（**同一团队**成员之间可互相操作任务，不受全局开关影响；
    跨团队成员仍按仅本人处理）
  - 判定顺序：管理员豁免 → 任一所属团队 `on` 即受限（更严格优先）→
    全部 `off` 放行 → 其余回退全局。查询失败时按全局设置保守处理。
  - 团队页支持创建时选择策略、列表展示、详情内修改（立即生效）。
- **主机分组多选标签**：团队授权表单改为标签选择器（候选来自 `GET /api/host-groups`，
  支持手填/回车添加/点 × 移除），替换只能提示首个匹配的 datalist。
- **FlyIAM 一键开通本地账号**：新增管理员接口
  `POST /api/flyiam/users/{username}/provision`：
  - 校验用户确实存在于 FlyIAM，本地同名账号返回 409（绝不覆盖密码）；
  - 未指定密码时生成 16 位一次性随机密码，仅本次响应返回；
  - `sync_fields=true` 时把 FlyIAM properties 中与本地字段定义同名的值写入扩展字段；
  - 前端「FlyIAM 人员」页为未接入用户提供「创建本地账号」按钮。
- 新增单测：团队策略归一化/聚合优先级/更新、task 侧策略回退、FlyIAM 开通流程
  （随机密码、字段同步、重复 409、未知用户 404、指定密码不回显）。

### 2026-10-24 主机自定义字段前后端打通 + 迁移修正

- **主机自定义字段全链路**：
  - 前端主机编辑表单按 `/api/field-defs/host` 动态渲染控件（text/textarea/number/select/date），
    保存时随 `custom_fields` 一起提交（服务端按键合并）；
  - 主机列表按字段定义的 `showInList` 动态追加列。
- **迁移修正**：`host_field_defs` 缺少 `builtin` 列（020 遗漏），而通用字段仓储对
  user/host 共用同一套 SQL，导致主机字段接口报 `column "builtin" does not exist`。
  新增迁移 `023` 补列（020 同步修正，新装环境直接带上）。
- 新增单测：主机自定义字段写入/未知键丢弃/数字校验。

### 2026-10-23 任务创建者展示与 owner 策略完善

- 任务列表返回 `created_by`，前端新增「创建者」列并标注「（我）」，便于判断 owner 限制。
- 移除 `task.Service` 上无用的空 `SetActor`（actor 统一从请求 context 读取）。
- owner 策略相关单测覆盖：创建者/他人/管理员/superadmin/匿名/策略关闭。

### 2026-10-23 任务 owner 校验、主机分组下拉与 FlyIAM 独立页面

- **任务操作 owner 校验**：新增设置 `agent.task_owner_only`（初始默认 false，后续版本已改为默认开启）。
  开启后，非管理员只能「启动/暂停/推进/取消/重试/删除」自己创建的任务；
  管理员与 superadmin 豁免；匿名内部调用（后台编排）不受影响。actor 从请求 context 读取。
- 迁移 `022`：`upgrade_tasks.updated_by` + `created_by` 索引；创建任务时两者同时写入。
- **主机分组下拉多选**：`GET /api/host-groups` 从 `hosts.app_code` / `biz_group`
  聚合去重，授权表单改用 `datalist` 提示，避免手填错。
- **FlyIAM 人员独立页面**：「人员组织 → FlyIAM 人员」表格展示用户名/姓名/邮箱/工号，
  并与本地账号对照标记「已接入 / 未接入」，支持搜索（不再用弹窗摘要）。
- 新增单测：owner 校验（创建者/他人/管理员/匿名/开关关闭）、created_by 写入、
  主机分组聚合去重。

### 2026-10-23 任务权限校验、主机分组范围与 FlyIAM 用户只读

- **任务权限校验（deploy）**：`task.Service` 增加 `SetAuthorizer`，由 `org.DeployAuthorizer`
  注入。规则：admin/superadmin 放行；其他用户必须在包含该组件的服务组上拥有 `deploy`
  权限，否则创建任务直接返回 403 语义错误（提示去「人员组织 → 团队」授权）。
  `CreateRequest.actor` 只能由 handler 注入，**不从 JSON 读取**，无法伪造身份。
- **主机分组范围**：迁移 `021` 为 `team_group_permissions` 增加 `host_groups TEXT[]`。
  授权可按 `hosts.app_code` / `biz_group` 限定主机；创建任务时逐台校验，
  超出范围的主机在错误信息中列出（最多 10 台）。空数组或 `*` 表示不限（保持旧行为）。
- `org.EffectivePermissionsWithHosts` 同时返回「组件权限」与「主机分组范围」，
  多团队授权自动取并集。
- **FlyIAM 用户只读展示**：`internal/pkg/flyiam` 增加 `FetchUsers`
  （`/api/users/export`），接口 `GET /api/flyiam/users`（管理员，只读）；
  前端「用户字段」页增加「查看 FlyIAM 用户」。用户数据仍以 Casdoor/FlyIAM 为准，本系统不写回。
- 前端授权表单新增「主机分组范围」输入，授权列表显示该列。
- 新增单测：任务授权注入与透传、主机分组范围（空/`*`/具体值/错误透传）、
  `actor` 不可由 JSON 注入、权限并集、FlyIAM 用户拉取。

### 2026-10-23 人员组织、字段自定义与 FlyIAM 集成

**人员组织（与 consul_mgr 对齐，照搬服务组模型）**

| 实体 | 说明 |
|---|---|
| 用户扩展字段 `user_field_defs` | 人事字段定义（键/名称/类型/选项/列表与表单可见性/排序），页面可增删 |
| 用户字段值 `user_field_values` | 按 (用户, 字段键) 存储；新增字段无需改表 |
| 角色 `roles` | 可复用权限集合（read/write/delete/deploy） |
| 团队 `teams` / `team_members` | 成员集合；团队是授权主体 |
| 服务组 `service_groups` / `_components` | 组件集合（替代 consul_mgr 的 Consul 服务组） |
| 团队授权 `team_group_permissions` | 直接权限 + 引用角色（并集）+ 授权服务（`*`=组内全部） |

- 内置 6 个人事字段（工号/零级~二级部门/编制类型/上级账号），与 FlyIAM 键一致。
- 角色被团队授权引用时**拒绝删除**；删除团队级联清理成员与授权。
- `EffectivePermissions(username)` 计算用户通过团队获得的组件级权限，供后续任务下发鉴权。
- 前端新增「人员组织」页签（人员信息 / 用户字段 / 角色 / 团队 / 服务组 五个子页），
  支持编辑人员字段、增删字段与角色、管理团队成员与授权。

**主机字段自定义**

- `host_field_defs` 存定义，`hosts.custom_fields`(JSONB) 存值；
- 主机编辑接口新增 `custom_fields`：按定义校验（未知键丢弃、数字/日期/下拉选项校验）后
  **按键合并**，不会覆盖其它自定义字段；
- 主机列表返回 `custom_fields`，前端可直接展示。

**FlyIAM 集成**

- `internal/pkg/flyiam`：`X-Service-Token` 调用
  `/api/casdoor/app-credentials`（应用凭据）与 `/api/user-fields/export`（字段定义）；
- 设置页新增「FlyIAM」分组（地址 + 服务令牌，令牌密文存储）；
- `POST /api/flyiam/test` 连通性测试（回显 application/organization，绝不返回 ClientSecret）；
- `POST /api/flyiam/sync-user-fields` 按 `field_key` 幂等同步字段定义：
  **本地新增不会被删除**，内置字段保留 builtin 标记。

**迁移 `020_org_and_fields`**：8 张新表 + `hosts.custom_fields` + 默认服务组（纳入全部已有组件）。

### 2026-10-23 外部文件：服务端分页、批量刷新与可用性指标

- **服务端分页 + 搜索**：`GET /api/external/binaries?page=&size=&q=`（size 上限 200，
  不传 page/size 仍返回全部以兼容旧行为）。表格页改为服务端分页，搜索按回车/按钮触发。
- **保存语义改为 merge**：`PUT /api/external/binaries` 支持 `merge=true` 与 `deleted[]`。
  分页后前端只持有当前页，必须用合并语义，否则保存会把其它页条目清空。
- **批量刷新**：`POST /api/external/binaries/refresh-all`，逐条重下并返回成功/失败明细；
  与单项刷新、普通下载共用同一把文件锁。
- **Prometheus 指标（含可用性）**：
  | 指标 | 说明 |
  |---|---|
  | `upgrade_mgr_external_binary_up{name,type}` | 上游是否可用（1/0） |
  | `upgrade_mgr_external_binary_probe_seconds{name}` | 最近一次探测耗时 |
  | `upgrade_mgr_external_binary_probe_total{name,result}` | 探测次数（success/error） |
  | `upgrade_mgr_external_binary_cached{name}` / `_cached_bytes{name}` | 缓存状态与大小 |
  | `upgrade_mgr_external_binary_cache_age_seconds{name}` | 缓存年龄 |
  | `upgrade_mgr_external_binary_requests_total{name,result}` | 下载请求（cache_hit/fresh/error/refresh/refresh_error） |
  | `upgrade_mgr_external_binary_download_seconds{name,result}` | 上游下载耗时 |
  | `upgrade_mgr_external_binary_bytes_total{name}` | 已下发字节数 |
- 新增 `internal/pkg/extbinprobe`：每分钟串行探测全部条目（只取前 64KB，不落盘），
  失败不影响其它条目；每个副本都会探测，多副本时指标按实例分别统计。
- 探测与下载共用同一套校验（域名白名单、内网放行、重定向逐跳校验），
  保证「指标显示可用」= 「主机实际能下到」。

### 2026-10-23 外部文件页增强与并发/死锁修复

- 表格页每行新增「复制地址」（复制完整平台 URL，便于直接粘贴给主机/脚本）
  与「刷新缓存」（强制删除旧缓存并重新下载，上游更新后主机立即拿到新文件）。
- 新增搜索框（按名称/上游地址过滤）与前端分页（每页 20 条）；
  输入时保持焦点与光标位置（统一 `state.focusId` 机制）。
- 新增 `POST /api/external/binaries/refresh`（管理员，审计 `external_binary.refresh`）。
- **并发/死锁修复**：
  - 新增 `internal/pkg/keyedlock`：按文件 key 的互斥锁，**用完自动清理**，
    修复原 `sync.Map` 保存 `*sync.Mutex` 导致 key 无限增长的内存泄漏；
  - 下载锁只覆盖「检查缓存 + 下载」阶段，拿到后立即释放；
    原实现在 `ServeContent` 流式输出期间仍持锁，会阻塞同文件其它请求。
  - 刷新接口与普通下载共用同一把文件锁，避免两个请求同时写同一文件。
  - 保存白名单改为**一次 `Apply`**，修复此前「先写策略再写白名单」两次写库
    导致的半更新（前者成功、后者失败时配置不一致）。
- 修复 `probeExternalBinary` 的经典坑：用命名返回值，否则 `defer` 里设置的
  `elapsed_ms` 被值拷贝丢弃（测试耗时恒为 0）。
- 下载实现增加**重定向逐跳校验**：白名单域名 302 到内网/环回时同样拒绝（防 SSRF 绕过）。
- 新增 `keyedlock` 并发单测、刷新/内网拒绝/耗时写回单测；核心包通过 `-race`。

### 2026-10-23 外部文件白名单表格页 + 测试下载 + 内网放行

- 新增独立页签「外部文件」：表格页支持**增删行**，比多行文本更易维护。
  列：名称 / 上游地址 / 类型（脚本勾选）/ 平台地址 / 缓存状态 / 操作（测试、删除）。
- 「测试」按钮立即拉取（只读前 64KB 校验，不落盘）：返回 HTTP 状态、大小、耗时，
  失败给出原因与提示；可测试尚未保存的候选地址。测试与真实下载共用同一校验函数，
  保证「测通即能下载」。
- 新增内网放行：
  - `server.external_binary_allow_private`：是否允许内网/环回（默认 false）；
  - `server.external_binary_allow_cidrs`：精确放行网段/IP（推荐），
    如 `10.0.0.0/8,192.168.1.10`；不在放行范围内的内网地址仍拒绝。
  - 内网镜像站需要同时把其域名/主机加入「允许的上游域名」。
- 接口：`GET/PUT /api/external/binaries`、`POST /api/external/binaries/test`（管理员）。
- 保存顺序修正：先写域名/内网策略，再写白名单，避免自定义域名被旧 hosts 误拒；
  设置页的域名校验改用「本次提交的 hosts」。
- 缓存状态（是否已缓存/大小）直接显示在表格中，便于确认主机能否立即拉取。

### 2026-10-23 外部文件代缓存改为通用白名单（可页面配置、热加载）

- 白名单不再硬编码：`server.external_binaries`（配置文件 map 或「设置 → 外部文件代缓存」）：
  - 键 = `/api/external/binary/{name}` 的 name；值 = 上游 URL，前缀 `script:` 表示文本脚本；
  - 环境变量 `EXTERNAL_BINARIES` 支持 `name=url,name2=script:url2`。
- 支持热加载：接口每次请求读取设置快照，页面保存后立即生效，**无需重启**；
  白名单为空时回退内置默认（windows_exporter amd64/arm64 + install.ps1），升级不断流。
- 新增 `server.external_binary_hosts`（上游域名白名单，默认 github.com /
  raw.githubusercontent.com / proxy.xiaonuo.live）与 `server.external_binary_ttl`（默认 24h）。
- 2026-10-26 起新增 `server.external_binary_proxy` / `server.external_binary_proxy_hosts`：
  默认缓存下载先走 `https://proxy.xiaonuo.live/proxy?url=`，失败再直连兜底；
  设置页可改代理地址/域名，留空代理地址表示完全直连。
- SSRF 防护：只允许配置内名称、只允许白名单域名、拒绝解析到环回/私网/链路本地地址、
  200MB 上限、内容校验（exe 必须 MZ、脚本不能是 HTML）、同一文件并发下载串行化。
- 设置页支持多行文本控件（`type: text`），白名单按行编辑；`Snapshot()` 深拷贝 map，
  避免并发读写下 data race。
- 启动时校验配置白名单（域名不在白名单直接启动失败，便于尽早发现配置错误）。

### 2026-10-23 Server 2008 R2 支持：平台代缓存与 PS2 兼容修复

日志证据（17:05）：`PowerShell 2.0`、`CLR` 为 .NET 2.0，`TLS 1.2 可用: False`，
脚本执行到入口就结束、服务未注册。三条根因与修复：

1. **PS2/.NET 2.0 不支持 TLS 1.2** → 无法访问 GitHub / proxy。
   - 控制台新增 `GET /api/external/binary/{name}`：由服务端（Go，支持 TLS 1.2）
     代为下载并缓存 `windows_exporter-amd64.exe` / `arm64` / `install.ps1`，
     主机用 **HTTP** 从这个接口拉取即可，`<agent_binary_dir>/.external-cache` 缓存 24h，
     带 MZ / 非 HTML 内容校验。
   - Windows 组件示例的 `script_url` 改为平台相对路径
     `/api/external/binary/windows-exporter-install.ps1`（Agent 自动补全服务端地址并带凭据）。
   - `install.ps1` 新增 `-BinaryPath`（本地 exe）与 `-BinaryUrl`（内网地址），
     并在直连失败时自动回退到 `UPGRADE_AGENT_SERVER_URL` 对应的平台地址。
   - Agent 把当前服务端地址注入子进程环境变量 `UPGRADE_AGENT_SERVER_URL`
     （切换连接地址时同步更新）。
2. **`[Enum]::Parse(..., 'Tls12')` 在 .NET 2.0 抛「未找到值 Tls12」** →
   改用数值位或（3072），并在不支持时明确提示改用平台地址/本地 exe。
3. **PS2 中 `exit` 在函数/管道里可能被吞掉，退出码仍为 0** → `Fail` 改用
   `[Environment]::Exit`；脚本外层加 try/catch 兜底；日志统一走
   `[Console]::Out`（`Write-Output` 会被调用方 `| Out-Null` 吞掉），并把
   stdout 设为 UTF-8（原日志出现中文乱码）。
4. 执行器合并 stdout+stderr 输出（PowerShell 的异常信息走 stderr，原实现会丢）。

### 2026-10-23 修复 Windows 脚本「执行成功但实际没装」

- 现象：日志显示「执行安装脚本」后立刻进入启动/健康检查，最终健康检查失败，
  但没有任何脚本输出，服务始终未注册。
- 根因一：`install.ps1` 全部使用 `Write-Host`。Windows PowerShell 的 Write-Host 写入
  host/information 流，平台捕获的 stdout 里看不到任何内容，因此无法判断脚本是否真的执行。
  改为 `Write-Output`，并在入口打印动作/端口/安装目录/PowerShell 版本。
- 根因二：下载的脚本是无 BOM 的 UTF-8（含中文），Windows PowerShell 会按系统 ANSI(GBK)
  解码，可能出现乱码或语法错误。Agent 在执行前统一转成「UTF-8 BOM + CRLF」临时副本。
- 新增下载内容校验：空文件、HTML/XML 错误页（代理/网关返回 200）会直接报错，
  不再让解释器“成功”执行空脚本。
- 服务声明了但安装后仍未注册时，健康检查前先输出根因提示。
- 配置写入改用 UTF8（原 ASCII 会丢中文）。

### 2026-10-23 修复任务完成后仍显示「已暂停」

- 根因：`auto_promote=false` 时最后一批完成后任务被置为 `paused`，而编排器只扫描
  `status='running'`，永远不会进入 `finishTask`，页面因此长期显示「已暂停」，
  实际上所有批次都已成功。
- 编排器改为同时扫描 `running` 与 `paused`；`paused` 任务不下发、不自动推进，
  但「所有批次已结束」时正常终结。
- 最后一批不再置 `paused`，直接按逐机结果终结（有失败即 `failed`），
  避免页面出现「已暂停」闪现或残留。
- `finishTask` 的状态条件放开到 `running|paused`；`advance` 增加 `taskStatus` 参数，
  暂停任务不会被自动派发下一批。
- 新增集成测试：暂停任务在全批次完成后终结、暂停任务不自动下发。

### 2026-10-23 Windows 接入命令补充网络失败提示

- `buildWindowsInstallCommand` 下载步骤改为单行 `try/catch`：失败时打印真实错误
  并提示 `Test-NetConnection <host> -Port <port>` 自检，不再继续执行导致
  「系统找不到指定的文件」误导排查方向。
- `Start-Process` 增加 `if (Test-Path $exe)` 保护，未下载成功时直接给出结论。
- README 增加「Windows 主机下载失败 / 连不上平台怎么办」排查小节：
  地址必须是 Windows 主机视角可达、服务端监听与防火墙检查、隧道场景直接填隧道地址、
  已安装旧地址可改用「连接地址覆盖」热切换。

### 2026-10-23 修复 Windows 接入命令引号错误

- 修复 `buildWindowsInstallCommand` 生成 `''http://...''`（psQuote 已带引号又外层再加）导致
  PowerShell 报「方法调用中缺少“)”」的问题；粘贴后下载直接失败、后续 Start-Process 报文件不存在。
- 生成命令改为 5 行独立语句，并用 `$exe` 保存临时路径：逐行粘贴、控制台自动换行都不会截断表达式。
- 新增回归测试：断言不出现 `''`、单引号成对、关键片段完整。
- README 中 Windows 接入命令同步更新。

### 2026-10-23 windows_exporter 示例与 diagnose

- 新增 `examples/windows_exporter/install.ps1`（install/update/uninstall/status 四动作，
  PowerShell 2.0 兼容）与 `examples/windows_exporter/config.yaml`（含 service.exclude 清单，
  textfile 目录改为 `C:\monitor\windows_exporter\textfile_inputs`）。
- 组件示例新增 Windows 三项：`windows_exporter_script` / `_update` / `_uninstall`，
  列表与表单按钮按 Linux / Windows 分组，任务页示例同样区分（名称带「Linux 示例」「Windows 示例」）。
- 任务命令示例新增 Windows 三项（安装/更新/卸载脚本），解释器预填 `powershell.exe`。
- 新增 `upgrade-agent diagnose`：主动拨测 DNS → TCP → TLS → HTTP /health 并输出各阶段耗时，
  可选 `--heartbeat` 验证鉴权链路；`status` 增加「服务端覆盖 vs 本机生效」对比。

### 2026-10-23 Agent status 子命令

- 新增 `upgrade-agent status`：输出当前连接地址及来源（默认/服务端覆盖/备用地址）、
  默认地址、备用地址列表、出口 IP、注册状态、最近心跳、连续崩溃、待补报条数、
  最近一次切换（从哪到哪、原因、时间）与 TLS 状态。
- 支持 `--json` 便于脚本采集，`--state` 指定状态文件；只读本地 state，断网可用，
  不输出任何凭据；未注册时退出码为 2。
- Agent 在切换连接地址时把原因/时间写入 state（`applied_override`、`last_switch_*`），
  跨进程可见，重启后仍能追溯。

### 2026-10-24 配置项全面页面化（设置页覆盖 config.yaml）

- **设置页从 42 项扩充到 81 项**，`deploy/config/config.yaml` 中除「数据库连接」与
  三个首启动凭据外，全部可在页面维护；保存即写 `app_settings`（优先级最高）。
- 新增可页面配置：`server.host/port/web_embedded/trust_proxy/trusted_proxies/
  cookie_secure/metrics_token/session_secret/agent_binary_dir`、
  `consul.timeout/sync_on_start`、`casdoor.redirect_uri`、
  `artifact.max_size/storage_type` 与完整的 S3/OSS 参数、
  `agent.require_enroll_token/enroll_token_ttl`、`auth.provider/admin_username`。
- **区分「即时生效」与「需重启」**：`Item.Restart` 标记启动时绑定的配置（监听地址、
  认证方式、制品后端、代理信任链等），页面显示红色「需重启」标签，保存后弹窗
  列出本次真正改动的需重启项，避免改完以为已生效。
- **数据库连接改为只读展示**：启动顺序是「先连库 → 再读设置」，连接参数不可能
  由页面提供。页面展示当前生效值（不含密码）便于确认连的是哪个库；服务端在
  `Apply` 中显式拒绝修改只读项，不只依赖前端禁用。
- **S3/OSS 参数补齐**：此前 `storage_type` 可在页面选 `s3`，但端点/桶/密钥只能写
  配置文件，选了必然降级回 local，属于死路；现在同组内可一并填写。
- **三个历史无效配置现已全部实现**：
  - `server.cors_origins`：严格 http/https Origin 白名单，支持带 Cookie 的跨域请求与
    OPTIONS 预检；不支持 `*`，设置页保存后即时生效，Redis HIT/MISS 都会添加一致的头；
  - `consul.concurrency`：同步及来源键扫描按服务有界并发查询（1-128），worker pool
    数量固定；结果仍按服务名排序聚合，避免并发完成顺序改变 Meta 冲突结果；
  - `agent.clock_skew`：通过心跳下发到 Agent、写入 state，并用于 HMAC 指令验签；
    设置页修改后最迟一个心跳周期生效，限制为 30s-30m（旧 Agent 默认 10m 兼容）。
- 新增 `internal/setting/registry_test.go`：覆盖键重复、Get/Set 完整性、
  只读项拒改与不落库、需重启项有提示、secret 脱敏。

### 2026-10-24 主机字段映射（去硬编码）+ consul_mgr 主机同步

- **去掉硬编码的 Meta 映射**：原先 `internal/logic/sync` 把 7 个 ServiceMeta 键
  （`account`/`app_code`/`group`/`department`/`vendor`/`os_type`/`host_type`）
  写死映射到固定列，加一个来源键就要改代码，自定义字段也拿不到 Consul 数据。
  现在映射关系存库（`host_field_mappings`），页面可增删改：
  - `target_kind=column`：写入 hosts 内置列，列名限定在白名单内；
  - `target_kind=custom`：写入 `hosts.custom_fields`，键须已在 `host_field_defs` 定义；
  - `overwrite=false`（默认）只补空值，不覆盖人工维护的内容；`true` 以外部为准；
  - 多条映射指向同一字段时按 `sort_order` 先到先得，可配置「取 app_code，没有就取 appCode」这类回退链。
  - 迁移 029 的种子数据与原硬编码行为完全一致，升级后同步结果不变。
  - 安全：列名只来自 `hostfieldmap.AllowedColumns` 白名单（迁移里另有 CHECK 约束兜底），
    不拼接任何外部输入；`ip`/`agent_id`/`source`/`tenant_id` 与 Agent 实测的
    `os_family`/`arch` 等一律不可被映射覆盖。

- **从 consul_mgr 同步主机**：consul_mgr 的每个服务组各自配置独立的 Consul
  地址与令牌，一个环境常有多套 Consul，而 upgrade_mgr 的 `consul` 配置只能连一套。
  新增 `POST /api/hosts/sync-consul-mgr`：一次拿到全部服务组的主机，并带上服务组归属。
  - consul_mgr 侧新增只读服务间接口 `GET /api/service/hosts`（按 IP 聚合，
    返回完整 ServiceMeta + 服务组归属 + 健康状态），用 `X-Service-Token` 鉴权，
    与用户会话完全隔离；令牌留空则不注册该路由（默认关闭，零暴露面）。
    接口只读，不返回 `consul_token` 等凭据。
  - 服务组名/编码、服务名、标签、健康状态等结构化信息以 `__` 前缀的保留键
    （`__group_name`、`__services` 等）并入来源键空间，和普通 Meta 一样可被映射。
  - 主机 `source` 标记为 `consul_mgr`，与直连 Consul 的主机区分；两种来源各自
    独立的同步锁，可并行。**不做「清理已移除主机」**：服务组可能被临时禁用或
    连接失败，据此删本地主机风险过高，清理仍只由直连 Consul 的同步负责。
  - 部分服务组查询失败时返回 `partial=true` 与失败组名，页面明确告警。

- **扫描来源键 → 人工勾选导入**：不清楚外部有哪些键时，
  `GET /api/host-field-keys?source=` 真实拉一次数据，统计每个键出现的主机数与
  样例值，并标注是否已映射；勾选后 `POST /api/host-field-keys/import` 按键名
  创建主机自定义字段并建立映射。**绝不自动创建字段**，避免外部 Meta 污染字段空间。
  导入按 `(source, source_key)` 幂等，已存在的映射会跳过而非覆盖。

- **新增接口**：`GET/POST /api/host-field-mappings`、
  `PUT/DELETE /api/host-field-mappings/{id}`、`GET /api/host-field-keys`、
  `POST /api/host-field-keys/import`、`POST /api/hosts/sync-consul-mgr`、
  `POST /api/consul-mgr/test`（连通性与令牌自检）。

- **配置项**：`consul_mgr.endpoint/service_token/timeout`，
  环境变量 `CONSUL_MGR_ENDPOINT/CONSUL_MGR_SERVICE_TOKEN/CONSUL_MGR_TIMEOUT`；
  页面「设置 → consul_mgr 对接」可改并即时生效。
  consul_mgr 侧对应 `ServiceAPI.Token`（env `CONSUL_MGR_SERVICE_API_TOKEN`）。
- **服务组在页面选择**：主机页「同步 consul_mgr」旁的下拉列出对方的服务组
  （`GET /api/consul-mgr/groups` → consul_mgr 的 `GET /api/service/groups`），
  默认「全部服务组」。候选只含「启用且已配置 Consul 地址」的组，与同步口径一致；
  接口只回 id/name/code/datacenter，不含 consul_address 与 consul_token。

- **页面**：「人员组织 → 主机字段映射」管理规则 + 扫描弹窗；主机页新增
  「同步 consul_mgr」按钮。映射的来源列表、内置列候选、保留键说明全部由后端下发，
  前端不硬编码任何键名。

- **验证**：新增 `hostfieldmap` 单测（白名单、回退链、保留键、覆盖语义）与
  `upsert` 的真实库集成测试（映射写入、未映射键不入库、overwrite 两种语义、
  空映射集）。迁移 029 在真实 PostgreSQL 上验证，种子数据与原行为逐条比对一致。

### 2026-10-24 顶栏菜单 + 人员组织对齐 + Redis 缓存

- **页面布局对齐 consul_mgr**：顶栏改为一级菜单 + 下拉（主机/组件/制品/任务、
  人员组织、运维、帮助），右侧主题/语言/用户头像菜单；窄屏自动切换为横向页签。
- **「接入主机示例」移入「帮助」菜单**：当时主机页不再占用大块空间；后续 2026-10-26 又在主机页顶部提供完整接入卡片，帮助菜单入口保留。帮助弹窗内可在
  「快速上手」与「接入主机示例」间切换，支持移动端。
- **表格与弹窗排版修复**：任务详情摘要改为自适应网格，状态/时间等短值不换行；
  长文本列自动换行，操作列固定 nowrap；弹窗 z-index 高于顶栏，关闭按钮不再被遮挡。
- **人员组织对齐 consul_mgr**：用户管理改为「搜索 + 新增用户 + 刷新」工具栏与
  编辑弹窗，支持编辑显示名/角色/租户、重置密码、启用/禁用、删除；后端新增
  `DELETE /api/users/{username}` 与 `POST /api/users/{username}/reset-password`。
  创建用户密码留空时生成一次性随机密码（仅响应中返回一次）。
- **Redis 缓存（单点 / 集群 / 哨兵）**：
  - 新增 `internal/pkg/cache`：统一键前缀、默认 TTL、JSON 读写、按前缀清理；
    Redis 不可用自动降级为直连数据库，不阻塞启动。
  - 新增只读 API 缓存中间件：`/api/hosts`、`/api/tasks`、`/api/components`、
    `/api/artifacts`、`/api/settings`、人员组织等命中缓存返回 `X-Cache: HIT`；
    任意写请求自动失效缓存；键按会话隔离，不会串租户数据。
  - 设置页新增「Redis 缓存」卡片：展示连接状态/模式/实际 DB/TTL/前缀/最近错误，
    支持「测试连接」「清空缓存」；保存 `redis.*` 设置后即时重建连接，无需重启。
  - 新增 `GET /api/cache/status`、`POST /api/cache/test`、`POST /api/cache/flush`。
  - 配置项：`redis.enabled/mode/host/port/hosts/master_name/password/db/ttl/prefix`，
    环境变量 `REDIS_ENABLED/REDIS_MODE/REDIS_HOST/REDIS_PORT/REDIS_ADDRS/
    REDIS_MASTER_NAME/REDIS_PASSWORD/REDIS_DB/REDIS_CACHE_TTL/REDIS_PREFIX`。
  - 兼容性：Redis Cluster 固定 DB 0；单点/哨兵若配置的 DB 超出 `databases`
    范围，自动回退到最大可用 DB 并在状态/日志中提示；键前缀仍保证逻辑隔离。
- **E2E**：Playwright 用例更新为顶栏下拉交互，并新增「帮助菜单」「人员组织」
  「英文切换」断言，7/7 通过。

### 2026-10-24 主机自定义字段管理界面 + 主机列表独立 TTL

- **主机字段定义可在页面维护**：后端 `/api/field-defs/host` 早已具备完整 CRUD，
  但前端只有「用户字段」入口，主机字段只能靠接口或改库维护。现「人员组织」新增
  **主机字段** 子页签（顶栏下拉与窄屏页签同步），支持新增 / 编辑 / 删除，
  类型覆盖 text / textarea / number / select / date。
  - `showInList` 勾选后作为主机列表的一列，`showInForm` 控制是否出现在「主机 → 编辑」；
  - 编辑时**锁定字段键**：后端改键不会迁移已存的值（`hosts.custom_fields` JSONB），
    会把历史数据变成孤儿，需要换键时请新建字段；
  - 删除会连带清理各主机该字段的值，弹窗二次确认并明确提示；
  - 字段定义改动后主机页缓存的定义同步失效，列表列立即按新定义渲染；
  - 「用户字段」与「主机字段」共用 `state.orgNewField`，切换子页签时统一清空，
    避免在一个页签打开表单后切到另一个、提交到错误的 kind。
- **`/api/hosts` 支持独立 TTL**：新增 `redis.hosts_ttl`（秒，0=沿用 `redis.ttl`，
  上限 300）。几万台规模下主机列表是最重的查询，而其内容只由页面写操作改变，
  任意写请求都会清空 `http` 缓存前缀，所以放长到分钟级不会让操作看起来没生效。
  - 小于通用 TTL 时自动抬到通用值，超过 300 秒按上限生效并记日志；
  - 环境变量 `REDIS_HOSTS_CACHE_TTL`；设置页「Redis 连接配置」可直接填；
  - 状态卡片与 `GET /api/cache/status` 增加「主机列表 TTL」（`hosts_ttl_seconds`），
    未单独配置时显示「与通用 TTL 相同」；
  - Agent 心跳/版本变化最迟在该时长后可见，这是放长 TTL 的唯一代价。
- **验证**：`go build ./...`、`go vet ./...` 全绿；新增 `internal/middleware/cache_ttl_test.go`
  与 cache 包 TTL 用例；用真实 Redis（10.1.230.44:7000）验证四种 TTL 组合
  （回退 / 显式 / 超限收敛 / 低于通用值抬升）与读写往返均符合预期。
