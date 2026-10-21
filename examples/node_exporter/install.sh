#!/bin/bash
# node_exporter 独立安装示例（不经过升级平台）
#
# 用法：sudo bash install.sh [amd64|arm64|arm|386]
#
# 支持：
#   - systemd：CentOS/RHEL/Rocky/openEuler 7+、Ubuntu 16.04+、Debian 8+、SLES 12+
#   - SysV/upstart：CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11
#
# 二进制使用 iflyelf/node_exporter latest（静态编译，兼容 CentOS 6 等老内核），
# 不依赖目标机 Go / glibc。服务文件自动按 init 系统选择。
set -euo pipefail

ARCH="${1:-}"
if [ -z "$ARCH" ]; then
  ARCH="$(uname -m)"
fi
case "$ARCH" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  armv7l | armv7 | armv6l | armv6 | arm) ARCH=arm ;;
  i386 | i486 | i586 | i686 | 386) ARCH=386 ;;
  *)
    echo "无法识别架构: $ARCH" >&2
    echo "可选架构: amd64 / arm64 / arm / 386" >&2
    exit 1
    ;;
esac

# 发行版信息仅用于提示，不影响安装流程。
detect_os() {
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    printf '%s' "${PRETTY_NAME:-${NAME:-Linux}}"
  elif [ -r /etc/redhat-release ]; then
    head -n 1 /etc/redhat-release
  elif [ -r /etc/SuSE-release ]; then
    head -n 1 /etc/SuSE-release
  elif [ -r /etc/debian_version ]; then
    printf 'Debian %s' "$(cat /etc/debian_version)"
  elif [ -r /etc/lsb-release ]; then
    # shellcheck disable=SC1091
    . /etc/lsb-release
    printf '%s %s' "${DISTRIB_ID:-Linux}" "${DISTRIB_RELEASE:-}"
  else
    uname -s
  fi
}

OS_PRETTY="$(detect_os)"
KERNEL="$(uname -r)"

# systemd 必须同时满足「有 systemctl」和「systemd 正在运行」，避免容器误判。
USE_SYSTEMD=0
INIT_LABEL="SysV"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  USE_SYSTEMD=1
  INIT_LABEL="systemd"
elif command -v initctl >/dev/null 2>&1 && [ -d /etc/init ]; then
  INIT_LABEL="upstart（使用 /etc/init.d 脚本）"
fi

echo "系统: ${OS_PRETTY}"
echo "内核: ${KERNEL}"
echo "架构: ${ARCH}"
echo "init: ${INIT_LABEL}"
case "$ARCH" in
  arm | 386)
    echo "提示：iflyelf latest 资产通常只提供 amd64/arm64；若下载 404，请改用自编译的静态二进制。" >&2
    ;;
esac

PROXY="https://proxy.xiaonuo.live/?url="
BASE="https://github.com/iflyelf/node_exporter/releases/download/latest"
INSTALL_DIR="/opt/monitor/exporter"
UNIT_FILE="/etc/systemd/system/node_exporter.service"
INIT_FILE="/etc/init.d/node_exporter"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
TMP_BIN="/tmp/node_exporter-download.$$"
trap 'rm -f "$TMP_BIN"' EXIT

if [ "$USE_SYSTEMD" -eq 1 ]; then
  if [ ! -f "${SRC_DIR}/node_exporter.service" ]; then
    echo "缺少 ${SRC_DIR}/node_exporter.service，请从仓库 examples/node_exporter 目录完整执行。" >&2
    exit 1
  fi
else
  if [ ! -f "${SRC_DIR}/node_exporter.init" ]; then
    echo "缺少 ${SRC_DIR}/node_exporter.init，请从仓库 examples/node_exporter 目录完整执行。" >&2
    exit 1
  fi
fi

download() {
  src="$1"
  dst="$2"
  if command -v curl >/dev/null 2>&1; then
    if curl -k -fL --connect-timeout 15 --retry 2 -o "$dst" "$src"; then
      return 0
    fi
  fi
  if command -v wget >/dev/null 2>&1; then
    if wget --no-check-certificate -q -c -O "$dst" "$src"; then
      return 0
    fi
  fi
  echo "下载失败: $src" >&2
  echo "请确认目标机能访问 proxy.xiaonuo.live，并已安装 curl 或 wget：" >&2
  echo "  RHEL/CentOS/Rocky/openEuler: yum install -y curl   # 或 dnf install -y curl" >&2
  echo "  Debian/Ubuntu: apt-get update && apt-get install -y curl" >&2
  echo "  SUSE: zypper install -y curl" >&2
  return 1
}

echo "下载 node_exporter-linux-${ARCH}（latest，静态编译）..."
download "${PROXY}${BASE}/node_exporter-linux-${ARCH}" "$TMP_BIN"

install -d -m 0755 "${INSTALL_DIR}/metrics"
install -m 0755 "$TMP_BIN" "${INSTALL_DIR}/node_exporter"

if [ "$USE_SYSTEMD" -eq 1 ]; then
  install -m 0644 "${SRC_DIR}/node_exporter.service" "$UNIT_FILE"
  systemctl daemon-reload
  systemctl enable node_exporter
  systemctl restart node_exporter
else
  install -m 0755 "${SRC_DIR}/node_exporter.init" "$INIT_FILE"

  REGISTERED=0
  if command -v chkconfig >/dev/null 2>&1; then
    chkconfig --add node_exporter >/dev/null 2>&1 || true
    if chkconfig node_exporter on >/dev/null 2>&1; then
      REGISTERED=1
    fi
  fi
  if [ "$REGISTERED" -eq 0 ] && command -v update-rc.d >/dev/null 2>&1; then
    if update-rc.d node_exporter defaults >/dev/null 2>&1; then
      REGISTERED=1
    fi
  fi
  if [ "$REGISTERED" -eq 0 ] && command -v insserv >/dev/null 2>&1; then
    if insserv node_exporter >/dev/null 2>&1; then
      REGISTERED=1
    fi
  fi
  if [ "$REGISTERED" -eq 0 ]; then
    echo "警告：未找到可用的 chkconfig/update-rc.d/insserv，服务不会开机自启。" >&2
  fi

  if command -v service >/dev/null 2>&1; then
    service node_exporter restart
  else
    "$INIT_FILE" restart
  fi
fi

sleep 1
if [ "$USE_SYSTEMD" -eq 1 ]; then
  systemctl --no-pager --lines=5 status node_exporter || true
else
  "$INIT_FILE" status || true
fi

echo
if command -v curl >/dev/null 2>&1; then
  curl -fsS --max-time 5 "http://127.0.0.1:16666/metrics" | head -n 3 || true
elif command -v wget >/dev/null 2>&1; then
  wget -qO- --timeout=5 "http://127.0.0.1:16666/metrics" | head -n 3 || true
else
  echo "未安装 curl/wget，跳过 metrics 自动验证。"
fi

echo
echo "完成：系统=${OS_PRETTY} 内核=${KERNEL} 架构=${ARCH} init=${INIT_LABEL}"
if [ "$USE_SYSTEMD" -eq 1 ]; then
  echo "服务文件: ${UNIT_FILE}"
  echo "查看状态: systemctl status node_exporter"
  echo "查看日志: journalctl -u node_exporter"
else
  echo "服务文件: ${INIT_FILE}"
  echo "查看状态: service node_exporter status"
  echo "查看日志: tail -f /var/log/node_exporter.log"
fi
echo "指标验证: wget -qO- http://127.0.0.1:16666/metrics | head"
echo "远程抓取请放行 16666/tcp（firewalld/ufw/SuSEfirewall2）。"
