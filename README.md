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
- **Agent 自升级**：收到 self_update 指令 → 下载新版本 → 备份 → 替换 → exit(75) 触发重启；watchdog 检测连续崩溃（≥3次）自动回滚旧版本
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
> 按需把 `amd64` 换成 `arm64` 或 `arm`。CentOS 6 请使用 `*-legacy` 版本。

### 方式一：systemd（推荐，直接跑在宿主机）

```bash
# 1. 下载控制台二进制到 /tmp 并解压
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-mgr-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-mgr.tar.gz -C /tmp

# 1.1 下载 Agent 二进制（供控制台生成一键安装命令时下载）
wget -q -c --no-check-certificate -O /tmp/upgrade-agent.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-agent-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-agent.tar.gz -C /tmp

# 2. 替换二进制(mv -f 原子覆盖)
mkdir -p /opt/upgrade-mgr/dist
mv -f /tmp/upgrade-mgr-linux-amd64 /usr/local/bin/upgrade-mgr
chmod +x /usr/local/bin/upgrade-mgr
mv -f /tmp/upgrade-agent-linux-amd64 /opt/upgrade-mgr/dist/upgrade-agent-linux-amd64

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

# 4. 启动
docker compose up -d

# 5. 查看日志
docker compose logs -f
```

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
make test           # 单测（集成测试需 DATABASE_URL / CONSUL_ADDRESS）
```

### CentOS 6 / 老内核 Agent

只做静态编译（`CGO_ENABLED=0`）不够。静态编译只去掉了对 glibc 的依赖，Go 运行时本身对内核版本仍有要求：Go 1.24 起官方最低支持 Linux 3.2，CentOS 6 的内核是 2.6.32，不在支持范围内。

Agent 只依赖标准库，所以用独立 modfile `build/agent-legacy.mod`（`go 1.23`）配合 `GOTOOLCHAIN=go1.23.12` 单独编译，主 `go.mod` 不受影响。Go 1.23 是官方最后一个支持内核 2.6.32 的版本。

- 产物：`dist/upgrade-agent-linux-{amd64,386,arm64}-legacy`，均为静态链接
- CI：`agent-legacy` job 用 Go 1.23 运行测试并构建
- ⚠️ Go 1.23 已停止安全维护，legacy 产物只建议用于确实无法升级内核的主机
- ⚠️ 还没有在真实 CentOS 6 主机上跑过，上线前请先实测 `install` / `run` / `watchdog`
- CentOS 6 没有 systemd，也没有 `/etc/os-release`。服务管理走 upstart（`initctl`，缺失时回退 SysV `service`），系统识别回退到 `/etc/centos-release` / `/etc/redhat-release`

---

## CI/CD 与镜像发布

推送代码到 `main` 或在 Actions 页面手动触发 `.github/workflows/publish.yml` 即构建，二进制与镜像统一发布到 `latest`：

| 产物 | 位置 |
|---|---|
| 控制台二进制 | `upgrade-mgr-linux-{amd64,arm64,arm}.tar.gz` |
| Agent 二进制 | `upgrade-agent-linux-{amd64,arm64,arm}.tar.gz` |
| Agent Legacy（CentOS 6） | `upgrade-agent-linux-{amd64,386,arm64}-legacy.tar.gz` |
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
  allow_exec_mode: false      # 使用 script/exec/replace_exec 模式时改为 true（安全开关）

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

> **一键安装说明**：控制台生成的安装命令从 `server.agent_binary_dir`（默认 `./dist`）下载
> `upgrade-agent-linux-<arch>`。systemd 部署请按上面步骤把 Agent 二进制放到
> `/opt/upgrade-mgr/dist/`（服务的 `WorkingDirectory`），Docker 镜像已内置对应架构的 Agent。
> 若自定义目录，请设置环境变量 `AGENT_BINARY_DIR`。

> **注册状态说明**：`install` 会在启动服务前把 `--server` / `--enroll-token` 写入
> `/var/lib/upgrade-agent/state.json`（0600），因此 systemd/upstart 启动 `run` 时无需再带令牌。
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

### 5. node_exporter 安装示例（可选，推荐脚本安装）

控制台「组件」页点「填入示例：node_exporter（脚本安装）」会自动填好组件；
`examples/node_exporter/install.sh` 是**单文件自包含**安装脚本，已内嵌
systemd unit 与 SysV init，上传后不需要同目录的其他文件。

#### 5.1 先打开脚本执行开关

`script` 模式会下载并执行上传的脚本，属于任意命令执行，默认关闭。管理员到
「设置 → Agent」把 `agent.allow_exec_mode` 设为 `true` 并保存。单实例保存后即时生效；
多副本环境需让各副本重新加载设置（滚动重启或刷新）。

> 这是安全开关，不要在生产环境长期无条件开放；只给可信管理员上传/创建任务的权限。

#### 5.2 组件：填入脚本安装示例

点「新建组件」→「填入示例：node_exporter（脚本安装）」→ 保存。自动填好的关键字段：

| 字段 | 值 |
| --- | --- |
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

#### 5.3 制品（可选）：上传安装脚本

脚本示例默认使用 5.2 中真实可用的 `script_url`，通常不需要本节。如果不想依赖
外链，先删除组件高级配置 JSON 里的 `script_url`，再进入「制品」页：

1. 组件选 `node_exporter`。
2. 版本填 `latest`（创建任务时必须选同一个版本）。
3. 架构选 `all（通用脚本）`。脚本会在目标机执行 `uname -m` 自动识别架构。
4. 上传仓库里的 `examples/node_exporter/install.sh`。
5. `os_family` 留空即可。

> 不需要另外上传 node_exporter 二进制。脚本会自己从
> `proxy.xiaonuo.live` 代理下载 iflyelf/node_exporter latest 静态编译二进制。

此时 Agent 从制品库下载脚本，不再访问 `script_url`；任务版本需要和制品版本一致。

#### 5.4 任务：安装到目标机

1. 「任务」页选组件 `node_exporter`、版本 `latest`。
2. 填 IP 段，例如 `10.1.255.24-26` 或 `10.1.255.0/24`。
3. 先点「预览」确认命中主机和分批，再「创建任务」→「启动」。
4. 想先装一台：策略选「金丝雀」、首批 1 台、取消「自动推进」；首批成功后点「推进」。

Agent 执行顺序：下载安装脚本 → 跳过平台备份 → `pre_install` → 停旧服务
（存在时）→ 用 Shell 执行脚本（优先 bash，缺失时 sh）→ 检查/启动服务 → `post_install`
（命令数组；如配置了 `post_install_script` 则随后执行）→ 健康检查（如配置了 `health_cmd`）
→ 版本确认（如配置了 `version_cmd`）。脚本输出会记录在任务日志里。

#### 5.5 脚本做了什么

5.2 的 `script_url` 直接指向本仓库 `main` 分支的
`examples/node_exporter/install.sh`，所以不需要先上传制品。`install.sh` 会：

- 按 `uname -m` 识别 `amd64` / `arm64` / `arm` / `386`（也可用
  `NODE_EXPORTER_ARCH` 或第一个参数覆盖）。
- 从 `https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/node_exporter/releases/download/latest/...`
  下载静态二进制，安装到 `/opt/monitor/exporter/node_exporter`。
- 创建 metrics 目录 `/opt/monitor/exporter/metrics`。
- 检测目标机 init：
  - systemd：写 `/etc/systemd/system/node_exporter.service`，
    `daemon-reload`、`enable`、`restart`。
  - SysV/upstart：写 `/etc/init.d/node_exporter`，用
    `chkconfig` / `update-rc.d` / `insserv` 注册，再启动。
- 输出服务状态并请求 `http://127.0.0.1:16666/metrics` 做初步验证。

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
sudo bash examples/node_exporter/install.sh

