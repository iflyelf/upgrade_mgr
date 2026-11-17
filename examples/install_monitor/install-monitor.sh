#!/bin/bash
# install-monitor.sh — 监控一体化安装脚本（Agent + Node Exporter）
#
# 用法：
#   curl -fsSL "http://<控制台>/api/external/binary/install-monitor.sh" | bash -s -- install -y --server "http://<控制台>"
#   curl -fsSL ".../install-monitor.sh" | bash -s -- update  --server ...
#   curl -fsSL ".../install-monitor.sh" | bash -s -- uninstall -y --server ...
#   bash install-monitor.sh status --server ...
#
# 说明：
#   - 脚本通过 /api/agent/install-monitor/token 读取控制台生成的永久自动化令牌；
#     令牌在页面「接入主机 → 生成接入脚本」中生成/轮换，脚本 URL 本身不变。
#   - 安装/更新始终带 --force，重复执行会强制重新注册主机。
#   - 仅使用控制台内网地址下载 Agent / Node Exporter，不访问 GitHub。
#   - 如果控制台未配置 server.external_url，请用 --server 指定可访问地址。
set -eu

DEFAULT_SERVER="http://console.example.com"
ACTION="install"
ASSUME_YES=0
SERVER="${UPGRADE_MGR_EXTERNAL_URL:-${UPGRADE_AGENT_SERVER_URL:-$DEFAULT_SERVER}}"
TOKEN="${INSTALL_MONITOR_TOKEN:-}"
ARCH=""
AGENT_BIN="/usr/local/bin/upgrade-agent"

usage() {
  cat <<'USAGE'
用法: install-monitor.sh [install|update|uninstall|status] [-y|--yes] [--server URL] [--token TOKEN] [--arch ARCH]

动作:
  install     安装/覆盖安装 Agent + Node Exporter（默认）
  update      更新 Agent + Node Exporter
  uninstall   卸载 Agent + Node Exporter
  status      查看安装状态

选项:
  -y, --yes       卸载时跳过确认
  --server URL    控制台地址（默认读取 UPGRADE_MGR_EXTERNAL_URL，最后回退内置地址）
  --token TOKEN   直接指定接入令牌（默认从控制台 /api/agent/install-monitor/token 获取）
  --arch ARCH     强制指定架构（amd64/arm64/arm/386；默认 uname -m）
  -h, --help      显示帮助
USAGE
}

log() { printf '[install-monitor] %s\n' "$*"; }
warn() { printf '[install-monitor] WARN: %s\n' "$*" >&2; }
die() { printf '[install-monitor] ERROR: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    install|update|uninstall|status) ACTION="$1" ;;
    -y|--yes) ASSUME_YES=1 ;;
    --server) shift; SERVER="${1:-}" ;;
    --server=*) SERVER="${1#*=}" ;;
    --token) shift; TOKEN="${1:-}" ;;
    --token=*) TOKEN="${1#*=}" ;;
    --arch) shift; ARCH="${1:-}" ;;
    --arch=*) ARCH="${1#*=}" ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数: $1" ;;
  esac
  shift
done

rotate_server() { SERVER="${SERVER%/}"; }
need_root() { [ "$(id -u)" -eq 0 ] || die "请使用 root 执行"; }

download() {
  url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -m 30 --retry 3 --connect-timeout 10 "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 30 -O - "$url"
  else
    die "需要 curl 或 wget"
  fi
}

download_to() {
  url="$1"; dest="$2"
  rm -f "$dest"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -m 600 --retry 3 --connect-timeout 10 -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 30 -O "$dest" "$url"
  else
    die "需要 curl 或 wget"
  fi
  [ -s "$dest" ] || die "下载失败或文件为空: $url"
}

detect_arch() {
  [ -n "$ARCH" ] || ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv8l|armv7l|armv7|armv6l|armv6) ARCH=arm ;;
    i386|i486|i586|i686|386) ARCH=386 ;;
    *) die "不支持的架构: $ARCH" ;;
  esac
  log "目标架构: $ARCH"
}

fetch_token() {
  [ -n "$TOKEN" ] && return 0
  log "读取控制台永久接入令牌: $SERVER/api/agent/install-monitor/token"
  raw="$(download "$SERVER/api/agent/install-monitor/token" 2>/dev/null || true)"
  TOKEN="$(printf '%s' "$raw" | tr -d '\r\n')"
  [ -n "$TOKEN" ] || die "未获取到接入令牌，请先在控制台「接入主机 → 生成接入脚本」生成"
  case "$TOKEN" in
    enr_auto_*) ;;
    *) die "控制台返回的接入令牌格式不正确，请重新生成接入脚本" ;;
  esac
}

