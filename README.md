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
- 页面设置化（DB 优先 / env 兜底），改动即时生效
- IP 段表达式解析：`10.1.255.24-26,10.1.255.38,10.1.255.0/24,10.1.26.5-10.1.26.7`（含排除、展开上限）
- 直接 exec 命令执行器（无 shell，防注入）；HMAC 指令签名；sd_notify 僵死检测（供 Agent 保活）
- 三主题（🌞暖沙米/🌊冷蓝/🌙暗黑）+ Mac 圆角 + H5 自适应，前端 `go:embed` 内嵌单二进制

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
- **enroll-token 生成**：管理员生成一次性注册令牌 + 一键安装命令（`GET /api/agent/enroll-token`）
- **主机升级历史**：`GET /api/hosts/{ip}/upgrade-history`，展示逐机升级记录
- **主机管理**：服务端分页（几万台主机不一次性加载）、编辑业务字段、删除主机（未完成任务保护 + Agent 凭据撤销）

**P5 企业级增强**
- **Prometheus 指标**：`/metrics` 端点暴露关键指标（Agent 在线数/心跳延迟/任务进度/失败率/指令执行时间），支持 Grafana 监控大盘
- **多租户 RBAC**：数据库增加 tenant_id 列（hosts/components/tasks），租户隔离查询，支持 admin/user/viewer 角色权限控制
- **分布式编排器**：基于 PostgreSQL advisory lock，多控制台实例安全并行（每轮仅一个副本推进批次，自动故障转移）；单机与多副本共用同一逻辑，不依赖 Consul
- **制品外部存储**：抽象 Backend 接口，支持本地/S3(MinIO)/阿里云 OSS 可切换，预签名 URL 下载

**P6 数据库自动化**
- **自动创建**：首次启动自动创建所有表结构，无需手动执行 SQL
- **版本化迁移**：使用 go:embed 内嵌迁移脚本（001_init, 002_multi_tenant, ...），自动检测并应用未执行的迁移
- **自动维护**：每日凌晨 2 点自动清理过期数据（7 天过期指令/30 天旧指令/90 天旧任务）、VACUUM ANALYZE、索引重建
- **零停机更新**：迁移在事务内执行，失败自动回滚，支持幂等重试

---

## 技术栈

Go 1.26（`CGO_ENABLED=0`，交叉编译 linux/amd64、arm64、arm）· 标准库 net/http（Go 1.22 路由）· PostgreSQL(`lib/pq`) · hashicorp/consul/api · Prometheus · 前端原生内嵌（后续可替换为 Vue3 构建产物）

Agent 构建矩阵：Linux 普通构建（Go 1.26）、Linux legacy（Go 1.23，CentOS 6）、
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

启用 Consul 同步（可选，或在设置页配置；`datacenter` 留空自动探测）：
```bash
export CONSUL_ENABLED=true CONSUL_ADDRESS=http://10.0.56.118:8500 \
       CONSUL_TOKEN=xxx CONSUL_SYNC_INTERVAL=5m
```

---

## 部署方式

> 下载均通过 `https://down.xiaonuo.live?url=` 代理加速，`wget` 支持断点续传(`-c`)。
> Linux 按需把 `amd64` 换成 `arm64` 或 `arm`；CentOS 6 请使用 `*-legacy` 版本。
> 接入 **Windows 主机**（Server 2008 R2 ~ Windows 11）时，还要把
> `upgrade-agent-windows-amd64.zip`（或 `arm64`）解压到控制台的 `dist` 目录，否则下载 404。
>
> **Windows Agent 是否包含在 Release 里？** 包含。CI 的 `agent-windows` 作业会构建
> `upgrade-agent-windows-{amd64,arm64}.exe` 并打包为 `.zip`，随 `latest`/版本 Release 一起发布；
> 但 **Docker 镜像只内置 Linux 当前架构**，接 Windows 主机需按下文把 `dist` 挂进容器。

### 方式一：systemd（推荐，直接跑在宿主机）