# 指定架构/版本（可选）
sudo NODE_EXPORTER_ARCH=arm64 NODE_EXPORTER_VERSION=latest \
  bash examples/node_exporter/install.sh
```

#### 5.6 验证与注意事项

- systemd：`systemctl status node_exporter`，日志 `journalctl -u node_exporter`。
- SysV/upstart：`service node_exporter status` 或
  `/etc/init.d/node_exporter status`，日志 `/var/log/node_exporter.log`。
- 指标：`wget -qO- http://127.0.0.1:16666/metrics | head`。
- 远程抓取放行 `16666/tcp`：firewalld 用
  `firewall-cmd --add-port=16666/tcp --permanent && firewall-cmd --reload`；
  Ubuntu 可用 `ufw allow 16666/tcp`；SUSE 可用 YaST 防火墙。

注意：

- `script` 模式执行下载的脚本，服务端被攻破时风险等同于任意命令执行；务必依赖
  `agent.allow_exec_mode` 和 RBAC，只允许可信管理员操作。
- `script` 模式不做平台侧备份/自动回滚；需要平台备份和回滚时，改用
  「填入示例：node_exporter（二进制替换）」，按主机架构上传二进制。该示例把
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
        │              Agent 集群 (upgrade-agent)     │
        │  ┌──────┐  ┌──────┐  ┌──────┐  ┌──────┐  │
        │  │ host1│  │ host2│  │ host3│  │ host4│  │
        │  └──────┘  └──────┘  └──────┘  └──────┘  │
        │    服务A      服务A      服务B      服务C   │
        └────────────────────────────────────────────┘
```

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
GET /api/components          # 列出组件
POST /api/components         # 创建组件
GET /api/components/{id}     # 获取详情
PUT /api/components/{id}     # 更新组件
DELETE /api/components/{id}  # 删除组件
```

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
GET /api/tasks                   # 列出任务
GET /api/tasks/{id}              # 任务详情
POST /api/tasks/{id}/start       # 启动任务
POST /api/tasks/{id}/pause       # 暂停任务
POST /api/tasks/{id}/promote     # 推进下一批（手动）
POST /api/tasks/{id}/cancel      # 取消任务
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

### 主机管理

```http
GET /api/hosts?page=1&size=50           # 分页列出主机（size 最大 500，返回 total/pages）
PUT /api/hosts/{id}                     # 编辑主机业务字段（管理员）
DELETE /api/hosts/{id}                  # 删除主机（管理员）
GET /api/hosts/{ip}/upgrade-history     # 主机升级历史
POST /api/hosts/sync                    # 从 Consul 同步主机
```

**主机编辑**

- 可维护字段：`hostname`、`account`、`app_code`、`biz_group`、`department`、`vendor`、`host_type`。
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
| Agent 二进制 | 使用镜像内置的 `/opt/upgrade-mgr/dist/upgrade-agent-linux-<arch>`，或共享 `agent_binary_dir` | 一键安装命令需要从服务端目录读取二进制 |
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

### 2026-10-22 修复：Agent 指令验签路径不一致

- 服务端签名使用 `Command.SignedPath()`（`ID|类型|过期时间`），Agent 之前只校验 `cmd.ID`，导致升级指令被误判为「签名不匹配」。
- Agent 改为使用 `cmd.SignedPath()` 验签，篡改指令类型或延长过期时间同样会被拒绝。
- **旧 Agent 二进制需要更新到本次构建**；服务端升级后请同步替换各主机上的 `upgrade-agent`。

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
  agent/{watchdog,svcmgr,downloader,binswap,executor,installer,selfupdate}
  pkg/{ipx,runner,notify,signer,consul,httpx,safepath,pglock,...}
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
