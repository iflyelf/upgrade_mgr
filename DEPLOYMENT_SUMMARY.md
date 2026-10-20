# upgrade_mgr 项目 CI/CD 部署完成总结

## ✅ 已完成的工作

### 1. 创建了 Dockerfile（多阶段构建）
- **构建阶段**: 使用 `iflyelf/ubuntu:latest`（预装 Go 工具链）
- **运行阶段**: 使用 `iflyelf/ubuntu:lite`（轻量级运行时）
- 支持多架构构建（linux/amd64, linux/arm64）
- 包含健康检查和安全加固

**文件位置**: `/xiaonuo/workspace/code/upgrade_mgr/Dockerfile`

### 2. 创建了 GitHub Actions 工作流

#### publish.yml - 构建并发布到 latest
- **触发条件**: 
  - 推送到 main 分支（路径过滤）
  - 手动触发（workflow_dispatch）
  - watch 事件（star 项目时）
- **并发控制**: 同一分支仅保留最新构建，自动取消旧构建
- **二进制编译**: 
  - 控制台: amd64, arm64, arm
  - Agent: amd64, arm64, arm
  - Agent Legacy (Go 1.23): amd64, 386, arm64（CentOS 6 兼容）
  - 打包为 tar.gz 并生成 SHA256 校验和
  - 发布到 GitHub Release (latest tag)
- **Docker 镜像**: 
  - 多架构构建（linux/amd64, linux/arm64）
  - 推送到 DockerHub: `iflyelf/upgrade-mgr:latest`
  - 推送到华为云 SWR: `swr.cn-east-3.myhuaweicloud.com/danxiaonuo/upgrade-mgr:latest`
- **自动清理**: 保留最近 1 天内的 workflow 运行记录，最少保留 3 次

**文件位置**: `/xiaonuo/workspace/code/upgrade_mgr/.github/workflows/publish.yml`

### 3. 部署配置文件

#### systemd 服务单元
- 标准 systemd 服务配置
- 包含安全加固（NoNewPrivileges, PrivateTmp）
- 支持环境变量文件
- 自动重启配置

**文件位置**: `/xiaonuo/workspace/code/upgrade_mgr/deploy/systemd/upgrade-mgr.service`

#### docker-compose.yml
- 包含 upgrade-mgr 和 PostgreSQL 服务
- 配置卷挂载（config, artifacts, logs）
- 网络隔离
- 健康检查

**文件位置**: `/xiaonuo/workspace/code/upgrade_mgr/deploy/docker/docker-compose.yml`

### 4. 更新了 README.md
在"快速开始"后添加了完整的部署文档：
- **方式一**: systemd 部署（推荐用于生产）
- **方式二**: Docker 部署（推荐用于快速体验）
- 包含完整的安装命令和配置示例
- 镜像仓库信息

### 5. 代码已推送到 GitHub
- 仓库地址: https://github.com/iflyelf/upgrade_mgr
- 仓库类型: 公开仓库
- 默认分支: main
- 提交历史: 
  - `3ce3798` - 初始提交（完整项目代码 + CI/CD）
  - `f250be8` - 修正华为云 SWR 登录配置

## 📋 需要手动完成的步骤

### 1. 设置 GitHub Secrets

访问 https://github.com/iflyelf/upgrade_mgr/settings/secrets/actions/new

依次添加以下 4 个 secrets：

| Secret Name | 说明 |
|-------------|------|
| `DOCKER_USERNAME` | DockerHub 用户名 |
| `DOCKER_PASSWORD` | DockerHub 密码/令牌 |
| `HUAWEI_ACCESS_KEY` | 华为云 Access Key Id |
| `HUAWEI_SWR_PASSWORD` | 华为云 SWR 登录密码 |

> 具体凭据值见本地涉密文档，**不要提交到仓库**。

### 2. 触发首次构建

设置完 Secrets 后，有两种方式触发构建：

#### 方式一：推送代码（已自动触发）
上次推送已触发构建，检查构建状态：
https://github.com/iflyelf/upgrade_mgr/actions

#### 方式二：手动触发
访问 https://github.com/iflyelf/upgrade_mgr/actions/workflows/publish.yml
点击右上角 "Run workflow" 按钮

### 3. 验证构建结果

构建成功后，检查以下位置：

