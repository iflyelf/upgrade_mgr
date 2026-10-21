#!/bin/bash
# node_exporter 单文件安装脚本（可直接作为平台制品上传）
#
# 用法：sudo bash install.sh [amd64|arm64|arm|386]
#
# 环境变量（均可选）：
#   NODE_EXPORTER_ARCH     目标架构，默认自动执行 uname -m
#   NODE_EXPORTER_VERSION  版本，默认 latest
#   NODE_EXPORTER_PROXY    下载代理前缀，默认 https://proxy.xiaonuo.live/?url=
#   NODE_EXPORTER_BASE     下载基础 URL，默认 GitHub iflyelf release 地址
#
# 支持：
#   - systemd：CentOS/RHEL/Rocky/openEuler 7+、Ubuntu 16.04+、Debian 8+、SLES 12+
#   - SysV/upstart：CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11
#
# 二进制使用 iflyelf/node_exporter（静态编译，兼容 CentOS 6 等老内核），
# 不依赖目标机 Go / glibc。systemd unit 与 SysV init 已内嵌在本脚本中，
# 单独上传并在任意目录执行即可，不再依赖同目录的 service/init 文件。
#
# 脚本使用 POSIX sh 兼容写法；Agent 优先用 /bin/bash，缺失时回退 /bin/sh。
set -eu

ARCH="${NODE_EXPORTER_ARCH:-${1:-}}"
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

VERSION="${NODE_EXPORTER_VERSION:-latest}"
PROXY="${NODE_EXPORTER_PROXY:-https://proxy.xiaonuo.live/?url=}"
BASE="${NODE_EXPORTER_BASE:-https://github.com/iflyelf/node_exporter/releases/download/${VERSION}}"
INSTALL_DIR="/opt/monitor/exporter"
UNIT_FILE="/etc/systemd/system/node_exporter.service"
INIT_FILE="/etc/init.d/node_exporter"
TMP_BIN="/tmp/node_exporter-download.$$"
trap 'rm -f "$TMP_BIN"' EXIT

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

write_unit() {
  cat > "$UNIT_FILE" <<'NODE_EXPORTER_UNIT'
# node_exporter systemd unit 示例
# 安装路径：/opt/monitor/exporter/node_exporter
# 指标目录：/opt/monitor/exporter/metrics（textfile collector）
# 监听端口：16666
#
# 注意：
# - ExecStart 中的 --web.listen-address 必须和前面的参数在同一行；按 -- 断行会变成两个参数。
# - MemoryMin / MemoryMax 默认注释：node_exporter 常驻内存通常只有几十 MB，2G/4G 属于过度配置；
#   MemoryMin 需要 cgroup v2（systemd 243+），小内存机器强行设置可能导致启动失败。
# - TimeoutStartSec / TimeoutStopSec 分开写，避免 TimeoutSec 同时覆盖停止超时。
# - systemd 目标机使用本 unit；CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11 等
#   无 systemd 的系统使用 node_exporter.init。
# - install.sh 已内嵌本 unit 与 init 脚本，可单独上传执行；本文件保留作参考。

[Unit]
# 描述服务
Description=Node Exporter
# 描述服务类别
After=network.target
# 禁用启动频率限制的时间窗口
StartLimitIntervalSec=0
# 禁用启动尝试的次数限制
StartLimitBurst=0

[Service]
# 启动类型：启动主进程
Type=simple
# 启动当前服务的命令（web.listen-address 必须在同一行）
ExecStart=/opt/monitor/exporter/node_exporter --collector.processes --collector.tcpstat --log.level=error --collector.textfile.directory=/opt/monitor/exporter/metrics --web.listen-address=:16666
# 重启当前服务时执行的命令
ExecReload=/bin/kill -s HUP $MAINPID
# 停止当前服务时执行的命令
ExecStop=/bin/kill -SIGTERM $MAINPID
# 启动服务用户
User=root
# 启动服务用户组
Group=root
# 只终止主进程
KillMode=process
# 终止信号
KillSignal=SIGTERM
# 启动超时 10 秒
TimeoutStartSec=10
# 等待 30 秒后强制终止
TimeoutStopSec=30
# 重启方式：不管是什么退出原因，总是重启
Restart=always
# 重启需要等待的秒数
RestartSec=5
# 文件目录权限
UMask=0022
# CPU 限制
CPUQuota=50%
# 内存限制（可选，按需取消注释并调整）
# MemoryMin=2G
# MemoryMax=4G
# 进程因内存不足而被杀死的优先级（可选）
OOMScoreAdjust=-1000
# 指定服务在系统日志中记录信息时使用的标识符
SyslogIdentifier=node_exporter
# 进程标准输出
StandardOutput=journal
# 进程标准错误
StandardError=journal+console
# 系统文件数限制
TasksMax=infinity
LimitSTACK=infinity
LimitCORE=infinity
LimitNOFILE=infinity
LimitNPROC=infinity

[Install]
# 表示该服务所在的 Target
WantedBy=multi-user.target
NODE_EXPORTER_UNIT
  chmod 0644 "$UNIT_FILE"
}

