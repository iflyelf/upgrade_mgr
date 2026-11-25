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
#   - 脚本通过 /api/agent/install-monitor/token 读取永久自动化令牌；
#     控制台会按需自动创建，无需先在页面点「生成接入脚本」，URL 本身不变。
#   - 安装/更新始终带 --force，重复执行会强制重新注册主机。
#   - 仅使用控制台内网地址下载 Agent / Node Exporter，不访问 GitHub。
#   - 如果控制台未配置 server.external_url，请用 --server 指定可访问地址。
set -eu

DEFAULT_SERVER=""
ACTION="install"
ASSUME_YES=0
ACTION_SET=0
INTERACTIVE=0
SERVER="${UPGRADE_MGR_EXTERNAL_URL:-${UPGRADE_AGENT_SERVER_URL:-${UPGRADE_MGR_SERVER:-$DEFAULT_SERVER}}}"
TOKEN="${INSTALL_MONITOR_TOKEN:-}"
ARCH=""
AGENT_BIN="/usr/local/bin/upgrade-agent"

usage() {
  cat <<'USAGE'
用法: install-monitor.sh [install|update|uninstall|status] [-y|--yes] [--server URL] [--token TOKEN] [--arch ARCH]

无参数在终端运行会显示数字菜单（1 安装 / 2 更新 / 3 卸载 / 4 状态 / 0 退出）；
通过 curl ... | bash 管道执行且未指定动作时，默认执行 install。

动作:
  install     安装/覆盖安装 Agent + Node Exporter（默认）
  update      更新 Agent + Node Exporter
  uninstall   卸载 Agent + Node Exporter
  status      查看安装状态

选项:
  -y, --yes       卸载时跳过确认
  --server URL    控制台地址（安装/更新/卸载必填；也可用 UPGRADE_MGR_EXTERNAL_URL）
  --token TOKEN   直接指定接入令牌（默认从控制台自动获取，控制台会自动创建）
  --arch ARCH     强制指定架构（amd64/arm64/arm/386；默认 uname -m）
  -h, --help      显示帮助
USAGE
}

log() { printf '[install-monitor] %s\n' "$*"; }
warn() { printf '[install-monitor] WARN: %s\n' "$*" >&2; }
die() { printf '[install-monitor] ERROR: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    install|update|uninstall|status) ACTION="$1"; ACTION_SET=1 ;;
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

rotate_server() {
  SERVER="${SERVER%/}"
  if [ -n "$SERVER" ]; then
    case "$SERVER" in
      http://*|https://*) ;;
      *) SERVER="http://$SERVER" ;;
    esac
  fi
}

require_server() {
  [ -n "$SERVER" ] || die "未指定控制台地址：请使用 --server 或设置 UPGRADE_MGR_EXTERNAL_URL"
}

# choose_action 终端无参数运行时显示数字菜单；提示写 stderr，避免被日志/管道吞掉。
choose_action() {
  while :; do
    printf '\n' >&2
    printf '========================================\n' >&2
    printf '         监控一体化安装脚本\n' >&2
    printf '========================================\n' >&2
    printf '  1. 安装（Agent + Node Exporter）\n' >&2
    printf '  2. 更新\n' >&2
    printf '  3. 卸载\n' >&2
    printf '  4. 查看状态\n' >&2
    printf '  0. 退出\n' >&2
    printf '请输入数字: ' >&2
    choice=""
    read -r choice || return 1
    case "$choice" in
      1) ACTION="install"; return 0 ;;
      2) ACTION="update"; return 0 ;;
      3) ACTION="uninstall"; return 0 ;;
      4) ACTION="status"; return 0 ;;
      0|q|Q) return 1 ;;
      *) printf '无效输入，请重新输入\n' >&2 ;;
    esac
  done
}

if [ "$ACTION_SET" != "1" ]; then
  if [ -t 0 ]; then
    INTERACTIVE=1
    choose_action || exit 0
  else
    # curl ... | bash：stdin 是脚本本身，不能读菜单；默认安装。
    ACTION="install"
  fi
fi
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

# ---- 监控指标探测（带超时，避免异常端口导致脚本卡死）----
MONITOR_PORT="${MONITOR_PORT:-16666}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-3}"
PROBE_RETRIES="${PROBE_RETRIES:-10}"
PROBE_URL="http://127.0.0.1:${MONITOR_PORT}/metrics"

port_listening() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":${MONITOR_PORT}$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -q ":${MONITOR_PORT}$"
  else
    return 1
  fi
}

port_owner_hint() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltnp 2>/dev/null | grep ":${MONITOR_PORT}" | head -n 3 || true
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltnp 2>/dev/null | grep ":${MONITOR_PORT}" | head -n 3 || true
  fi
}

node_service_active() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl is-active --quiet node_exporter 2>/dev/null
  elif command -v service >/dev/null 2>&1; then
    service node_exporter status >/dev/null 2>&1
  else
    return 1
  fi
}