# LEGACY-CLEANUP: 旧监控卸载。确认旧环境全部迁移后，可删除本函数及调用。
legacy_cleanup() {
  [ "$ACTION" = "install" ] || [ "$ACTION" = "update" ] || return 0
  if ! command -v script >/dev/null 2>&1; then
    warn "未找到 script 命令，跳过旧监控卸载"
    return 0
  fi
  log "尝试卸载旧监控（失败不影响新安装）..."
  printf 'Y\n' | script -q -c "bash -c 'bash <(curl -fsSL http://legacy.example.com/get/monitor/install-monitor.sh) uninstall'" /dev/null || true
}

install_agent() {
  tmp="$(mktemp /tmp/upgrade-agent.XXXXXX)"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" EXIT
  url="$SERVER/api/agent/binary/linux-$ARCH"
  log "下载 Agent: $url"
  download_to "$url" "$tmp"
  chmod +x "$tmp"
  log "安装/重新注册 Agent（--force）..."
  "$tmp" install --server "$SERVER" --enroll-token "$TOKEN" --force
  rm -f "$tmp"
  trap - EXIT
}

install_node() {
  action="$1"
  tmp="$(mktemp /tmp/node-exporter-install.XXXXXX.sh)"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" EXIT
  url="$SERVER/api/external/binary/node-exporter-install.sh"
  log "下载 Node Exporter 安装脚本: $url"
  download_to "$url" "$tmp"
  log "执行 Node Exporter ${action}..."
  # 目标机无法访问公网：把主下载地址指向一个必然失败的本地地址，
  # 让现有脚本立即进入「平台离线缓存/代下载」分支；仍可用 NODE_EXPORTER_BASE/
  # NODE_EXPORTER_PROXY 覆盖为真实内网镜像。
  if [ "$action" = "uninstall" ]; then
    UPGRADE_MGR_EXTERNAL_URL="$SERVER" \
    NODE_EXPORTER_VERSION="${NODE_EXPORTER_VERSION:-latest}" \
    NODE_EXPORTER_BASE="${NODE_EXPORTER_BASE:-127.0.0.1:9}" \
    NODE_EXPORTER_PROXY="${NODE_EXPORTER_PROXY:-http://}" \
    bash "$tmp" uninstall -y
  else
    UPGRADE_MGR_EXTERNAL_URL="$SERVER" \
    NODE_EXPORTER_VERSION="${NODE_EXPORTER_VERSION:-latest}" \
    NODE_EXPORTER_BASE="${NODE_EXPORTER_BASE:-127.0.0.1:9}" \
    NODE_EXPORTER_PROXY="${NODE_EXPORTER_PROXY:-http://}" \
    bash "$tmp" "$action"
  fi
  rm -f "$tmp"
  trap - EXIT
}

uninstall_agent() {
  if [ -x "$AGENT_BIN" ]; then
    log "卸载 Agent 服务..."
    "$AGENT_BIN" uninstall || warn "Agent uninstall 返回非 0"
  fi
  # Agent 二进制缺失时的兜底清理。
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now upgrade-agent 2>/dev/null || true
  elif command -v service >/dev/null 2>&1; then
    service upgrade-agent stop 2>/dev/null || true
  fi
  rm -f /etc/systemd/system/multi-user.target.wants/upgrade-agent.service
  rm -f "$AGENT_BIN"
  rm -rf /var/lib/upgrade-agent
  rm -f /var/log/upgrade-agent.log
  rm -f /etc/systemd/system/upgrade-agent.service
  rm -f /etc/init.d/upgrade-agent
  log "Agent 已清理"
}

status() {
  log "控制台: $SERVER"
  if [ -x "$AGENT_BIN" ]; then
    "$AGENT_BIN" status || true
  else
    log "Agent 未安装（$AGENT_BIN 不存在）"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl status node_exporter --no-pager 2>/dev/null | sed -n '1,8p' || true
  else
    service node_exporter status 2>/dev/null | sed -n '1,8p' || true
  fi
}

rotate_server
case "$ACTION" in
  install)
    need_root
    detect_arch
    legacy_cleanup
    fetch_token
    install_agent
    install_node install
    log "安装完成"
    ;;
  update)
    need_root
    detect_arch
    legacy_cleanup
    fetch_token
    install_agent
    install_node update || { warn "Node Exporter update 失败，尝试 install"; install_node install; }
    log "更新完成"
    ;;
  uninstall)
    need_root
    if [ "$ASSUME_YES" != "1" ]; then
      printf '确定卸载 upgrade-agent 与 node_exporter 吗？输入 yes 继续: '
      read -r answer
      [ "$answer" = "yes" ] || { log "已取消"; exit 0; }
    fi
    install_node uninstall || warn "Node Exporter 卸载失败"
    uninstall_agent
    log "卸载完成"
    ;;
  status)
    status
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
