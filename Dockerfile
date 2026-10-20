#=================================================
# https://github.com/iflyelf/upgrade_mgr
# License: MIT
# Author: iflyelf
#=================================================

#=================================================
# 构建阶段：使用预装工具链的完整镜像
#=================================================
FROM iflyelf/ubuntu:latest AS builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /build

# 拷贝依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 拷贝源码
COPY . .

# 交叉编译控制台和 Agent（静态链接）
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /tmp/upgrade-mgr ./cmd/server && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /tmp/upgrade-agent ./cmd/agent

#=================================================
# 运行阶段：轻量级镜像，仅包含运行时依赖
#=================================================
FROM iflyelf/ubuntu:lite

LABEL maintainer="iflyelf@gmail.com"
LABEL description="Upgrade Manager - 灰度升级控制平台"
LABEL org.opencontainers.image.source="https://github.com/iflyelf/upgrade_mgr"

# 运行时依赖（根据实际需求调整）
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    curl \
    && rm -rf /var/lib/apt/lists/*

# 设置时区
ENV TZ=Asia/Shanghai
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# 创建运行用户
RUN groupadd -r upgrade && useradd -r -g upgrade -s /bin/false upgrade

# 创建必要目录
RUN mkdir -p /opt/upgrade-mgr /etc/upgrade-mgr /var/log/upgrade-mgr /data/artifacts && \
    chown -R upgrade:upgrade /opt/upgrade-mgr /etc/upgrade-mgr /var/log/upgrade-mgr /data/artifacts

# 拷贝编译产物
COPY --from=builder /tmp/upgrade-mgr /usr/local/bin/upgrade-mgr
COPY --from=builder /tmp/upgrade-agent /usr/local/bin/upgrade-agent
RUN chmod +x /usr/local/bin/upgrade-mgr /usr/local/bin/upgrade-agent

# 一键安装：Agent 二进制放入 server.agent_binary_dir（默认 ./dist）
ARG TARGETARCH=amd64
COPY --from=builder --chown=upgrade:upgrade /tmp/upgrade-agent /opt/upgrade-mgr/dist/upgrade-agent-linux-${TARGETARCH}

# 拷贝默认配置示例（可选）
COPY --chown=upgrade:upgrade deploy/config/config.yaml /etc/upgrade-mgr/config.example.yaml

WORKDIR /opt/upgrade-mgr
USER upgrade

# 暴露端口
EXPOSE 8090

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD curl -f http://localhost:8090/health || exit 1

# 启动控制台
ENTRYPOINT ["/usr/local/bin/upgrade-mgr"]
CMD ["-c", "/etc/upgrade-mgr/config.yaml"]