# check_port_conflict 安装前提示 16666 端口占用；不阻断安装，完成后由探测给出结论。
check_port_conflict() {
  port_listening || return 0
  if node_service_active; then
    log "端口 ${MONITOR_PORT} 已由现有 node_exporter 监听（更新场景正常）"
    return 0
  fi
  warn "端口 ${MONITOR_PORT} 已被其它程序占用，Node Exporter 可能无法启动"
  port_owner_hint
  warn "继续安装，完成后会自动探测 ${PROBE_URL} 并给出结果"
}

fetch_metrics() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --connect-timeout 2 --max-time "$PROBE_TIMEOUT" "$PROBE_URL" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T "$PROBE_TIMEOUT" -O - "$PROBE_URL" 2>/dev/null
  else
    return 1
  fi
}

# probe_metrics 最多重试 PROBE_RETRIES 次，每次单请求超时 PROBE_TIMEOUT 秒。
probe_metrics() {
  i=0
  while [ "$i" -lt "$PROBE_RETRIES" ]; do
    i=$((i + 1))
    if body="$(fetch_metrics)" && printf '%s' "$body" | grep -Eq '^(node_|windows_)'; then
      log "指标探测成功（第 ${i} 次）：${PROBE_URL}"
      printf '%s\n' "$body" | grep -E '^(node_|windows_)' | head -n 3 || true
      return 0
    fi
    if [ "$i" -lt "$PROBE_RETRIES" ]; then sleep 1; fi
  done
  return 1
}

# verify_or_fail 安装/更新结束后给出明确的成功或失败结论，失败时打印诊断信息。
verify_or_fail() {
  if probe_metrics; then
    log "✅ $1成功：Agent 已接入，监控指标正常"
    return 0
  fi
  warn "❌ 监控指标探测失败：${PROBE_URL}（重试 ${PROBE_RETRIES} 次，每次超时 ${PROBE_TIMEOUT}s）"
  if port_listening; then
    warn "端口 ${MONITOR_PORT} 正在监听，但 /metrics 不可用或返回内容异常，可能是其它程序占用"
  else
    warn "端口 ${MONITOR_PORT} 未监听，Node Exporter 可能未启动"
  fi
  port_owner_hint
  if command -v systemctl >/dev/null 2>&1; then
    systemctl status node_exporter --no-pager -l 2>/dev/null | sed -n '1,15p' || true
    journalctl -u node_exporter -n 20 --no-pager 2>/dev/null || true
  else
    service node_exporter status 2>/dev/null | sed -n '1,15p' || true
    tail -n 20 /var/log/node_exporter.log 2>/dev/null || true
  fi
  die "$1失败：监控指标不可用（Agent 已安装，可远程排查后重试）"
}

fetch_token() {
  require_server
  [ -n "$TOKEN" ] && return 0
  log "读取控制台永久接入令牌: $SERVER/api/agent/install-monitor/token"
  raw="$(download "$SERVER/api/agent/install-monitor/token" 2>/dev/null || true)"
  TOKEN="$(printf '%s' "$raw" | tr -d '\r\n')"
  [ -n "$TOKEN" ] || die "未获取到接入令牌：请确认控制台可访问且数据库正常（令牌会自动创建，无需人工生成）"
  case "$TOKEN" in
    enr_auto_*) ;;
    *)
      [ -z "$raw" ] || warn "控制台返回: $(printf '%s' "$raw" | head -c 300)"
      die "控制台未返回有效接入令牌：请确认控制台可访问且数据库正常（令牌会自动创建，无需人工生成）"
      ;;
  esac
}