#### 二进制文件
https://github.com/iflyelf/upgrade_mgr/releases/tag/latest
- upgrade-mgr-linux-{amd64,arm64,arm}.tar.gz
- upgrade-agent-linux-{amd64,arm64,arm}.tar.gz
- upgrade-agent-linux-{amd64,386,arm64}-legacy.tar.gz
- checksums.txt

#### Docker 镜像
```bash
# 拉取 DockerHub 镜像
docker pull iflyelf/upgrade-mgr:latest

# 拉取华为云 SWR 镜像
docker pull swr.cn-east-3.myhuaweicloud.com/danxiaonuo/upgrade-mgr:latest
```

## 🎯 部署指南

### 快速部署（systemd）

```bash
# 1. 下载控制台二进制
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.tar.gz \
  "https://down.xiaonuo.live?url=https://github.com/iflyelf/upgrade_mgr/releases/latest/download/upgrade-mgr-linux-amd64.tar.gz"
tar -xzf /tmp/upgrade-mgr.tar.gz -C /tmp
mv -f /tmp/upgrade-mgr-linux-amd64 /usr/local/bin/upgrade-mgr
chmod +x /usr/local/bin/upgrade-mgr

# 2. 准备配置和数据库
mkdir -p /etc/upgrade-mgr /data/artifacts
createdb upgrade_mgr

# 3. 创建配置文件（参考 deploy/config/config.yaml）
vi /etc/upgrade-mgr/config.yaml

# 4. 下载并安装 systemd 服务
wget -q -c --no-check-certificate -O /tmp/upgrade-mgr.service \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/systemd/upgrade-mgr.service"
mv -f /tmp/upgrade-mgr.service /etc/systemd/system/upgrade-mgr.service

# 5. 启动服务
systemctl daemon-reload
systemctl enable --now upgrade-mgr
systemctl status upgrade-mgr
```

### Docker 部署

```bash
# 使用 docker-compose
mkdir -p upgrade-mgr/config upgrade-mgr/data upgrade-mgr/logs
cd upgrade-mgr

# 下载 docker-compose.yml
wget -q -c --no-check-certificate -O docker-compose.yml \
  "https://down.xiaonuo.live?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/deploy/docker/docker-compose.yml"

# 创建配置文件
vi config/config.yaml

# 启动
docker compose up -d
docker compose logs -f
```

## 🔄 后续维护

### 更新二进制/镜像
每次推送到 main 分支，CI/CD 会自动：
1. 编译多架构二进制
2. 更新 latest Release
3. 构建并推送 Docker 镜像到两个仓库

### 手动触发构建
在不修改代码的情况下重新构建（例如网络错误重试）：
```bash
curl -X POST \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/iflyelf/upgrade_mgr/actions/workflows/publish.yml/dispatches" \
  -d '{"ref":"main","inputs":{"build":"build upgrade_mgr"}}'
```

### 查看构建状态
```bash
curl -s "https://api.github.com/repos/iflyelf/upgrade_mgr/actions/runs?per_page=1" \
  | jq -r '.workflow_runs[0] | "run #\(.run_number) \(.status) \(.conclusion // "-") \(.display_title)"'
```

## 📚 参考文档

- 项目 README: https://github.com/iflyelf/upgrade_mgr/blob/main/README.md
- GitHub Actions: https://github.com/iflyelf/upgrade_mgr/actions
- Releases: https://github.com/iflyelf/upgrade_mgr/releases
- Docker Hub: https://hub.docker.com/r/iflyelf/upgrade-mgr
- 华为云 SWR: https://console.huaweicloud.com/swr/?region=cn-east-3

## ✨ 特性亮点

1. **多阶段构建**: 构建镜像使用完整工具链，运行镜像轻量化
2. **多架构支持**: amd64/arm64/arm 全覆盖
3. **CentOS 6 兼容**: 提供 Go 1.23 编译的 legacy Agent
4. **双镜像仓库**: DockerHub + 华为云 SWR，国内外都能快速拉取
5. **自动化构建**: 推送即构建，无需人工干预
6. **并发控制**: 避免多个构建堆积，确保镜像及时更新
7. **完整文档**: systemd 和 Docker 两种部署方式，开箱即用

---

**项目已准备就绪！设置完 GitHub Secrets 后即可触发首次构建。**
