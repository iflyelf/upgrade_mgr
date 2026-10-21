#!/bin/bash
# node_exporter 独立安装示例（不经过升级平台）
# 用法：sudo bash install.sh [amd64|arm64|arm|386]
# 下载走 proxy 加速，与平台示例中的 wget 命令一致。
set -euo pipefail

ARCH="${1:-}"
if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    armv7l | armv6l | arm) ARCH=arm ;;
    i386 | i686) ARCH=386 ;;
    *)
      echo "无法识别架构: $(uname -m)，请显式传入 amd64/arm64/arm/386" >&2
      exit 1
      ;;
  esac
fi

PROXY="https://proxy.xiaonuo.live/?url="
BASE="https://github.com/iflyelf/node_exporter/releases/download/latest"
INSTALL_DIR="/opt/monitor/exporter"
UNIT_FILE="/etc/systemd/system/node_exporter.service"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v systemctl >/dev/null 2>&1; then
  echo "未检测到 systemctl：本示例要求目标机使用 systemd" >&2
  exit 1
fi

echo "下载 node_exporter-linux-${ARCH} ..."
wget -q -c --no-check-certificate -O /tmp/node_exporter \
  "${PROXY}${BASE}/node_exporter-linux-${ARCH}"

install -d -m 0755 "${INSTALL_DIR}/metrics"
install -m 0755 /tmp/node_exporter "${INSTALL_DIR}/node_exporter"
install -m 0644 "${SRC_DIR}/node_exporter.service" "${UNIT_FILE}"

systemctl daemon-reload
systemctl enable node_exporter
systemctl restart node_exporter

sleep 1
systemctl --no-pager --lines=5 status node_exporter || true
echo "验证：curl -s http://127.0.0.1:16666/metrics | head"