# LEGACY-CLEANUP: 旧监控卸载。确认旧环境全部迁移后，可删除本函数及调用。
#
# 逻辑提取自旧 Qxbdown 监控脚本的
# uninstall / pre_install_purge，避免目标机访问不了 qxbdown 时无法清理。
# 原命令保留备查（不再执行，旧地址以 <legacy-monitor> 代替）：
# printf 'Y\n' | script -q -c "bash -c 'bash <(curl -fsSL http://<legacy-monitor>/get/monitor/install-monitor.sh) uninstall'" /dev/null
legacy_cleanup() {
  [ "$ACTION" = "install" ] || [ "$ACTION" = "update" ] || return 0
  log "清理旧监控（内置卸载逻辑，不依赖 qxbdown 脚本）..."

  # 旧定时更新任务。
  if command -v crontab >/dev/null 2>&1; then
    { crontab -l 2>/dev/null | grep -v "/usr/bin/check_update.sh" | crontab -; } 2>/dev/null || true
    { crontab -l 2>/dev/null | grep -v "/usr/bin/check_monitor_update.sh" | crontab -; } 2>/dev/null || true
  fi

  # 停止旧服务。不 pkill node_exporter，避免误杀新安装的进程。
  for svc in monitor_manage zabbix_agentd node_exporter; do
    if command -v systemctl >/dev/null 2>&1; then
      systemctl stop "$svc" >/dev/null 2>&1 || true
      systemctl disable "$svc" >/dev/null 2>&1 || true
      systemctl reset-failed "$svc" >/dev/null 2>&1 || true
    fi
    if [ -x "/etc/init.d/$svc" ]; then
      "/etc/init.d/$svc" stop >/dev/null 2>&1 || true
    fi
  done
  pkill -f "monitor_manage" >/dev/null 2>&1 || true
  pkill -f "zabbix_agent2" >/dev/null 2>&1 || true

  # 只 mask 旧 zabbix 服务；不要 mask node_exporter（新服务同名）。
  if command -v systemctl >/dev/null 2>&1; then
    systemctl mask monitor_manage >/dev/null 2>&1 || true
    systemctl mask zabbix_agentd >/dev/null 2>&1 || true
  fi
  if command -v chkconfig >/dev/null 2>&1; then
    for svc in monitor_manage zabbix_agentd node_exporter; do
      chkconfig --del "$svc" >/dev/null 2>&1 || true
    done
  fi

  # 删除旧 init/unit 文件。
  rm -f /etc/init.d/monitor_manage /etc/init.d/zabbix_agentd /etc/init.d/node_exporter
  rm -f /etc/systemd/system/monitor_manage.service /etc/systemd/system/zabbix_agentd.service /etc/systemd/system/node_exporter.service
  rm -f /usr/lib/systemd/system/monitor_manage.service /usr/lib/systemd/system/zabbix_agentd.service /usr/lib/systemd/system/node_exporter.service
  rm -f /lib/systemd/system/zabbix* /usr/lib/systemd/system/zabbix* /etc/init.d/zabbix*
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl reset-failed >/dev/null 2>&1 || true
  fi

  # 旧目录、二进制与配置。不要删除新路径 /opt/monitor/exporter。
  rm -rf /usr/local/zabbix /etc/zabbix
  rm -f /usr/bin/zabbix* /usr/sbin/zabbix* /usr/local/bin/zabbix* /bin/zabbix*
  rm -f /usr/bin/monitor_manage
  rm -f /usr/bin/check_update.sh /usr/bin/check_monitor_update.sh
  rm -f /usr/bin/node_exporter /usr/local/bin/node_exporter /usr/local/zabbix/sbin/node_exporter
  # 旧 zabbix 用户/组。
  userdel -rf zabbix >/dev/null 2>&1 || true
  groupdel zabbix >/dev/null 2>&1 || true
  log "旧监控清理完成"
}

install_agent() {
  require_server
  tmp="$(mktemp /tmp/upgrade-agent.XXXXXX)"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" EXIT
  # 旧内核（CentOS 6 的 2.6.32 等）必须用 Go 1.23 legacy 构建，
  # 否则新 Agent 启动会报 "procresize: invalid arg"。
  kernel="$(uname -r 2>/dev/null || true)"
  legacy_suffix=""
  case "$kernel" in
    2.*) legacy_suffix="-legacy" ;;
  esac
  url="$SERVER/api/agent/binary/linux-$ARCH$legacy_suffix"
  [ -z "$kernel" ] || url="$url?kernel=$kernel"
  log "下载 Agent: $url（内核 ${kernel:-未知}）"
  download_to "$url" "$tmp"
  chmod +x "$tmp"
  log "安装/重新注册 Agent（--force）..."
  "$tmp" install --server "$SERVER" --enroll-token "$TOKEN" --force
  rm -f "$tmp"
  trap - EXIT
}

install_node() {
  require_server
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
  log "控制台: ${SERVER:-未指定（本地状态）}"
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
  check_port_conflict
  if probe_metrics; then
    log "指标探测: 正常（${PROBE_URL}）"
  else
    warn "指标探测: 失败（${PROBE_URL}，可执行 install/update 重试）"
  fi
}

rotate_server
if [ "$ACTION" != "status" ] && [ -z "$SERVER" ] && [ "$INTERACTIVE" = "1" ]; then
  printf '请输入控制台地址（如 http://mgr.example.com）: ' >&2
  read -r SERVER || true
  rotate_server
fi
case "$ACTION" in
  install)
    need_root
    detect_arch
    fetch_token
    check_port_conflict
    # 先装/更新 Agent，确保旧监控卸载期间仍能保持平台连接（防止失联）。
    install_agent
    legacy_cleanup
    install_node install
    verify_or_fail "安装"
    ;;
  update)
    need_root
    detect_arch
    fetch_token
    check_port_conflict
    # 先更新 Agent，再清理旧监控。
    install_agent
    legacy_cleanup
    install_node update || { warn "Node Exporter update 失败，尝试 install"; install_node install; }
    verify_or_fail "更新"
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

if [ "$INTERACTIVE" = "1" ]; then
  printf '\n按回车键退出...'
  read -r _ || true
fi
