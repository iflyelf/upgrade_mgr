# upgrade_mgr — 升级控制平台

用于 zabbix 客户端、采集器、各类插件（均为 `CGO_ENABLED=0` 交叉编译的 Go 静态二进制）的**灰度升级控制平台**。控制台 `upgrade-mgr` + 被控端 `upgrade-agent`，支持按 IP 段灰度、维护窗口、自动回滚。

> 独立运行：**没有 consul_mgr 也能启动**；联合使用时共享同一 Consul 与 FlyIAM。数据库为独立库 `upgrade_mgr`。

---

## 特性

**P0 骨架**
- 独立启动、可选依赖优雅降级（Consul / Casdoor / Redis 不可用仅告警，不阻塞）
- 主机清单本地化（事实来源为 `hosts` 表），Consul 为可选同步来源（含业务分组 account/app_code/group…）
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

**P5 企业级增强**
- **Prometheus 指标**：`/metrics` 端点暴露关键指标（Agent 在线数/心跳延迟/任务进度/失败率/指令执行时间），支持 Grafana 监控大盘
- **多租户 RBAC**：数据库增加 tenant_id 列（hosts/components/tasks），租户隔离查询，支持 admin/user/viewer 角色权限控制
- **分布式编排器**：基于 Consul Leader 选举，多控制台实例高可用（仅 Leader 执行编排循环，自动故障转移）
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
export SESSION_SECRET="至少16字节的会话密钥"

make build
./bin/upgrade-mgr -c deploy/config/config.yaml
# 访问 http://localhost:8090
```

启用 Consul 同步（可选，或在设置页配置；`datacenter` 留空自动探测）：
```bash
export CONSUL_ENABLED=true CONSUL_ADDRESS=http://10.0.56.118:8500 \
       CONSUL_TOKEN=xxx
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

# 2. 替换二进制(mv -f 原子覆盖)
mv -f /tmp/upgrade-mgr-linux-amd64 /usr/local/bin/upgrade-mgr
chmod +x /usr/local/bin/upgrade-mgr

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
  session_secret: "请替换为至少16字节的随机密钥"

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

**生产环境配置示例**（`config.yaml`）：

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
  allow_exec_mode: false

consul:  # 可选：同步主机清单
  enabled: true
  address: consul.internal:8500
  token: ""  # 建议用环境变量 CONSUL_TOKEN 覆盖
  datacenter: ""  # 留空自动探测；多数据中心需显式指定
  leader_election: true  # 多实例高可用
```

> **配置优先级**：环境变量 > 配置文件 > 内置默认值。
> 配置文件中的 `${VAR}` **不会被自动展开**，敏感项请留空并通过 `ADMIN_PASSWORD`、`CONSUL_TOKEN`、`CASDOOR_CLIENT_SECRET` 等环境变量注入（可写入 systemd 的 `EnvironmentFile`）。

**`server.external_url` 怎么填**：

填写 **Agent/浏览器实际访问本服务的地址**，不是监听地址（`host`/`port`）。它会影响：

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
/tmp/upgrade-agent-linux-amd64 install --server https://upgrade.example.com --enroll-token <token>

# 检查服务状态
systemctl status upgrade-agent  # systemd
# 或
status upgrade-agent            # upstart
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
| **原子升级** | 备份→停服→替换→启服→健康检查，失败自动回滚 |
| **崩溃自愈** | Watchdog 心跳检测，连续崩溃自动回滚旧版本 |
| **断点续传** | 制品下载支持 HTTP Range，大文件续传节省带宽 |
| **零依赖** | 静态编译单二进制，无 Python/Ruby 依赖 |

### 数据模型

**核心表**：
- `components`：组件定义（被升级的服务/二进制）
- `artifacts`：制品仓库（版本×架构的二进制文件）
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
GET /api/hosts                         # 列出主机
GET /api/hosts/{ip}/upgrade-history    # 主机升级历史
POST /api/hosts/sync                   # 从 Consul 同步主机
```

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
3. **强密码**：修改默认管理员密码，配置强随机 `session_secret`
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