```bash
# 1. 下载控制台二进制到 /tmp 并解压
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-mgr-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-mgr.tar.gz -C /tmp

# 1.1 下载 Linux Agent 二进制（供控制台生成一键安装命令时下载）
wget -q -c --no-check-certificate -O /tmp/upgrade-agent.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-agent.tar.gz -C /tmp

# 1.2 下载 Windows Agent 二进制（Windows 主机接入必需，缺了会 404）
wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-amd64.zip \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.zip"
unzip -o /tmp/upgrade-agent-windows-amd64.zip -d /tmp

# 可选：按需补充其他平台/架构（Windows on ARM、Linux arm64、CentOS 6 legacy）
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-arm64.zip \
#   "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-arm64.zip"
# unzip -o /tmp/upgrade-agent-windows-arm64.zip -d /tmp
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-linux-arm64.tar.gz \
#   "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-arm64.tar.gz"
# tar -xzf /tmp/upgrade-agent-linux-arm64.tar.gz -C /tmp
# wget -q -c --no-check-certificate -O /tmp/upgrade-agent-legacy.tar.gz \
#   "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64-legacy.tar.gz"
# tar -xzf /tmp/upgrade-agent-legacy.tar.gz -C /tmp

# 2. 替换二进制(mv -f 原子覆盖)
mkdir -p /opt/upgrade-mgr/dist
mv -f /tmp/upgrade-mgr-linux-amd64 /usr/local/bin/upgrade-mgr
chmod +x /usr/local/bin/upgrade-mgr
# 文件名必须保持 upgrade-agent-<os>-<arch>[-legacy][.exe]，控制台按此名下发
mv -f /tmp/upgrade-agent-linux-amd64      /opt/upgrade-mgr/dist/upgrade-agent-linux-amd64
mv -f /tmp/upgrade-agent-windows-amd64.exe /opt/upgrade-mgr/dist/upgrade-agent-windows-amd64.exe
# 可选（与上面 1.2 的下载项对应）
# mv -f /tmp/upgrade-agent-windows-arm64.exe /opt/upgrade-mgr/dist/upgrade-agent-windows-arm64.exe
# mv -f /tmp/upgrade-agent-linux-arm64       /opt/upgrade-mgr/dist/upgrade-agent-linux-arm64
# mv -f /tmp/upgrade-agent-linux-amd64-legacy /opt/upgrade-mgr/dist/upgrade-agent-linux-amd64-legacy

# 2.1 确认 dist 目录（控制台的 Agent 下载目录，默认 server.agent_binary_dir=./dist，
#     systemd 单元里 WorkingDirectory=/opt/upgrade-mgr，所以实际是 /opt/upgrade-mgr/dist）
ls -l /opt/upgrade-mgr/dist/
# 应至少包含：
#   upgrade-agent-linux-amd64             Linux x86_64
#   upgrade-agent-windows-amd64.exe       Windows Server 2008 R2 ~ Windows 11（x64）
# 需要接入 Windows on ARM / Linux arm64 / CentOS 6 时，再补充对应文件。
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

database:
  dsn: "postgresql://user:pass@localhost:5432/upgrade_mgr?sslmode=disable"

auth:
  provider: builtin
  admin_username: admin
  admin_password: "请替换为强密码"

artifact:
  dir: /data/artifacts
  storage_type: local

agent:
  poll_hold: 20
  require_enroll_token: true
EOF

# 5. 下载 systemd 单元到 /tmp 再替换
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.service \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/systemd/upgrade-mgr.service"
mv -f /tmp/upgrade-mgr.service /etc/systemd/system/upgrade-mgr.service

# 6. 启动服务
systemctl daemon-reload
systemctl enable --now upgrade-mgr

# 7. 查看状态
systemctl status upgrade-mgr
# 访问 http://your-server-ip:8090
```

> `unzip` 不可用时先安装：`yum install -y unzip` / `apt install -y unzip`。
> 也可以只下载当前要接入的平台：只接 Linux 主机就不必放 Windows 的 `.exe`，反之亦然。
> Docker 镜像已内置 linux/amd64 的 Agent，Windows Agent 需按下文单独放入 `dist` 目录。

#### Windows 目标机接入（同属 systemd 部署方式）

1. 控制台「主机」页 →「接入主机」→ 操作系统选 **Windows** → 选架构（绝大多数为 `amd64`）→
   生成接入命令；
