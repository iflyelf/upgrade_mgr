#!/bin/bash
# CentOS 6 真机验证脚本
#
# 用法: ./centos6_verify.sh <agent_url> [server_url]
# 示例: ./centos6_verify.sh http://upgrade-mgr.example.com/api/agent/binary/linux-amd64-legacy

set -e

if [ "$#" -lt 1 ]; then
    echo "用法: $0 <agent_url> [server_url]"
    echo "示例: $0 http://127.0.0.1:8090/api/agent/binary/linux-amd64-legacy http://127.0.0.1:8090"
    exit 1
fi

AGENT_URL="$1"
SERVER_URL="${2:-http://upgrade-mgr.example.com}"

echo "=== CentOS 6 兼容性验证 ==="

# 检查系统版本和内核
echo ""
echo "=== 系统信息 ==="
if [ -f /etc/redhat-release ]; then
    echo "发行版: $(cat /etc/redhat-release)"
else
    echo "⚠️ 非 CentOS/RHEL 系统"
fi
echo "内核版本: $(uname -r)"
if [ "$(uname -r | cut -d. -f1-2)" != "2.6" ]; then
    echo "⚠️ 不是 2.6.x 内核，本脚本用于 CentOS 6 验证"
fi

# 检查 glibc 版本
echo ""
echo "=== glibc 版本 ==="
ldd --version | head -1

# 检查 init 系统
echo ""
echo "=== Init 系统 ==="
if command -v systemctl &> /dev/null; then
    echo "systemd"
elif command -v initctl &> /dev/null && [ -d /etc/init ]; then
    echo "upstart (initctl 版本: $(initctl version 2>&1 | head -1))"
elif [ -d /etc/init.d ]; then
    echo "sysv"
else
    echo "unknown"
fi

# 创建临时目录（防 TOCTOU 符号链接攻击）
TMPDIR="$(mktemp -d /tmp/upgrade-agent-verify.XXXXXX)"
trap "rm -rf '$TMPDIR'" EXIT

# 下载 Agent
echo ""
echo "=== 下载 Agent ==="
echo "URL: $AGENT_URL"
echo "临时目录: $TMPDIR"

if ! wget --tries=3 --timeout=30 -O "$TMPDIR/upgrade-agent" "$AGENT_URL"; then
    echo "❌ 下载失败"
    exit 1
fi

chmod +x "$TMPDIR/upgrade-agent"
echo "✅ 下载成功"

# 检查二进制依赖
echo ""
echo "=== 二进制依赖检查 ==="
if ldd "$TMPDIR/upgrade-agent" 2>&1 | grep -q "not a dynamic executable"; then
    echo "✅ 静态链接，无外部依赖"
elif ldd "$TMPDIR/upgrade-agent" 2>&1 | grep -q "not found"; then
    echo "❌ 发现缺失依赖:"
    ldd "$TMPDIR/upgrade-agent" | grep "not found"
    exit 1
else
    echo "动态链接库:"
    ldd "$TMPDIR/upgrade-agent"
fi

# 检查 Go 版本
echo ""
echo "=== Go 版本信息 ==="
go version "$TMPDIR/upgrade-agent" || echo "(无法读取 Go 版本)"

# 测试启动
echo ""
echo "=== 版本信息 ==="
if ! "$TMPDIR/upgrade-agent" version; then
    echo "❌ 启动失败"
    exit 1
fi
echo "✅ 启动成功"

# 检查安装能力
echo ""
echo "=== 安装测试 ==="
echo "服务端: $SERVER_URL"

if [ "$EUID" -ne 0 ]; then
    echo "⚠️ 非 root 用户，跳过安装测试"
    echo ""
    echo "=== ✅ CentOS 6 基本验证通过（未测试安装） ==="
    exit 0
fi

echo "以 root 身份运行安装测试..."
INSTALL_LOG="$TMPDIR/install.log"
if "$TMPDIR/upgrade-agent" install --server "$SERVER_URL" --cron=false 2>&1 | tee "$INSTALL_LOG"; then
    echo "✅ 安装成功"
    
    # 检查服务状态
    if command -v systemctl &> /dev/null; then
        systemctl status upgrade-agent || true
    elif command -v status &> /dev/null; then
        status upgrade-agent || true
    elif command -v service &> /dev/null; then
        service upgrade-agent status || true
    fi
    
    # 清理
    echo ""
    echo "=== 清理测试环境 ==="
    /usr/local/bin/upgrade-agent uninstall || true
else
    echo "❌ 安装失败，日志:"
    cat "$INSTALL_LOG"
    exit 1
fi

echo ""
echo "=== ✅ CentOS 6 完整验证通过 ==="