write_init() {
  cat > "$INIT_FILE" <<'NODE_EXPORTER_INIT'
#!/bin/sh
#
# node_exporter    Prometheus Node Exporter
#
# chkconfig: 2345 90 10
# description: Prometheus node_exporter
#
### BEGIN INIT INFO
# Provides:          node_exporter
# Required-Start:    $network $local_fs
# Required-Stop:     $network $local_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: Prometheus Node Exporter
# Description:       Exposes machine metrics for Prometheus
### END INIT INFO
#
# 适用于无 systemd 的系统：
#   - CentOS/RHEL 6（upstart + SysV）
#   - Ubuntu 14.04（upstart）
#   - Debian 7（SysV）
#   - SUSE Linux Enterprise 11（SysV）
# 安装路径、监听端口、collector 参数与 node_exporter.service 保持一致。
# 本文件已内嵌到 install.sh，可单独上传执行；这里保留作参考。
# 日志文件默认 /var/log/node_exporter.log，如需轮转请自行配置 logrotate。

PATH=/sbin:/usr/sbin:/bin:/usr/bin
export PATH

NAME=node_exporter
BIN=/opt/monitor/exporter/node_exporter
PIDFILE=/var/run/node_exporter.pid
LOGFILE=/var/log/node_exporter.log
METRICS_DIR=/opt/monitor/exporter/metrics
ARGS="--collector.processes --collector.tcpstat --log.level=error --collector.textfile.directory=/opt/monitor/exporter/metrics --web.listen-address=:16666"

is_running() {
    [ -f "$PIDFILE" ] || return 1
    PID=$(cat "$PIDFILE" 2>/dev/null)
    [ -n "$PID" ] || return 1
    kill -0 "$PID" 2>/dev/null
}

start() {
    if [ ! -x "$BIN" ]; then
        echo "$BIN is not executable" >&2
        return 1
    fi

    if is_running; then
        echo "$NAME is already running (pid $(cat "$PIDFILE"))"
        return 0
    fi

    # PID 文件丢失但进程还在（例如被手工启动）时，接管已有进程。
    if command -v pgrep >/dev/null 2>&1; then
        OTHER_PID=$(pgrep -f "^${BIN}([[:space:]]|\$)" 2>/dev/null | head -n 1)
        if [ -n "$OTHER_PID" ]; then
            echo "$OTHER_PID" >"$PIDFILE"
            echo "$NAME is already running (pid $OTHER_PID)"
            return 0
        fi
    fi

    rm -f "$PIDFILE"
    mkdir -p "$METRICS_DIR"
    nohup "$BIN" $ARGS >>"$LOGFILE" 2>&1 &
    PID=$!
    echo "$PID" >"$PIDFILE"
    sleep 1
    if kill -0 "$PID" 2>/dev/null; then
        echo "$NAME started (pid $PID)"
        return 0
    fi

    echo "$NAME failed to start; see $LOGFILE" >&2
    rm -f "$PIDFILE"
    return 1
}

stop() {
    if is_running; then
        PID=$(cat "$PIDFILE" 2>/dev/null)
        kill "$PID" 2>/dev/null || true
        i=0
        while [ $i -lt 10 ]; do
            kill -0 "$PID" 2>/dev/null || break
            sleep 1
            i=$((i + 1))
        done
        if kill -0 "$PID" 2>/dev/null; then
            kill -9 "$PID" 2>/dev/null || true
        fi
        echo "$NAME stopped"
    elif command -v pgrep >/dev/null 2>&1; then
        PIDS=$(pgrep -f "^${BIN}([[:space:]]|\$)" 2>/dev/null || true)
        if [ -n "$PIDS" ]; then
            kill $PIDS 2>/dev/null || true
            echo "$NAME stopped"
        else
            echo "$NAME is not running"
        fi
    else
        echo "$NAME is not running"
    fi
    rm -f "$PIDFILE"
    return 0
}

status() {
    if is_running; then
        echo "$NAME is running (pid $(cat "$PIDFILE"))"
        return 0
    fi
    echo "$NAME is stopped"
    return 3
}

case "$1" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        stop
        start
        ;;
    status)
        status
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}" >&2
        exit 2
        ;;
esac

exit $?
NODE_EXPORTER_INIT
  chmod 0755 "$INIT_FILE"
}

echo "下载 node_exporter-linux-${ARCH}（${VERSION}，静态编译）..."
download "${PROXY}${BASE}/node_exporter-linux-${ARCH}" "$TMP_BIN"

install -d -m 0755 "${INSTALL_DIR}/metrics"
install -m 0755 "$TMP_BIN" "${INSTALL_DIR}/node_exporter"

if [ "$USE_SYSTEMD" -eq 1 ]; then
  write_unit
  systemctl daemon-reload
  systemctl enable node_exporter
  systemctl restart node_exporter
else
  write_init

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