2. 把命令粘贴到目标机的**管理员 PowerShell** 中执行（Server 2008 R2 自带的 PowerShell 2.0 即可）：

```powershell
# 控制台生成的就是下面这种形式（令牌一次性、15 分钟有效，每台机器生成一次；
# 换行在 PowerShell 里可直接分行粘贴，也可用分号连成一行）
Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path $env:TEMP 'upgrade-agent.exe')
(New-Object System.Net.WebClient).DownloadFile('http://your-server-ip:8090/api/agent/binary/windows-amd64', (Join-Path $env:TEMP 'upgrade-agent.exe'))
Start-Process -FilePath (Join-Path $env:TEMP 'upgrade-agent.exe') -ArgumentList 'install','--server','http://your-server-ip:8090','--enroll-token','<token>' -Wait -NoNewWindow
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

### 方式二：Docker（推荐用于快速体验）

```bash
# 1. 创建工作目录
mkdir -p upgrade-mgr && cd upgrade-mgr

# 2. 下载 docker-compose.yml
wget -q -c --no-check-certificate -O /tmp/docker-compose.yml \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/docker/docker-compose.yml"
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
wget -q -c --no-check-certificate -O /tmp/upgrade-agent-windows-amd64.zip \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.zip"
unzip -o /tmp/upgrade-agent-windows-amd64.zip -d dist

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
**不需要** Windows legacy 变体。

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
Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path $env:TEMP 'upgrade-agent.exe')
(New-Object System.Net.WebClient).DownloadFile('https://<server>/api/agent/binary/windows-amd64', (Join-Path $env:TEMP 'upgrade-agent.exe'))
Start-Process -FilePath (Join-Path $env:TEMP 'upgrade-agent.exe') -ArgumentList 'install','--server','https://<server>','--enroll-token','<token>' -Wait -NoNewWindow
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

⚠️ 尚未在真实 Server 2008 R2 主机上验证，上线前请先实测 `install` / `run` / `watchdog` /
组件升级与自升级链路。

---

## CI/CD 与镜像发布

仓库只有一条流水线 `.github/workflows/ci-cd.yml`（原 `build.yml` 与 `publish.yml` 已合并，避免同一事件重复编译）：

- **Pull Request**：跑 PostgreSQL 集成测试、`go vet`、前端语法检查，并编译三种架构做校验，不发布任何产物。
- **push main / 手动触发**：测试通过后编译多架构二进制与 Legacy Agent，打包发布到 GitHub Release 的 `latest`，并构建推送镜像。
- **push tag `v*`**：复用同一套编译产物，创建对应版本号的 Release；不会移动 `latest` 镜像标签。

推送代码到 `main` 或在 Actions 页面手动触发 `.github/workflows/ci-cd.yml` 即构建，二进制与镜像统一发布到 `latest`：

| 产物 | 位置 |
|---|---|
| 控制台二进制 | `upgrade-mgr-linux-{amd64,arm64,arm}.tar.gz` |
| Agent 二进制 | `upgrade-agent-linux-{amd64,arm64,arm}.tar.gz` |
| Agent Legacy（CentOS 6） | `upgrade-agent-linux-{amd64,386,arm64}-legacy.tar.gz` |
| Agent Windows | `upgrade-agent-windows-{amd64,arm64}.zip` |
| 华为云 SWR 镜像 | `swr.cn-east-3.myhuaweicloud.com/iflyelf/upgrade-mgr:latest` |
| DockerHub 镜像（可选） | `iflyelf/upgrade-mgr:latest` |

二进制发布到 GitHub Release 的 `latest` 标签，下载统一走 `https://down.xiaonuo.live?url=` 加速。

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
> 配置文件中的 `${VAR}` **不会被自动展开**，敏感项请留空并通过环境变量注入。常用变量：`SERVER_EXTERNAL_URL`、`TRUST_PROXY`、`COOKIE_SECURE`、`ADMIN_PASSWORD`、`SESSION_SECRET`、`CONSUL_ENABLED`、`CONSUL_SYNC_INTERVAL`、`CONSUL_TOKEN`、`CONSUL_CLEANUP_MISSING`、`CASDOOR_CLIENT_SECRET`、`ARTIFACT_STORAGE_TYPE`、`ARTIFACT_S3_*`、`ARTIFACT_OSS_*`（可写入 systemd 的 `EnvironmentFile` 或 K8s Secret）。

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