# 检查制品 ELF 合法性
file your-binary
ldd your-binary  # 应为静态链接（not a dynamic executable）
```

### Q3: 升级失败如何回滚？

- 方式1：备份自动保留在 `backup_dir`，手动回滚
- 方式2：创建回滚任务，指定旧版本

### Q4: 如何清理过期数据？

自动维护任务每日凌晨 2 点自动执行：
- 清理 7 天前过期的 pending 指令
- 清理 30 天前完成的指令
- 归档 90 天前完成的任务
- VACUUM ANALYZE 优化表

---

## 已知限制与问题

### 关键限制

1. **Go 版本与内核兼容性**：控制台和默认 Agent 用 Go 1.26 构建，要求 Linux 内核 ≥ 3.2。CentOS 6（内核 2.6.32）请使用 `make agent-legacy` 产出的 Go 1.23 版 Agent，见上文「CentOS 6 / 老内核 Agent」。

2. **多实例部署**：未启用 Consul Leader 选举时，多实例会重复编排任务。生产环境务必开启 `consul.leader_election: true`。

3. **配置热更新**：部分配置（如 `allow_exec_mode`）修改后需重启生效，页面设置不会实时生效。

4. **制品下载认证**：当前制品下载端点需 Agent Bearer 认证，匿名下载已禁用。

5. **CentOS 6 兼容性**：legacy Agent 已用 Go 1.23 编译并通过单测，但尚未在真实 CentOS 6 主机实测；Go 1.23 已停止安全维护。

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
- 批次下发改为单事务：CAS 批次状态 + FOR UPDATE SKIP LOCKED 认领主机，防重复下发
- HealthWait 实现：检查上一批次 finished_at，未到等待时间不下发
- 失败批次处理：超阈值时批次置为 failed 任务转 paused，Promote 后跳过 failed 批次继续
- Cancel 完整实现：取消未完成的 commands 和 task_hosts，更新任务状态
- 百分比灰度至少分配 1 台（`task.go` 整数除法归零修复，新增 3 个测试用例）

*并发与性能*：
- Leader 选举改为 `Run` 循环，session 失效/续约失败后自动重新竞选
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
- state.json 非原子写入，与 watchdog 竞态 → 需 tmp+fsync+rename，watchdog 用独立文件
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
  pkg/{ipx,runner,notify,signer,consul,httpx,safepath,...}
  migrate/       数据库迁移
  storage/       制品存储（local/s3/oss）
  metrics/       Prometheus 指标
  maintenance/   自动维护
web/dist/     内嵌前端
deploy/config/ 配置示例
scripts/      工具脚本
```

---

## 进度

- [x] **P0**：骨架、配置分层、独立启动、PG schema、IP 解析、Consul 可选同步、内置认证、三主题内嵌前端
- [x] **P1**：协议定义、Agent 核心包、Agent 主循环（注册/心跳/poll）、服务端 Agent 网关 API
- [x] **P2**：组件/制品完整 CRUD、任务模型、长轮询原子取件、维护租约、ack/stage/report 闭环、端到端验证
- [x] **P3**：灰度编排器、Agent 指令验签、全 Go 安装器、保活 watchdog、CI 多架构交叉编译
- [x] **P4**：Agent 自升级、Consul Meta 版本回写、enroll-token 生成 API、主机升级历史 API
- [x] **P5**：Prometheus 指标、多租户 RBAC、分布式编排器（Consul Leader 选举）、制品外部存储（S3/OSS）
- [x] **P6**：数据库自动化（自动创建/版本化迁移/自动维护/零停机更新）

---

## 许可证

MIT © iflyelf

---

## 贡献与支持

如发现问题或有改进建议，请提交 [GitHub Issues](https://github.com/iflyelf/upgrade_mgr/issues)。