# 方式2: 手动安装（下载均通过 down.xiaonuo.live 加速）
# 按需把 amd64 换成 arm64 / arm；CentOS 6 等老内核请使用 *-legacy 包
wget -q -c --no-check-certificate -O /tmp/upgrade-agent.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64.tar.gz"
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
(New-Object System.Net.WebClient).DownloadFile('https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-windows-amd64.zip', "$dir\agent.zip")

# PowerShell 5+：Expand-Archive -Path "$dir\agent.zip" -DestinationPath $dir -Force
# PowerShell 2.0（2008 R2）没有 Expand-Archive，用 Shell.Application 解压：
$shell = New-Object -ComObject Shell.Application
$shell.Namespace($dir).CopyHere($shell.Namespace("$dir\agent.zip").Items())

& "$dir\upgrade-agent-windows-amd64.exe" install --server https://upgrade.example.com --enroll-token <token>
Get-Service upgrade-agent
```

> 更简单的方式是在控制台「接入主机」选择 Windows 后复制一键命令（走
> `/api/agent/binary/windows-amd64`，无需手工解压）。
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

> `proxy.xiaonuo.live` 与上文的 `down.xiaonuo.live` 都是下载代理，两个地址均可
> 代理 GitHub 文件；脚本示例统一使用 `proxy.xiaonuo.live`。

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
GET /api/agent/enroll-token           # 生成注册令牌（管理员）
```

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
4. **注册令牌**：生产环境开启 `require_enroll_token`，防止未授权 Agent 注册
5. **最小权限**：数据库用户仅授予必要权限，避免使用 superuser
6. **审计日志**：定期检查 `audit_logs` 表，监控敏感操作
7. **备份恢复**：每日备份数据库，定期演练恢复流程

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
| 连接池 | `database.max_open_conns` 至少 4（默认 50；小于 4 时自动调整，建议 10+） | 编排器、维护、Consul 同步各自可能持锁并同时查询 |

推荐配置：

```yaml
server:
  session_secret: "所有副本一致的 32+ 字节随机串"  # 也可留空，由 DB 持久化共享
artifact:
  storage_type: s3          # 或 oss；尽量不用 local
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

**推荐路径：设置页修改 → 滚动重启。** 设置页会把值写入数据库并只立即更新当前副本内存；滚动重启后所有副本在启动时重新加载 `app_settings`，最终一致。

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
- 确认制品后端没有出现“降级为 local”告警；多副本使用本地盘会导致上传/下载命中不同副本时 404。
- `GET /ready` 返回 200 且 `{"status":"ready"}`；`GET /health` 只表示进程存活。
- 多副本下任意副本被终止，其他副本下一轮会自动抢到编排锁，任务继续推进。
- 手动 Consul 同步与定时同步并发时，只有一个会执行；另一个返回 409 或直接跳过。

### 副本间不一致的点（可接受但需知悉）

- **设置页不是跨副本热更新**：修改只写入当前副本内存和数据库，其他副本不会重新加载；多副本请改完后滚动重启全部副本。
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

3. **配置热更新**：设置页只对当前副本即时生效（同时写入 DB）；多副本需在修改后滚动重启全部副本。`app_settings` 优先级高于 YAML/环境变量，切回配置文件前需清理对应 DB 行。部分配置（如认证方式、制品目录）本来就需要重启。

4. **制品下载认证**：当前制品下载端点需 Agent Bearer 认证，匿名下载已禁用。

5. **CentOS 6 兼容性**：legacy Agent 已用 Go 1.23 编译并通过单测，但尚未在真实 CentOS 6 主机实测；Go 1.23 已停止安全维护。

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
- 新增 `build/agent-windows.mod`（Go 1.20 + `x/sys v0.18.0`）、Makefile `agent-windows` / `test-agent-windows`、CI Windows 作业与 `.zip` 发布产物。

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
