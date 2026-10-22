// 升级控制平台 — 最小前端（原生 JS，无构建依赖）
// P0 提供：登录、主机列表(分组筛选)、设置、主题切换。
// 后续用 Vue3 构建产物替换本文件与 app.css。
(function () {
  var app = document.getElementById('app');
  var THEMES = [
    { value: 'warm', emoji: '🌞', label: '暖沙米' },
    { value: 'cool', emoji: '🌊', label: '冷蓝' },
    { value: 'dark', emoji: '🌙', label: '暗黑' }
  ];

  function setTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    localStorage.setItem('upgrade_mgr_theme', t);
    render();
  }
  function curTheme() { return document.documentElement.getAttribute('data-theme') || 'warm'; }

  function api(path, opts) {
    opts = opts || {};
    opts.headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
    return fetch(path, opts).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok || (j && j.code && j.code !== 0)) {
          throw new Error((j && j.message) || ('HTTP ' + r.status));
        }
        return j.data;
      });
    });
  }

  var state = { user: null, view: 'hosts', hosts: [], total: 0, q: '', settings: [], components: [], artifacts: [], tasks: [], err: '',
    hostPage: 1, hostSize: 50, hostPages: 1, hostErr: '', hostsLoading: false, editingHost: null,
    agentTargetVersion: '', agentAutoUpdate: false,
    guideOpen: null, enroll: null, preview: null, editingComp: null, taskForm: null, uploadComponent: '',
    taskDetailId: null, taskDetail: null, taskDetailPage: 1, taskDetailLoading: false, taskDetailErr: '', taskDetailScroll: 0,
    taskDetailOpenLogs: {},
    // 自动刷新：记录上一次数据指纹，状态没变化时不重建 DOM，避免滚动/焦点抖动。
    hostsSig: '', tasksSig: '', taskDetailSig: '', loginName: '', loginBusy: false,
    // 任务页按组件筛选：'' 表示全部；用于删除组件前核对引用。
    taskFilterComp: '', taskFilterKind: '' };

  function h(tag, attrs, children) {
    var el = document.createElement(tag);
    attrs = attrs || {};
    var val;
    for (var k in attrs) {
      var v = attrs[k];
      if (v == null) continue;
      if (k.indexOf('on') === 0 && typeof v === 'function') el[k] = v;
      else if (k === 'value') val = v;
      else if (k === 'checked') el.checked = !!v;
      else el.setAttribute(k, v);
    }
    (children || []).forEach(function (c) {
      if (c == null) return;
      el.appendChild(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(String(c)) : c);
    });
    // select 的 value 需要在 option 挂载后设置才会生效
    if (val !== undefined) el.value = val;
    return el;
  }

  var fieldSeq = 0;
  // field 生成带 label 关联的表单项，hintText 为可选说明。
  function field(label, input, hintText) {
    if (!input.id) input.id = 'f' + (++fieldSeq);
    return h('div', { class: 'field' }, [
      h('label', { for: input.id }, [label]),
      input,
      hintText ? hint(hintText) : null
    ]);
  }
  function hint(text) { return h('div', { class: 'hint' }, [text]); }

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }
  function copyText(text, btn) {
    function done(ok) {
      btn.textContent = ok ? '已复制' : '复制失败，请手动选中';
      setTimeout(function () { btn.textContent = '复制'; }, 1500);
    }
    // 非 HTTPS 页面没有 navigator.clipboard，回退到 execCommand
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(fallbackCopy(text)); });
    } else {
      done(fallbackCopy(text));
    }
  }
  function codeBlock(text) {
    var btn = h('button', { type: 'button', class: 'btn ghost small copy-btn', 'aria-label': '复制代码' }, ['复制']);
    btn.onclick = function () { copyText(text, btn); };
    return h('div', { class: 'code-wrap' }, [btn, h('pre', { class: 'code' }, [text])]);
  }
  function isAdmin() { return !!(state.user && state.user.IsAdmin); }

  var TASK_STATUS = {
    draft: '待启动', running: '运行中', paused: '已暂停',
    success: '成功', failed: '失败', canceled: '已取消', rolled_back: '已回滚'
  };
  var HOST_STATUS = {
    pending: '待下发', running: '执行中', success: '成功', failed: '失败',
    rolled_back: '已回滚', skipped: '已跳过', timeout: '超时', canceled: '已取消'
  };
  var CMD_STATUS = {
    pending: '待下发', dispatched: '已下发', accepted: '已接收', running: '执行中',
    success: '成功', failed: '失败', rolled_back: '已回滚', timeout: '超时',
    expired: '已过期', canceled: '已取消'
  };
  var BATCH_STATUS = {
    pending: '待执行', running: '执行中', success: '成功', failed: '失败',
    skipped: '已跳过', canceled: '已取消'
  };
  var STAGE_TEXT = {
    accepted: '已接收', enter_maintenance: '进入维护窗口', maintenance: '维护窗口',
    downloading: '下载制品', downloaded: '下载完成', pre_install: '安装前检查',
    backup: '备份当前版本', stopping: '停止服务', replacing: '替换文件',
    exec: '执行安装', starting: '启动服务', verifying: '验证版本',
    post_install: '安装后处理', rollback: '回滚', failed: '失败', done: '完成',
    run_command: '执行命令'
  };
  var AGENT_STATUS = {
    online: '在线', upgrading: '升级中', restarting: '重启中',
    crash_loop: '崩溃重启', offline: '离线', unknown: '未知'
  };
  function statusText(s) { return TASK_STATUS[s] || s; }
  function hostStatusText(s) { return HOST_STATUS[s] || s || '-'; }
  function cmdStatusText(s) { return CMD_STATUS[s] || s || '-'; }
  function batchStatusText(s) { return BATCH_STATUS[s] || s || '-'; }
  function stageText(s) { return STAGE_TEXT[s] || s || '-'; }
  function agentStatusText(s) { return AGENT_STATUS[s] || s || '未知'; }
  function fmtTime(v) {
    if (!v) return '-';
    var d = new Date(v);
    if (isNaN(d.getTime())) return String(v);
    function p(n) { return (n < 10 ? '0' : '') + n; }
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' +
      p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  }
  function deletableTask(x) {
    return ['draft', 'success', 'failed', 'canceled', 'rolled_back'].indexOf(x.status) >= 0;
  }

  function themeSwitcher() {
    return h('div', { class: 'theme-switcher' }, THEMES.map(function (t) {
      return h('button', {
        class: 'theme-btn' + (curTheme() === t.value ? ' active' : ''),
        title: t.label, onclick: function () { setTheme(t.value); }
      }, [t.emoji]);
    }));
  }

  function topbar() {
    return h('div', { class: 'topbar' }, [
      h('div', { class: 'brand' }, ['🚀 升级控制平台']),
      h('div', { class: 'spacer' }, []),
      themeSwitcher(),
      state.user ? h('button', { class: 'btn', onclick: function() { switchView('password'); } }, ['修改密码']) : null,
      state.user ? h('button', { class: 'btn', onclick: logout }, ['退出']) : null
    ]);
  }

  function tabs() {
    var items = [['hosts', '主机'], ['components', '组件'], ['artifacts', '制品'], ['tasks', '任务'], ['settings', '设置']];
    return h('div', { class: 'tabs' }, items.map(function (it) {
      return h('div', {
        class: 'tab' + (state.view === it[0] ? ' active' : ''),
        onclick: function () { switchView(it[0]); }
      }, [it[1]]);
    }));
  }

  // ---------- 内置示例 ----------
  // node_exporter 的 systemd unit 关键配置与 examples/node_exporter/node_exporter.service 保持一致（页面内省略注释）。
  // 注意：ExecStart 中的 web.listen-address 必须在同一行，不能按 -- 断行。
  var NODE_EXPORTER_UNIT = [
    '[Unit]',
    'Description=Node Exporter',
    'After=network.target',
    'StartLimitIntervalSec=0',
    'StartLimitBurst=0',
    '',
    '[Service]',
    'Type=simple',
    'ExecStart=/opt/monitor/exporter/node_exporter --collector.processes --collector.tcpstat --log.level=error --collector.textfile.directory=/opt/monitor/exporter/metrics --web.listen-address=:16666',
    'ExecReload=/bin/kill -s HUP $MAINPID',
    'ExecStop=/bin/kill -SIGTERM $MAINPID',
    'User=root',
    'Group=root',
    'KillMode=process',
    'KillSignal=SIGTERM',
    'TimeoutStartSec=10',
    'TimeoutStopSec=30',
    'Restart=always',
    'RestartSec=5',
    'UMask=0022',
    'CPUQuota=50%',
    'MemoryMin=2G',
    'MemoryMax=4G',
    'OOMScoreAdjust=-1000',
    'SyslogIdentifier=node_exporter',
    'StandardOutput=journal',
    'StandardError=journal+console',
    'TasksMax=infinity',
    'LimitSTACK=infinity',
    'LimitCORE=infinity',
    'LimitNOFILE=infinity',
    'LimitNPROC=infinity',
    '',
    '[Install]',
    'WantedBy=multi-user.target'
  ].join('\n');

  // node_exporter 的 SysV init 脚本，与 examples/node_exporter/node_exporter.init 保持一致。
  // 无 systemd 的 CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11 等使用此脚本。
  var NODE_EXPORTER_INIT = [
    '#!/bin/sh',
    '#',
    '# node_exporter    Prometheus Node Exporter',
    '#',
    '# chkconfig: 2345 90 10',
    '# description: Prometheus node_exporter',
    '#',
    '### BEGIN INIT INFO',
    '# Provides:          node_exporter',
    '# Required-Start:    $network $local_fs',
    '# Required-Stop:     $network $local_fs',
    '# Default-Start:     2 3 4 5',
    '# Default-Stop:      0 1 6',
    '# Short-Description: Prometheus Node Exporter',
    '# Description:       Exposes machine metrics for Prometheus',
    '### END INIT INFO',
    '#',
    '# 适用于无 systemd 的系统：',
    '#   - CentOS/RHEL 6（upstart + SysV）',
    '#   - Ubuntu 14.04（upstart）',
    '#   - Debian 7（SysV）',
    '#   - SUSE Linux Enterprise 11（SysV）',
    '# 安装路径、监听端口、collector 参数与 node_exporter.service 保持一致。',
    '# 日志文件默认 /var/log/node_exporter.log，如需轮转请自行配置 logrotate。',
    '',
    'PATH=/sbin:/usr/sbin:/bin:/usr/bin',
    'export PATH',
    '',
    'NAME=node_exporter',
    'BIN=/opt/monitor/exporter/node_exporter',
    'PIDFILE=/var/run/node_exporter.pid',
    'LOGFILE=/var/log/node_exporter.log',
    'METRICS_DIR=/opt/monitor/exporter/metrics',
    'ARGS="--collector.processes --collector.tcpstat --log.level=error --collector.textfile.directory=/opt/monitor/exporter/metrics --web.listen-address=:16666"',
    '',
    'is_running() {',
    '    [ -f "$PIDFILE" ] || return 1',
    '    PID=$(cat "$PIDFILE" 2>/dev/null)',
    '    [ -n "$PID" ] || return 1',
    '    kill -0 "$PID" 2>/dev/null',
    '}',
    '',
    'start() {',
    '    if [ ! -x "$BIN" ]; then',
    '        echo "$BIN is not executable" >&2',
    '        return 1',
    '    fi',
    '',
    '    if is_running; then',
    '        echo "$NAME is already running (pid $(cat "$PIDFILE"))"',
    '        return 0',
    '    fi',
    '',
    '    # PID 文件丢失但进程还在（例如被手工启动）时，接管已有进程。',
    '    if command -v pgrep >/dev/null 2>&1; then',
    '        OTHER_PID=$(pgrep -f "^${BIN}([[:space:]]|\\$)" 2>/dev/null | head -n 1)',
    '        if [ -n "$OTHER_PID" ]; then',
    '            echo "$OTHER_PID" >"$PIDFILE"',
    '            echo "$NAME is already running (pid $OTHER_PID)"',
    '            return 0',
    '        fi',
    '    fi',
    '',
    '    rm -f "$PIDFILE"',
    '    mkdir -p "$METRICS_DIR"',
    '    nohup "$BIN" $ARGS >>"$LOGFILE" 2>&1 &',
    '    PID=$!',
    '    echo "$PID" >"$PIDFILE"',
    '    sleep 1',
    '    if kill -0 "$PID" 2>/dev/null; then',
    '        echo "$NAME started (pid $PID)"',
    '        return 0',
    '    fi',
    '',
    '    echo "$NAME failed to start; see $LOGFILE" >&2',
    '    rm -f "$PIDFILE"',
    '    return 1',
    '}',
    '',
    'stop() {',
    '    if is_running; then',
    '        PID=$(cat "$PIDFILE" 2>/dev/null)',
    '        kill "$PID" 2>/dev/null || true',
    '        i=0',
    '        while [ $i -lt 10 ]; do',
    '            kill -0 "$PID" 2>/dev/null || break',
    '            sleep 1',
    '            i=$((i + 1))',
    '        done',
    '        if kill -0 "$PID" 2>/dev/null; then',
    '            kill -9 "$PID" 2>/dev/null || true',
    '        fi',
    '        echo "$NAME stopped"',
    '    elif command -v pgrep >/dev/null 2>&1; then',
    '        PIDS=$(pgrep -f "^${BIN}([[:space:]]|\\$)" 2>/dev/null || true)',
    '        if [ -n "$PIDS" ]; then',
    '            kill $PIDS 2>/dev/null || true',
    '            echo "$NAME stopped"',
    '        else',
    '            echo "$NAME is not running"',
    '        fi',
    '    else',
    '        echo "$NAME is not running"',
    '    fi',
    '    rm -f "$PIDFILE"',
    '    return 0',
    '}',
    '',
    'status() {',
    '    if is_running; then',
    '        echo "$NAME is running (pid $(cat "$PIDFILE"))"',
    '        return 0',
    '    fi',
    '    echo "$NAME is stopped"',
    '    return 3',
    '}',
    '',
    'case "$1" in',
    '    start)',
    '        start',
    '        ;;',
    '    stop)',
    '        stop',
    '        ;;',
    '    restart)',
    '        stop',
    '        start',
    '        ;;',
    '    status)',
    '        status',
    '        ;;',
    '    *)',
    '        echo "Usage: $0 {start|stop|restart|status}" >&2',
    '        exit 2',
    '        ;;',
    'esac',
    '',
    'exit $?',
  ].join('\n');

  // post_install 脚本：首次安装时根据 init 系统写 systemd unit 或 SysV init 脚本；
  // 服务启动由 Agent 在 post_install 之后完成。已存在的文件不覆盖。
  // 该脚本保存到组件的 post_install_script 字段，由 Agent 写入临时文件后执行。
  var NODE_EXPORTER_POST_INSTALL_SCRIPT = [
    '#!/bin/sh',
    'set -e',
    'mkdir -p /opt/monitor/exporter/metrics',
    'if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then',
    '  if [ ! -f /etc/systemd/system/node_exporter.service ]; then',
    "    cat > /etc/systemd/system/node_exporter.service <<'EOF'",
    NODE_EXPORTER_UNIT,
    'EOF',
    '    systemctl daemon-reload',
    '    systemctl enable node_exporter',
    '  fi',
    'else',
    '  if [ ! -f /etc/init.d/node_exporter ]; then',
    "    cat > /etc/init.d/node_exporter <<'EOF'",
    NODE_EXPORTER_INIT,
    'EOF',
    '    chmod 0755 /etc/init.d/node_exporter',
    '    REGISTERED=0',
    '    if command -v chkconfig >/dev/null 2>&1; then',
    '      chkconfig --add node_exporter >/dev/null 2>&1 || true',
    '      if chkconfig node_exporter on >/dev/null 2>&1; then',
    '        REGISTERED=1',
    '      fi',
    '    fi',
    '    if [ "$REGISTERED" -eq 0 ] && command -v update-rc.d >/dev/null 2>&1; then',
    '      if update-rc.d node_exporter defaults >/dev/null 2>&1; then',
    '        REGISTERED=1',
    '      fi',
    '    fi',
    '    if [ "$REGISTERED" -eq 0 ] && command -v insserv >/dev/null 2>&1; then',
    '      if insserv node_exporter >/dev/null 2>&1; then',
    '        REGISTERED=1',
    '      fi',
    '    fi',
    '    if [ "$REGISTERED" -eq 0 ]; then',
    '      echo "WARN: no chkconfig/update-rc.d/insserv; node_exporter will not start on boot"',
    '    fi',
    '  fi',
    'fi'
  ].join('\n');

  // INSTALL_SCRIPT_URL：node_exporter 安装脚本（经 proxy 代理），组件示例与任务示例共用。
  var INSTALL_SCRIPT_URL = 'https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh';

  // installScript(action) 生成“先下载 install.sh 再执行指定动作”的脚本。
  // 不使用 `bash <(curl ...)`：某些环境把 -c 字符串交给 /bin/sh（dash）解析，
  // 进程替换 <(...) 是 bash 语法，会报 Syntax error: "(" unexpected。
  // 组件 exec 模式与任务命令模式共用这段骨架，只改最后一个动作参数。
  function installScript(action) {
    return [
      '#!/bin/bash',
      'set -e',
      'url=\'' + INSTALL_SCRIPT_URL + '\'',
      'f=$(mktemp /tmp/node_exporter-install.XXXXXX.sh)',
      'trap \'rm -f "$f"\' EXIT',
      'if command -v curl >/dev/null 2>&1; then',
      '  curl -fsSL "$url" -o "$f"',
      'elif command -v wget >/dev/null 2>&1; then',
      '  wget -qO "$f" "$url"',
      'else',
      '  echo "需要 curl 或 wget 下载安装脚本" >&2; exit 1',
      'fi',
      'bash "$f" ' + action
    ].join('\n');
  }

  // 组件模板：顶层字段对应表单，其余字段回填到“高级配置 JSON”。
  //
  // 「只执行命令」的三个示例都用 script_url + 简短 exec_args（install/update/uninstall），
  // 不再把整段下载脚本塞进 exec_args：Agent 下载脚本后把 exec_args 作为脚本参数执行，
  // 与 script 模式体验一致，配置更短、更不容易写错。
  var COMP_TEMPLATES = [
    {
      key: 'node_exporter_exec',
      label: '只执行命令（exec + script_url：安装 node_exporter）',
      desc: '部署模式 exec，但不填制品、只填 script_url：Agent 先下载 install.sh，再把 exec_args（这里是 install）作为脚本参数执行。相比早期在 exec_args 里塞整段 shell，配置只需一个 URL + 一个动作。脚本会自行下载二进制并注册服务；平台不备份、不替换二进制，服务启停与回滚由脚本负责（本示例不填服务名，避免平台重复启停）。需要升级请用“更新”示例，卸载请用“卸载”示例。',
      comp: {
        name: 'node_exporter_cmd', display_name: 'Node Exporter（只执行命令-安装）',
        deploy_mode: 'exec',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter/node_exporter',
        service_name: '', service_mgr: 'auto', enabled: true,
        script_url: INSTALL_SCRIPT_URL,
        exec_args: ['install'],
        exec_timeout: '30m',
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']]
      }
    },
    {
      key: 'node_exporter_exec_update',
      label: '只执行命令（exec + script_url：更新 node_exporter）',
      desc: '与“安装”示例同源，script_url 相同，只把 exec_args 改成 update：Agent 下载 install.sh 后执行 update，脚本先备份旧二进制到 node_exporter.bak，重启服务后若启动失败会自动回滚。适合已安装过 node_exporter、只想升级版本的机器。组件名独立（node_exporter_update），避免与安装组件互相覆盖。',
      comp: {
        name: 'node_exporter_update', display_name: 'Node Exporter（只执行命令-更新）',
        deploy_mode: 'exec',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter/node_exporter',
        service_name: '', service_mgr: 'auto', enabled: true,
        script_url: INSTALL_SCRIPT_URL,
        exec_args: ['update'],
        exec_timeout: '30m',
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']]
      }
    },
    {
      key: 'node_exporter_exec_uninstall',
      label: '只执行命令（exec + script_url：卸载 node_exporter）',
      desc: 'exec_args 为 uninstall -y（-y 跳过交互确认，exec 模式没有 stdin）。Agent 下载 install.sh 后执行卸载，停止并删除服务、二进制与数据。卸载后目标机上已无该服务，因此本示例不填 health_cmd / version_cmd——若保留会在卸载完成后做健康检查并必然失败。',
      comp: {
        name: 'node_exporter_uninstall', display_name: 'Node Exporter（只执行命令-卸载）',
        deploy_mode: 'exec',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter/node_exporter',
        service_name: '', service_mgr: 'auto', enabled: true,
        script_url: INSTALL_SCRIPT_URL,
        exec_args: ['uninstall', '-y'],
        exec_timeout: '30m'
      }
    },
    {
      key: 'node_exporter_script',
      label: 'node_exporter_script（脚本安装，Agent 下载后执行）',
      desc: '直接使用真实可用的 script_url（通过 proxy.xiaonuo.live 代理本仓库 examples/node_exporter/install.sh），不需要上传制品。Agent 下载脚本后用 Shell 执行；脚本会自行从 iflyelf/node_exporter latest 下载静态二进制（当前 1.12.1，仅 amd64/arm64），并按目标机 init 写 systemd unit 或 SysV init。script 模式与命令任务默认可用（由 agent.allow_exec_mode 控制，默认开启，可在“设置”里关闭）；高级配置 JSON 已预填 script_url、exec_env、exec_timeout、health_cmd 与 version_cmd，post_install 脚本文本框也填入了一段可选的安装后检查示例，可直接修改参考。若不想依赖外链，删除高级 JSON 中的 script_url，改为上传 examples/node_exporter/install.sh 作为制品即可。',
      comp: {
        name: 'node_exporter_script', display_name: 'Node Exporter（脚本安装）', deploy_mode: 'script',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter',
        service_name: 'node_exporter', service_mgr: 'auto', enabled: true,
        script_url: 'https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh',
        exec_env: ['NODE_EXPORTER_VERSION=latest'],
        exec_timeout: '10m',
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']],
        post_install_script: [
          '#!/bin/sh',
          '# post_install 脚本示例（可选）：在安装脚本执行后由 Agent 运行。',
          '# 可在这里补充配置、注册服务或做安装后检查；不需要时整段删除。',
          'if [ -f /etc/systemd/system/node_exporter.service ]; then',
          '  echo "node_exporter systemd unit found"',
          'elif [ -f /etc/init.d/node_exporter ]; then',
          '  echo "node_exporter SysV init found"',
          'else',
          '  echo "WARN: node_exporter service file not found"',
          'fi'
        ].join('\n')
      }
    },
    {
      key: 'node_exporter_script_update',
      label: 'node_exporter_script_update（脚本更新，失败自动回滚）',
      desc: '与“脚本安装”示例同源，script_url 相同，只把 exec_args 改成 update：Agent 下载 install.sh 后按参数执行 update，脚本先备份旧二进制到 node_exporter.bak，重启服务后若启动失败会自动回滚。适合已安装过 node_exporter、只想升级版本的机器。组件名独立（node_exporter_script_update），避免与安装组件互相覆盖。',
      comp: {
        name: 'node_exporter_script_update', display_name: 'Node Exporter（脚本更新）', deploy_mode: 'script',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter',
        service_name: 'node_exporter', service_mgr: 'auto', enabled: true,
        script_url: INSTALL_SCRIPT_URL,
        exec_args: ['update'],
        exec_env: ['NODE_EXPORTER_VERSION=latest'],
        exec_timeout: '10m',
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']]
      }
    },
    {
      key: 'node_exporter_script_uninstall',
      label: 'node_exporter_script_uninstall（脚本卸载，非交互）',
      desc: '与“脚本安装”示例同源，script_url 相同，exec_args 为 uninstall -y（-y 跳过交互确认）。脚本模式没有交互终端，必须带 -y，否则会一直等不到确认而取消。卸载后服务已不存在，因此本示例不填服务名、health_cmd、version_cmd，避免平台在卸载后尝试启服并做健康检查导致任务必然失败。',
      comp: {
        name: 'node_exporter_script_uninstall', display_name: 'Node Exporter（脚本卸载）', deploy_mode: 'script',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter',
        service_name: '', service_mgr: 'auto', enabled: true,
        script_url: INSTALL_SCRIPT_URL,
        exec_args: ['uninstall', '-y'],
        exec_timeout: '10m'
      }
    },
    {
      key: 'node_exporter_binary',
      label: 'node_exporter_binary（高级：二进制替换 + post_install 脚本注册服务）',
      desc: '把对应架构的 node_exporter 静态二进制作为制品上传。Agent 替换到安装路径后，执行下方“post_install 脚本”：判断目标机 init 并写 unit/init。脚本单独存放，不再塞进高级配置 JSON。',
      comp: {
        name: 'node_exporter_binary', display_name: 'Node Exporter（二进制替换）', deploy_mode: 'replace',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter/node_exporter',
        service_name: 'node_exporter', service_mgr: 'auto', enabled: true,
        post_install_script: NODE_EXPORTER_POST_INSTALL_SCRIPT,
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']]
      }
    }
  ];
  var ADV_KEYS = ['exec_path', 'exec_args', 'exec_as_user', 'exec_timeout', 'exec_env', 'work_dir', 'script_url',
    'ignore_error', 'pre_install', 'post_install', 'stop_cmd', 'start_cmd', 'restart_cmd',
    'version_cmd', 'version_regex', 'health_cmd', 'consul_maint', 'backup_dir'];

  function guide() {
    var open = state.guideOpen;
    if (open === null) open = state.hosts.length === 0;
    function jump(view, text) {
      return h('button', { type: 'button', class: 'btn ghost small', onclick: function () { switchView(view); } }, [text]);
    }
    function step(no, title, view, lines, extra) {
      return h('div', { class: 'guide-step' }, [
        h('div', { class: 'guide-title' }, [h('span', { class: 'guide-no' }, [no]), title, view ? jump(view, '去' + title.split(' ')[0]) : null]),
        h('ul', {}, lines.map(function (l) { return h('li', {}, [l]); })),
        extra || null
      ]);
    }
    var d = h('details', { class: 'card help' }, [
      h('summary', {}, ['📘 快速上手：接入主机 → 组件 →（可选）制品 → 任务（以 node_exporter 为例）']),
      step('1', '主机 接入 Agent', 'hosts', [
        '管理员在“主机”页选择目标机器架构，点“生成接入命令”，复制到目标机器用 root 执行。',
        'Agent 注册成功后主机自动出现在列表：IP 取服务端看到的来源地址，主机名、OS、ARCH 由 Agent 上报。',
        '接入令牌一次性使用，默认 15 分钟有效，每台机器单独生成一次。',
        '“同步 Consul”也能导入主机，但没装 Agent 的主机在任务下发时会被跳过。'
      ]),
      step('2', '组件 定义装什么、装到哪', 'components', [
        '组件示例提供「只执行命令」三种动作，与「任务」页的示例按钮一一对应：安装（exec_args=["install"]）、更新（["update"]，备份旧二进制并在启动失败时自动回滚）、卸载（["uninstall","-y"]，跳过交互确认）。三者都只需 script_url + 一个动作参数，Agent 下载 install.sh 后按参数执行，不再需要把整段 shell 写进 exec_args。',
        '任务页在组件升级模式下也提供「填入示例：安装组件 / 更新组件 / 卸载组件」，会选中对应组件；动作由组件自身配置决定。',
        '卸载示例不带服务名、health_cmd、version_cmd：exec 模式没有 stdin，因此传 -y；卸载后服务已不存在，保留健康检查会让任务必然失败。',
        '脚本模式同样提供「脚本安装 / 脚本更新 / 脚本卸载」三个示例：都只填 script_url，用 exec_args 区分动作（install / update / uninstall -y）。脚本更新会备份旧二进制并在启动失败时自动回滚；脚本卸载不带服务名与 health/version 检查，避免卸载后平台又去启服或做健康检查。',
        '点“填入示例：脚本安装”：部署模式 script，安装路径 /opt/monitor/exporter，服务名 node_exporter；高级配置 JSON 会预填真实可用的 script_url、exec_env、exec_timeout、health_cmd 与 version_cmd。',
        '示例 script_url 指向 https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh，目标机需要能访问 proxy.xiaonuo.live；不需要上传制品。',
        '适用 systemd 系统：CentOS/RHEL 7+、Rocky 8+、openEuler 20.03+、Ubuntu 16.04+、Debian 8+、SLES 12+/openSUSE 15+，脚本自动写 node_exporter.service。',
        'CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11：无 systemd，脚本自动写 /etc/init.d/node_exporter。',
        'agent.allow_exec_mode 默认开启：Agent 会下载安装脚本并以 Shell 执行（优先 bash，缺失时 sh）；如需禁止可在“设置”里关闭。',
        '高级配置 JSON 的 exec_args / exec_env / exec_timeout / work_dir 会直接控制脚本执行；例如用 exec_env 指定 NODE_EXPORTER_VERSION=1.8.2。',
        '安装脚本支持 install / update / uninstall / status 四个动作：install 安装或覆盖升级，update 会备份旧二进制并在启动失败时自动回滚，uninstall 停止并删除服务与文件（加 -y 跳过确认），status 查看安装状态与版本。',
        '需要平台备份/回滚时改用“填入二进制替换示例”：服务注册逻辑放在独立的 post_install 脚本文本框里，不再写进高级配置 JSON。',
        '脚本输出会记录在任务日志；登录目标机后也可以用 systemctl/service 查看服务状态。'
      ]),
      step('3', '制品（可选）上传安装脚本', 'artifacts', [
        '脚本示例已预填真实 script_url，通常可以跳过本页，直接去“任务”创建。',
        '如果不想依赖外链，删除组件高级 JSON 中的 script_url，再在这里上传 examples/node_exporter/install.sh：版本 latest，架构 all（通用脚本）。',
        '不需要另外上传 node_exporter 二进制：脚本会自动从 proxy 下载 iflyelf latest 静态二进制（amd64/arm64）。',
        '脚本用 uname -m 自动识别目标机架构；如果要固定架构，可在组件 exec_args 里传 amd64 / arm64。',
        '安装脚本是纯文本文件，直接上传即可；平台不会解压文件，也不需要 tar.gz。'
      ], codeBlock([
        '# 仓库里已经自带单文件安装脚本（内嵌 systemd unit 与 SysV init）',
        'ls -l examples/node_exporter/install.sh',
        '',
        '# 可选：先在目标机手动试跑（需要 root，会自动下载并安装）',
        'sudo bash examples/node_exporter/install.sh install',
        'sudo bash examples/node_exporter/install.sh update     # 升级，失败自动回滚',
        'sudo bash examples/node_exporter/install.sh status     # 查看状态与版本',
        'sudo bash examples/node_exporter/install.sh uninstall -y  # 卸载',
        '',
        '# 平台默认流程：组件高级 JSON 已用真实 script_url，无需上传制品',
        '# 备用流程：删除 script_url 后，在制品页上传 install.sh（版本 latest，架构 all）'
      ].join('\n'))),
      step('4', '任务 选择主机并执行', 'tasks', [
        '组件升级模式：点“填入示例：安装组件 / 更新组件 / 卸载组件”可直接选中对应示例组件，动作由组件的 script_url + exec_args 决定。',
        '组件选 node_exporter_cmd（安装）、node_exporter_update（更新）或 node_exporter_uninstall（卸载），版本选 latest，IP 段填 10.1.255.24-26 或 10.1.255.0/24。',
        '先点“预览”确认命中的主机数和分批，再点“创建任务”，最后在列表里点“启动”。',
        '想先装一台观察：策略选“金丝雀”、首批 1 台并取消“自动推进”，首批成功后任务暂停，确认无误再点“推进”。',
        '只想临时执行一条命令、不创建组件时，直接在任务页把“任务类型”切到“执行命令/脚本”，填脚本后启动；命令任务支持 stdin 与伪终端，适合交互式卸载。'
      ])
    ]);
    d.open = open;
    d.ontoggle = function () { state.guideOpen = d.open; };
    return d;
  }

  function switchView(v) {
    // 切换页签时关闭任务详情并停止轮询，避免后台定时器继续渲染旧弹窗。
    closeTaskDetail(true);
    stopAutoRefresh();
    state.view = v;
    state.err = '';
    if (v === 'settings') loadSettings();
    else if (v === 'components') loadComponents();
    else if (v === 'artifacts') loadArtifacts();
    else if (v === 'tasks') loadTasks();
    else if (v === 'password') render();
    else loadHosts();
  }

  function loginView() {
    var uEl, pEl;
    var doLogin = function () {
      if (state.loginBusy) return;
      state.err = '';
      state.loginBusy = true;
      api('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username: uEl.value, password: pEl.value })
      })
        .then(function (u) {
          state.user = u;
          state.loginBusy = false;
          state.loginName = '';
          state.err = '';
          loadHosts();
        })
        .catch(function (e) {
          state.loginBusy = false;
          state.loginName = uEl.value;
          state.err = e.message;
          render();
        });
    };
    var onKey = function (ev) {
      if (ev.key === 'Enter' || ev.keyCode === 13) {
        ev.preventDefault();
        doLogin();
      }
    };
    var card = h('div', { class: 'card login-card' }, [
      h('h2', {}, ['登录']),
      state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
      h('div', { class: 'field' }, [h('label', {}, ['用户名']),
        (uEl = h('input', { value: state.loginName || '', autocomplete: 'username' }, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['密码']),
        (pEl = h('input', { type: 'password', autocomplete: 'current-password' }, []))]),
      h('button', {
        class: 'btn', disabled: state.loginBusy ? 'disabled' : null,
        onclick: doLogin
      }, [state.loginBusy ? '登录中…' : '登录'])
    ]);
    uEl.onkeydown = onKey;
    pEl.onkeydown = onKey;
    // 登录页自动聚焦用户名；密码框按回车同样提交。
    setTimeout(function () {
      try {
        if (state.user) return;
        if (state.loginName) pEl.focus(); else uEl.focus();
      } catch (e) {}
    }, 0);
    return h('div', { class: 'login-wrap' }, [card]);
  }

  function hostsView() {
    var qEl;
    var doSearch = function () {
      state.q = qEl.value;
      state.hostPage = 1;
      loadHosts();
    };
    var toolbar = h('div', { class: 'toolbar' }, [
      (qEl = h('input', { value: state.q, placeholder: 'IP/主机名/业务组' }, [])),
      h('button', { class: 'btn', onclick: doSearch }, ['搜索']),
      state.q ? h('button', { class: 'btn ghost', onclick: function () { state.q = ''; state.hostPage = 1; loadHosts(); } }, ['清除']) : null,
      h('button', { class: 'btn ghost', onclick: syncHosts }, ['同步 Consul']),
      state.hostsLoading ? h('span', { class: 'muted' }, ['加载中…']) : null,
      h('span', { class: 'muted auto-refresh-tip' }, ['● 自动刷新中'])
    ]);
    qEl.onkeydown = function (ev) { if (ev.key === 'Enter') doSearch(); };

    var cols = ['IP', '主机名', 'OS', 'ARCH', '业务组', 'app_code', 'Agent', '来源'];
    if (isAdmin()) cols.push('操作');
    var head = h('tr', {}, cols.map(function (t) { return h('th', {}, [t]); }));
    var rows = state.hosts.map(function (x) {
      var cells = [
        h('td', {}, [x.ip]), h('td', {}, [x.hostname || '-']),
        h('td', {}, [x.os_type || x.os_family || '-']), h('td', {}, [x.arch || '-']),
        h('td', {}, [x.biz_group || '-']), h('td', {}, [x.app_code || '-']),
        h('td', {}, [
          h('span', { class: 'tag' }, [agentStatusText(x.agent_status)]),
          agentBuildLine(x),
          x.agent_update_status ? h('div', { class: 'muted' }, [
            '自升级：' + cmdStatusText(x.agent_update_status) +
            (x.agent_update_stage ? ' / ' + stageText(x.agent_update_stage) : '') +
            (x.agent_update_at ? ' · ' + fmtTime(x.agent_update_at) : '')
          ]) : null,
          x.agent_update_log ? h('details', { class: 'log-details' }, [
            h('summary', {}, ['自升级日志']),
            h('pre', { class: 'code log-pre' }, [x.agent_update_log])
          ]) : null
        ]),
        h('td', {}, [x.source])
      ];
      if (isAdmin()) {
        cells.push(h('td', {}, [h('div', { class: 'row-actions' }, [
          x.agent_id ? h('button', {
            class: 'btn ghost small', onclick: function () { upgradeAgents([x.id]); }
          }, ['升级 Agent']) : null,
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.editingHost = Object.assign({}, x);
              state.hostErr = '';
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn danger small', onclick: function () { deleteHost(x); }
          }, ['删除'])
        ])]));
      }
      return h('tr', {}, cells);
    });
    var empty = !state.hostsLoading && !state.hostErr && state.hosts.length === 0
      ? h('div', { class: 'muted empty' }, [
        state.q ? '没有匹配的主机。' : '还没有主机。在上方“接入主机”生成命令，到目标机器上用 root 执行即可。'
      ])
      : null;
    return h('div', {}, [
      isAdmin() ? enrollCard() : null,
      state.hostErr ? h('div', { class: 'card' }, [h('div', { class: 'err', role: 'alert' }, [state.hostErr])]) : null,
      isAdmin() ? agentUpdateCard() : null,
      isAdmin() ? hostForm() : null,
      h('div', { class: 'card' }, [
        toolbar,
        h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, rows)])]),
        empty,
        state.total > 0 ? hostsPager() : null
      ])
    ]);
  }

  function shortHash(s) {
    s = String(s || '');
    return s.length > 12 ? s.slice(0, 12) : s;
  }

  function agentBuildLine(x) {
    var text = x.agent_build_version || '';
    if (!text && x.agent_build_hash) text = 'sha256:' + shortHash(x.agent_build_hash);
    if (!text) return null;
    return h('div', {
      class: 'muted', title: x.agent_build_hash ? ('SHA256 ' + x.agent_build_hash) : ''
    }, ['构建 ' + text]);
  }

  // agentUpdateCard 自升级入口：服务端从 AGENT_BINARY_DIR 读取二进制并计算 SHA256，
  // Agent 下载校验后替换自身并重启；新版本心跳会自动确认成功。
  function agentUpdateCard() {
    var target = state.agentTargetVersion || '-';
    return h('div', { class: 'card' }, [
      h('h3', {}, ['Agent 自升级']),
      hint('服务端目标版本：' + target + '。旧 Agent 会收到 self_update 指令，下载与当前架构匹配的二进制、校验 SHA256、备份并替换后自动重启。' +
        '升级期间主机会进入维护窗口；新 Agent 心跳上报构建哈希后，指令自动标记成功。' +
        '自动升级：' + (state.agentAutoUpdate ? '已开启（心跳发现旧版本时自动下发）' : '未开启（可在“设置”页开启 agent.auto_update）')),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: function () { upgradeAgents(null); } }, ['升级全部旧 Agent']),
        h('span', { class: 'muted' }, ['只下发仍不是目标二进制哈希的 Agent；已有任务/指令在执行的主机会跳过。'])
      ])
    ]);
  }

  function upgradeAgents(hostIDs) {
    var single = hostIDs && hostIDs.length === 1;
    var msg = single
      ? '确定向这台主机下发 Agent 自升级吗？Agent 将下载新二进制并重启。'
      : '确定向全部旧 Agent 下发自升级吗？Agent 将下载新二进制并重启。';
    if (!confirm(msg)) return;
    var payload = single ? { host_ids: hostIDs } : { all_outdated: true };
    api('/api/agents/self-update', { method: 'POST', body: JSON.stringify(payload) })
      .then(function (d) {
        var out = '已下发 ' + d.enqueued + ' 台 Agent 自升级';
        if (d.skipped_count) out += '，跳过 ' + d.skipped_count + ' 台';
        if (d.skipped && d.skipped.length) {
          out += '\n\n' + d.skipped.slice(0, 8).map(function (s) {
            return (s.ip || s.host_id) + '：' + s.reason;
          }).join('\n');
          if (d.skipped_count > 8) out += '\n…';
        }
        alert(out);
        loadHosts();
      })
      .catch(function (e) { alert('下发自升级失败: ' + e.message); });
  }

  // hostsPager 服务端分页控件：主机可能达到几万台，页面只按当前页拉取。
  function hostsPager() {
    var pages = state.hostPages || 1;
    var jumpEl = h('input', {
      type: 'number', min: '1', max: String(pages), value: String(state.hostPage),
      style: 'width:76px', 'aria-label': '跳转页码'
    }, []);
    var doJump = function () {
      var p = parseInt(jumpEl.value, 10);
      if (!p || p < 1) p = 1;
      if (p > pages) p = pages;
      loadHosts(p);
    };
    jumpEl.onkeydown = function (ev) { if (ev.key === 'Enter') doJump(); };
    var sizeEl = h('select', {
      value: String(state.hostSize), 'aria-label': '每页条数'
    }, [20, 50, 100, 200, 500].map(function (n) {
      return h('option', { value: String(n) }, [n + ' 条/页']);
    }));
    sizeEl.onchange = function () {
      state.hostSize = parseInt(sizeEl.value, 10) || 50;
      state.hostPage = 1;
      loadHosts();
    };
    return h('div', { class: 'pager' }, [
      h('span', { class: 'muted' }, ['第 ' + state.hostPage + ' / ' + pages + ' 页 · 共 ' + state.total + ' 台']),
      h('button', {
        class: 'btn ghost small', disabled: state.hostPage <= 1 ? '' : null,
        onclick: function () { loadHosts(state.hostPage - 1); }
      }, ['上一页']),
      h('button', {
        class: 'btn ghost small', disabled: state.hostPage >= pages ? '' : null,
        onclick: function () { loadHosts(state.hostPage + 1); }
      }, ['下一页']),
      h('span', { class: 'muted' }, ['跳至']), jumpEl,
      h('button', { class: 'btn ghost small', onclick: doJump }, ['跳转']),
      h('span', { class: 'spacer' }, []),
      sizeEl
    ]);
  }

  // hostForm 编辑主机可人工维护字段；IP/OS/ARCH/Agent 状态保持 Agent 上报值。
  function hostForm() {
    var host = state.editingHost;
    if (!host) return null;
    var nameEl = h('input', { value: host.hostname || '' }, []);
    var accountEl = h('input', { value: host.account || '' }, []);
    var appCodeEl = h('input', { value: host.app_code || '' }, []);
    var bizEl = h('input', { value: host.biz_group || '' }, []);
    var deptEl = h('input', { value: host.department || '' }, []);
    var vendorEl = h('input', { value: host.vendor || '' }, []);
    var typeEl = h('input', { value: host.host_type || '' }, []);

    var save = function () {
      var payload = {
        hostname: nameEl.value,
        account: accountEl.value,
        app_code: appCodeEl.value,
        biz_group: bizEl.value,
        department: deptEl.value,
        vendor: vendorEl.value,
        host_type: typeEl.value
      };
      api('/api/hosts/' + host.id, { method: 'PUT', body: JSON.stringify(payload) })
        .then(function () {
          state.editingHost = null;
          state.hostErr = '';
          loadHosts();
        })
        .catch(function (e) { state.hostErr = '保存失败: ' + e.message; render(); });
    };

    return h('div', { class: 'card' }, [
      h('h3', {}, ['编辑主机 ' + host.ip]),
      hint('IP、OS、ARCH、Agent 状态由 Agent 上报，不能手工修改。Agent 重新注册会覆盖主机名；Consul 同步时，若 Consul 返回非空的业务字段，会覆盖对应字段。'),
      h('div', { class: 'grid2' }, [
        field('主机名 (hostname)', nameEl),
        field('账号 (account)', accountEl)
      ]),
      h('div', { class: 'grid2' }, [
        field('app_code', appCodeEl),
        field('业务组 (biz_group)', bizEl)
      ]),
      h('div', { class: 'grid2' }, [
        field('部门 (department)', deptEl),
        field('厂商 (vendor)', vendorEl)
      ]),
      h('div', { class: 'grid2' }, [
        field('主机类型 (host_type)', typeEl),
        h('div', {}, [])
      ]),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', {
          class: 'btn ghost', onclick: function () {
            state.editingHost = null;
            state.hostErr = '';
            render();
          }
        }, ['取消'])
      ])
    ]);
  }

  function deleteHost(host) {
    var msg = '确定删除主机 ' + host.ip + ' 吗？\n\n' +
      '删除后将级联删除该主机的任务/指令历史，并撤销 Agent 凭据，无法恢复。\n' +
      '如果主机存在未完成任务或指令，删除会被拒绝。';
    if (!confirm(msg)) return;
    api('/api/hosts/' + host.id, { method: 'DELETE' })
      .then(function () {
        state.hostErr = '';
        if (state.hosts.length === 1 && state.hostPage > 1) state.hostPage--;
        loadHosts();
      })
      .catch(function (e) { state.hostErr = '删除失败: ' + e.message; render(); });
  }

  var AGENT_ARCHES = [
    ['amd64', 'amd64（x86_64）'],
    ['arm64', 'arm64（aarch64，鲲鹏/飞腾等）'],
    ['arm', 'arm（32 位）'],
    ['386', '386（32 位 x86）'],
    ['amd64-legacy', 'amd64-legacy（CentOS 6 等老内核）'],
    ['386-legacy', '386-legacy（老内核 32 位）'],
    ['arm64-legacy', 'arm64-legacy（老内核 arm64）']
  ];

  // enrollCard 生成一次性接入命令，主机执行后自动注册到列表。
  function enrollCard() {
    var e = state.enroll;
    var archEl = h('select', { value: (e && e.arch) || 'amd64' },
      AGENT_ARCHES.map(function (a) { return h('option', { value: a[0] }, [a[1]]); }));
    var forceEl = h('input', { type: 'checkbox', checked: !!(e && e.force) }, []);
    return h('div', { class: 'card' }, [
      h('h3', {}, ['接入主机（安装 Agent）']),
      hint('主机信息来自 Agent 注册：IP 取服务端看到的来源地址，主机名、OS、架构、init 系统由 Agent 上报。所以装好客户端，主机就会带着 IP 出现在列表里。'),
      field('目标机器架构', archEl, '在目标机器执行 uname -m：x86_64 选 amd64，aarch64 选 arm64。镜像里默认只带构建架构的 Agent，选其他架构下载可能 404。'),
      field('强制重新注册', forceEl, '勾选后命令会带 --force，覆盖目标机已有 state.json 并重新注册。仅在换服务端、数据库重置或重新接入同一台机器时使用；普通重装请勿勾选。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          type: 'button', class: 'btn', onclick: function () {
            var arch = archEl.value;
            var force = forceEl.checked;
            api('/api/agent/enroll-token?arch=' + encodeURIComponent(arch) + (force ? '&force=1' : ''))
              .then(function (d) { state.enroll = { arch: arch, force: force, base: d.base_url, cmd: d.install_command, expires: d.expires_in, err: '' }; render(); })
              .catch(function (err) { state.enroll = { arch: arch, force: force, err: err.message }; render(); });
          }
        }, ['生成接入命令'])
      ]),
      e && e.err ? h('div', { class: 'err field', role: 'alert' }, [e.err]) : null,
      e && e.cmd ? h('div', {}, [
        e.base ? hint('安装命令使用地址：' + e.base + '。可在“设置”页修改 server.external_url，保存后立即生效；Nginx 域名场景填 https://your-domain。') : null,
        hint('在目标机器用 root 执行（令牌一次性使用，' + Math.round((e.expires || 900) / 60) + ' 分钟内有效，每台机器各生成一次）：'),
        codeBlock(e.cmd),
        e.force ? hint('当前为强制重新注册命令（含 --force）：会覆盖目标机现有 Agent 身份，旧 Agent 记录将不再被该主机使用。') : null,
        hint('执行完成后点上方“搜索”刷新列表。如果 IP 显示成了代理地址，检查服务端 server.trust_proxy 配置。'),
        hint('目标机已注册时，普通命令会保留原身份（本次令牌不会被使用）；需要覆盖重注册请勾选“强制重新注册”后重新生成。')
      ]) : null
    ]);
  }

  function settingsView() {
    var inputs = {};
    var groups = {};
    state.settings.forEach(function (s) { (groups[s.group] = groups[s.group] || []).push(s); });
    var sections = Object.keys(groups).map(function (g) {
      return h('div', { class: 'card' }, [h('h3', {}, [g])].concat(groups[g].map(function (s) {
        var inp;
        if (s.type === 'bool') {
          inp = h('select', {}, [h('option', { value: 'true' }, ['true']), h('option', { value: 'false' }, ['false'])]);
          inp.value = s.value || 'false';
        } else {
          inp = h('input', { value: s.value || '' }, []);
          if (s.secret) inp.type = 'password';
        }
        inputs[s.key] = inp;
        return h('div', { class: 'field' }, [h('label', {}, [s.label + '  (' + s.key + ')']), inp]);
      })));
    });
    var saveBtn = h('button', {
      class: 'btn', onclick: function () {
        var kv = {}; Object.keys(inputs).forEach(function (k) { kv[k] = inputs[k].value; });
        api('/api/settings', { method: 'PUT', body: JSON.stringify(kv) })
          .then(function () { alert('已保存'); loadSettings(); })
          .catch(function (e) { alert('保存失败: ' + e.message); });
      }
    }, ['保存设置']);
    return h('div', {}, sections.concat([h('div', { class: 'card' }, [saveBtn])]));
  }

  function simpleTable(cols, rows, cells) {
    var head = h('tr', {}, cols.map(function (t) { return h('th', {}, [t]); }));
    var body = rows.map(function (x) {
      return h('tr', {}, cells(x).map(function (c) { 
        // 支持 DOM 节点或字符串
        return h('td', {}, [typeof c === 'string' ? c : c]); 
      }));
    });
    return h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, body)])]);
  }

  // advText 把组件里属于“高级配置”的字段序列化到 textarea。
  function advText(c) {
    var adv = {};
    ADV_KEYS.forEach(function (k) {
      var v = c[k];
      if (v === undefined || v === null || v === '' || v === false) return;
      if (Array.isArray(v) && v.length === 0) return;
      adv[k] = v;
    });
    return Object.keys(adv).length ? JSON.stringify(adv, null, 2) : '';
  }

  function componentsView() {
    var c = state.editingComp || null;
    var tpl = c && c._tpl ? COMP_TEMPLATES.filter(function (t) { return t.key === c._tpl; })[0] : null;

    function fillTemplate(key) {
      var t = COMP_TEMPLATES.filter(function (x) { return x.key === key; })[0] || COMP_TEMPLATES[0];
      state.editingComp = Object.assign({ _tpl: t.key, enabled: true }, JSON.parse(JSON.stringify(t.comp)));
      state.err = '';
      render();
    }

    var form = null;
    if (c) {
      var nameEl = h('input', { value: c.name || '', placeholder: '如 node_exporter' }, []);
      if (c.id) nameEl.readOnly = true;
      var dispEl = h('input', { value: c.display_name || '', placeholder: '如 Node Exporter' }, []);
      var modeEl = h('select', { value: c.deploy_mode || 'replace' }, [
        h('option', { value: 'replace' }, ['replace（替换二进制）']),
        h('option', { value: 'script' }, ['script（下载并执行 Shell 脚本）']),
        h('option', { value: 'exec' }, ['exec（只执行命令）']),
        h('option', { value: 'replace_exec' }, ['replace_exec（替换后执行命令）'])
      ]);
      var binEl = h('input', { value: c.binary_name || '', placeholder: '如 node_exporter' }, []);
      var pathEl = h('input', { value: c.install_path || '', placeholder: '如 /opt/monitor/exporter/node_exporter' }, []);
      var svcEl = h('input', { value: c.service_name || '', placeholder: '如 node_exporter' }, []);
      var mgrEl = h('select', { value: c.service_mgr || 'auto' }, [
        h('option', { value: 'auto' }, ['auto（Agent 自动探测）']),
        h('option', { value: 'systemd' }, ['systemd']),
        h('option', { value: 'sysv' }, ['sysv'])
      ]);
      var osEl = h('input', { value: c.os_match || '', placeholder: '留空即可' }, []);
      var archEl = h('input', { value: c.arch_match || '', placeholder: '留空即可' }, []);
      var enabledEl = h('input', { type: 'checkbox', checked: c.enabled !== false }, []);
      var advEl = h('textarea', { rows: '10', placeholder: '{"script_url":"https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh","exec_args":["install"],"health_cmd":[["pgrep","-x","node_exporter"]],"version_cmd":[["/opt/monitor/exporter/node_exporter","--version"]]}' }, []);
      advEl.value = advText(c);
      var postScriptEl = h('textarea', {
        rows: '12',
        placeholder: '#!/bin/sh\n# post_install 脚本：Agent 会写入临时文件后用 bash/sh 执行'
      }, []);
      postScriptEl.value = c.post_install_script || '';

      var saveComp = function () {
        var base = c.id ? JSON.parse(JSON.stringify(c)) : {
          backup_dir: '/var/lib/upgrade-agent/backup', service_mgr: 'auto', deploy_mode: 'replace', enabled: true
        };
        delete base._tpl;
        base.name = nameEl.value.trim();
        base.display_name = dispEl.value.trim();
        base.deploy_mode = modeEl.value;
        base.binary_name = binEl.value.trim();
        base.install_path = pathEl.value.trim();
        base.service_name = svcEl.value.trim();
        base.service_mgr = mgrEl.value;
        base.os_match = osEl.value.trim();
        base.arch_match = archEl.value.trim();
        base.enabled = enabledEl.checked;

        // 高级配置以文本框为准：先移除旧值，确保删除 script_url 等字段后保存能生效。
        ADV_KEYS.forEach(function (k) { delete base[k]; });
        if (advEl.value.trim()) {
          var adv;
          try { adv = JSON.parse(advEl.value); } catch (e) {
            state.err = '高级配置 JSON 格式错误: ' + e.message;
            render();
            return;
          }
          if (!adv || typeof adv !== 'object' || Array.isArray(adv)) {
            state.err = '高级配置 JSON 必须是一个对象';
            render();
            return;
          }
          delete adv.id;
          delete adv._tpl;
          Object.assign(base, adv);
        }
        // post_install_script 只从独立文本框读取，避免高级 JSON 与文本框内容冲突。
        base.post_install_script = postScriptEl.value;

        var method = base.id ? 'PUT' : 'POST';
        var url = base.id ? '/api/components/' + base.id : '/api/components';
        api(url, { method: method, body: JSON.stringify(base) })
          .then(function () {
            state.editingComp = null;
            state.err = '';
            loadComponents();
          })
          .catch(function (e) { state.err = e.message; render(); });
      };

      form = h('div', { class: 'card' }, [
        h('h3', {}, [c.id ? '编辑组件' : '新建组件']),
        tpl ? hint('已填入示例：' + tpl.label + '。' + tpl.desc) : null,
        state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
        h('div', { class: 'grid2' }, [
          field('名称 (name)', nameEl, c.id ? '创建后不可修改。' : '唯一标识，建议小写字母、数字、下划线。'),
          field('显示名称 (display_name)', dispEl, '页面上展示的名字。')
        ]),
        h('div', { class: 'grid2' }, [
          field('部署模式 (deploy_mode)', modeEl, 'script / exec / replace_exec 由 agent.allow_exec_mode 控制（默认开启，可在“设置”里关闭）。script 与 exec 模式都可用高级 JSON 的 script_url 指定 HTTP(S) 脚本地址（需新版 Agent）；exec + script_url 时 exec_args 只写脚本参数（如 install）。'),
          field('服务名称 (service_name)', svcEl, 'systemd/sysv 服务名。首次安装时服务尚不存在，Agent 会跳过停服，安装后再启动。')
        ]),
        h('div', { class: 'grid2' }, [
          field('二进制名称 (binary_name)', binEl, '仅记录，当前版本不参与执行。'),
          field('安装路径 (install_path)', pathEl, 'replace 模式会替换到该路径，必须是白名单目录内的绝对路径；script 模式不使用该路径，仅作安装目录说明。')
        ]),
        h('div', { class: 'grid2' }, [
          field('服务管理器 (service_mgr)', mgrEl, '仅记录；Agent 按本机 init 系统自动选择。'),
          h('div', { class: 'field' }, [
            h('label', { class: 'check' }, [enabledEl, ' 启用 (enabled)']),
            hint('停用后不会参与任务匹配。')
          ])
        ]),
        h('div', { class: 'grid2' }, [
          field('OS 匹配 (os_match)', osEl, '仅记录，当前版本不参与匹配。'),
          field('架构匹配 (arch_match)', archEl, '仅记录，当前版本不参与匹配。')
        ]),
        field('高级配置 JSON（可选）', advEl, '支持 ' + ADV_KEYS.join('、') + '；命令是二维数组，例如 [["/bin/sh","-c","echo hi"]]。script 或 exec 模式可用 script_url 指定 HTTP(S) 脚本地址（设置后不再要求上传制品）；exec + script_url 时 exec_args 作为脚本参数传入，例如 ["install"]。post_install 脚本请填到下方单独文本框。'),
        field('post_install 脚本（可选）', postScriptEl, 'Agent 在 post_install 命令之后把脚本写入临时文件，并用 bash/sh 执行；失败会触发回滚。脚本不会写入高级配置 JSON。'),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: saveComp }, ['保存']),
          h('button', { class: 'btn ghost', onclick: function () { state.editingComp = null; state.err = ''; render(); } }, ['取消']),
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec'); } }, ['填入只执行命令（安装）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_update'); } }, ['填入只执行命令（更新）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_uninstall'); } }, ['填入只执行命令（卸载）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script'); } }, ['填入脚本安装示例']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_update'); } }, ['填入脚本更新示例']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_uninstall'); } }, ['填入脚本卸载示例']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_binary'); } }, ['填入二进制替换示例']) : null
        ])
      ]);
    }

    var toolbar = h('div', { class: 'toolbar' }, [
      isAdmin() ? h('button', {
        class: 'btn', onclick: function () {
          state.editingComp = { enabled: true };
          state.err = '';
          render();
        }
      }, ['新建组件']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec'); } }, ['填入示例：只执行命令（安装）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_update'); } }, ['填入示例：只执行命令（更新）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_uninstall'); } }, ['填入示例：只执行命令（卸载）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script'); } }, ['填入示例：脚本安装']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_update'); } }, ['填入示例：脚本更新']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_uninstall'); } }, ['填入示例：脚本卸载']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_binary'); } }, ['填入示例：二进制替换']) : null,
      h('span', { class: 'muted' }, ['共 ' + state.components.length + ' 个组件'])
    ]);

    var table = simpleTable(
      ['ID', '名称', '显示名称', '模式', '安装路径', '服务', '操作'],
      state.components,
      function (x) {
        var actions = isAdmin() ? h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.editingComp = Object.assign({}, x);
              state.err = '';
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn ghost small', onclick: function () {
              // 直接跳到任务页并按该组件筛选，删除前先看清引用情况。
              state.taskFilterComp = String(x.id);
              state.taskFilterKind = '';
              state.tasksSig = '';
              switchView('tasks');
            }
          }, ['相关任务']),
          h('button', {
            class: 'btn danger small', onclick: function () {
              // 先查引用：有任务引用时必须二次确认是否连带删除；
              // 运行中/暂停中的任务不能连带删除，需要先取消。
              api('/api/components/' + x.id + '/references')
                .then(function (ref) {
                  ref = ref || {};
                  var n = ref.task_count || 0;
                  if (!n) {
                    if (!confirm('确定删除组件 ' + x.name + ' 吗？')) return;
                    return api('/api/components/' + x.id, { method: 'DELETE' })
                      .then(function () {
                        if (String(state.taskFilterComp) === String(x.id)) { state.taskFilterComp = ''; state.tasksSig = ''; }
                        loadComponents();
                      })
                      .catch(function (e) { alert('删除失败: ' + e.message); });
                  }
                  if (ref.blocking_tasks > 0) {
                    alert('组件 ' + x.name + ' 被 ' + n + ' 个任务引用，其中 ' +
                      ref.blocking_tasks + ' 个正在运行或暂停。\n请先到「任务」页取消这些任务（可用“按组件筛选”快速定位），再删除组件。');
                    return;
                  }
                  if (ref.active_commands > 0) {
                    alert('引用该组件的任务仍有 ' + ref.active_commands + ' 条未完成指令，请先取消任务。');
                    return;
                  }
                  var names = (ref.tasks || []).slice(0, 5).map(function (t) { return '#' + t.id + ' ' + t.name + '（' + statusText(t.status) + '）'; });
                  var more = n > names.length ? '\n… 等共 ' + n + ' 个任务' : '';
                  var tip = '组件 ' + x.name + ' 被 ' + n + ' 个任务引用：\n' + names.join('\n') + more +
                    '\n\n删除组件会同时删除这些任务及其历史记录（批次、逐机状态、指令日志），且不可恢复。\n是否连带删除？';
                  if (!confirm(tip)) return;
                  // 二次确认：要求输入组件名，防止误点造成批量历史数据丢失。
                  var typed = prompt('不可恢复操作。请输入组件名 “' + x.name + '” 以确认连带删除 ' + n + ' 个引用任务：');
                  if (typed === null) return;
                  if (typed.trim() !== x.name) {
                    alert('组件名不匹配，已取消删除。');
                    return;
                  }
                  return api('/api/components/' + x.id + '?cascade_tasks=true', { method: 'DELETE' })
                    .then(function () {
                      alert('已删除组件及 ' + n + ' 个引用任务。');
                      if (String(state.taskFilterComp) === String(x.id)) { state.taskFilterComp = ''; state.tasksSig = ''; }
                      loadComponents();
                    })
                    .catch(function (e) { alert('删除失败: ' + e.message); });
                })
                .catch(function (e) { alert('查询引用失败: ' + e.message); });
            }
          }, ['删除'])
        ]) : '-';
        return [x.id, x.name, x.display_name || '-', x.deploy_mode, x.install_path || '-', x.service_name || '-', actions];
      }
    );

    var empty = state.components.length === 0
      ? h('div', { class: 'muted empty' }, ['还没有组件。点击“新建组件”，或直接点“填入示例：只执行命令（安装 / 更新 / 卸载）、脚本（安装 / 更新 / 卸载）或二进制替换”。'])
      : null;

    return h('div', {}, [form, h('div', { class: 'card' }, [toolbar, table, empty])]);
  }

  var ARTIFACT_ARCHES = [
    ['amd64', 'amd64（x86_64）'],
    ['arm64', 'arm64（aarch64）'],
    ['arm', 'arm（32 位）'],
    ['386', '386（32 位 x86）'],
    ['all', 'all（通用脚本）']
  ];

  function artifactsView() {
    var compMap = {};
    state.components.forEach(function (c) { compMap[c.id] = c; });

    var uploadForm = null;
    if (isAdmin()) {
      var compEl = h('select', {
        value: state.uploadComponent || '',
        onchange: function () {
          state.uploadComponent = compEl.value;
          var selected = state.components.filter(function (c) { return String(c.id) === String(compEl.value); })[0];
          if (selected && selected.deploy_mode === 'script') archEl.value = 'all';
          else if (archEl.value === 'all') archEl.value = 'amd64';
        }
      }, [h('option', { value: '' }, ['请选择组件'])].concat(
        state.components.map(function (c) {
          return h('option', { value: c.id }, [c.name + (c.display_name ? '（' + c.display_name + '）' : '')]);
        })
      ));
      var verEl = h('input', { placeholder: '如 latest 或 1.8.2' }, []);
      var archEl = h('select', { value: 'amd64' }, ARTIFACT_ARCHES.map(function (a) {
        return h('option', { value: a[0] }, [a[1]]);
      }));
      var selectedComp = state.components.filter(function (c) {
        return String(c.id) === String(state.uploadComponent || '');
      })[0];
      if (selectedComp && selectedComp.deploy_mode === 'script') archEl.value = 'all';
      var osEl = h('select', { value: '' }, [
        h('option', { value: '' }, ['通用（留空，匹配所有 OS）']),
        h('option', { value: 'rhel' }, ['rhel（CentOS/RHEL/麒麟/openEuler 等）']),
        h('option', { value: 'debian' }, ['debian（Debian/Ubuntu 等）']),
        h('option', { value: 'suse' }, ['suse（SLES/openSUSE）']),
        h('option', { value: 'alpine' }, ['alpine']),
        h('option', { value: 'linux' }, ['linux（未识别发行版）'])
      ]);
      var fileEl = h('input', { type: 'file' }, []);
      var innerEl = h('input', { placeholder: '一般留空' }, []);
      var notesEl = h('input', { placeholder: '可选' }, []);

      var doUpload = function () {
        if (!compEl.value || !verEl.value.trim() || !fileEl.files[0]) {
          state.err = '组件、版本和文件为必填项';
          render();
          return;
        }
        state.uploadComponent = compEl.value;
        var fd = new FormData();
        fd.append('file', fileEl.files[0]);
        fd.append('component_id', compEl.value);
        fd.append('version', verEl.value.trim());
        fd.append('arch', archEl.value);
        if (osEl.value) fd.append('os_family', osEl.value);
        if (innerEl.value.trim()) fd.append('inner_path', innerEl.value.trim());
        if (notesEl.value.trim()) fd.append('notes', notesEl.value.trim());

        fetch('/api/artifacts', { method: 'POST', body: fd })
          .then(function (r) {
            return r.json().then(function (j) {
              if (!r.ok || (j && j.code && j.code !== 0)) {
                throw new Error((j && j.message) || ('HTTP ' + r.status));
              }
              return j.data;
            });
          })
          .then(function () {
            alert('上传成功');
            state.err = '';
            fileEl.value = '';
            verEl.value = '';
            notesEl.value = '';
            loadArtifacts();
          })
          .catch(function (e) { state.err = e.message; render(); });
      };

      uploadForm = h('div', { class: 'card' }, [
        h('h3', {}, ['上传制品']),
        state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
        h('div', { class: 'grid2' }, [
          field('组件', compEl, '没有可选项时，先到“组件”页新建。'),
          field('版本', verEl, '创建任务时要选同一个版本；建议与下载的 release 对应，如 latest。')
        ]),
        h('div', { class: 'grid2' }, [
          field('架构 (arch)', archEl, '二进制必须和目标主机完全一致；Shell 脚本等与架构无关的制品选 all，可匹配所有主机。'),
          field('操作系统 (os_family)', osEl, '留空表示通用；填了以后只匹配同族主机。')
        ]),
        field('文件', fileEl, '脚本安装上传 examples/node_exporter/install.sh；二进制安装上传解压后的可执行文件。平台不解压 tar.gz。'),
        h('div', { class: 'grid2' }, [
          field('内部路径 (inner_path)', innerEl, '预留字段：当前版本不会解压或从包内取文件。'),
          field('备注 (notes)', notesEl, '可选，例如下载来源或变更说明。')
        ]),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: doUpload }, ['上传'])
        ])
      ]);
    }

    var table = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [
        h('span', { class: 'muted' }, ['共 ' + state.artifacts.length + ' 个制品（版本 × 架构，md5 校验）']),
        h('span', { class: 'muted' }, ['任务匹配 arch 相同或 all、os_family 相同或为空的制品'])
      ]),
      simpleTable(['ID', '组件', '版本', 'ARCH', 'OS', '文件名', '大小', 'MD5', '备注'], state.artifacts,
        function (x) {
          var c = compMap[x.component_id];
          return [
            x.id,
            c ? c.name : x.component_id,
            x.version,
            x.arch,
            x.os_family || '通用',
            x.filename || '-',
            x.size ? (x.size / 1024 / 1024).toFixed(2) + ' MB' : '-',
            (x.md5 || '').slice(0, 12),
            x.notes || '-'
          ];
        }),
      state.artifacts.length === 0 ? h('div', { class: 'muted empty' }, ['还没有制品。先在“组件”页创建 node_exporter，再上传下载好的二进制。']) : null
    ]);

    return h('div', {}, [uploadForm, table]);
  }

  function taskDetailModal() {
    var d = state.taskDetail;
    var mask = h('div', {
      id: 'task-detail-mask',
      class: 'modal-mask',
      onclick: function (e) { if (e.target === mask) closeTaskDetail(); },
      onscroll: function () { state.taskDetailScroll = mask.scrollTop; }
    }, []);
    var modal = h('div', { class: 'modal' }, []);
    mask.appendChild(modal);

    var title = '任务详情' + (d && d.id ? ' #' + d.id : (state.taskDetailId ? ' #' + state.taskDetailId : ''));
    if (d && d.name) title += ' · ' + d.name;
    var actions = d ? taskActions(d, function () {
      loadTaskDetail(d.id, state.taskDetailPage, true);
    }) : [];
    modal.appendChild(h('div', { class: 'modal-head' }, [
      h('h3', {}, [title]),
      h('div', { class: 'row-actions' }, actions.concat([
        h('button', { class: 'btn ghost small', onclick: function () { closeTaskDetail(); } }, ['关闭'])
      ]))
    ]));

    if (state.taskDetailErr) {
      modal.appendChild(h('div', { class: 'err field' }, ['加载失败: ' + state.taskDetailErr]));
      modal.appendChild(h('button', {
        class: 'btn ghost small',
        onclick: function () { loadTaskDetail(state.taskDetailId, state.taskDetailPage); }
      }, ['重试']));
    }
    if (!d) {
      modal.appendChild(h('div', { class: 'muted empty' }, [state.taskDetailLoading ? '加载中…' : '暂无数据']));
      return mask;
    }

    var total = d.total || 0;
    var success = d.success || 0;
    var failed = d.failed || 0;
    var done = d.done || 0;
    var active = d.active || 0;
    var percent = total ? Math.round(done * 100 / total) : 0;

    modal.appendChild(h('div', { class: 'detail-summary' }, [
      h('div', { class: 'detail-stat' }, ['状态: ', h('span', { class: 'tag status-' + d.status }, [statusText(d.status)])]),
      h('div', { class: 'detail-stat' }, ['类型: ', d.kind === 'command' ? '执行命令/脚本' : '组件升级']),
      d.kind === 'command' ? null : h('div', { class: 'detail-stat' }, ['组件: ', d.component_display_name || d.component_name || ('#' + d.component_id)]),
      h('div', { class: 'detail-stat' }, ['目标版本: ', d.version || '-']),
      h('div', { class: 'detail-stat' }, ['进度: ', done + ' / ' + total + '（' + percent + '%）']),
      h('div', { class: 'detail-stat' }, ['成功: ', success, ' 失败: ', failed, ' 执行中/待执行: ', active]),
      h('div', { class: 'detail-stat' }, ['创建人: ', d.created_by || '-']),
      h('div', { class: 'detail-stat' }, ['创建时间: ', fmtTime(d.created_at)])
    ]));
    if (d.kind === 'command' && d.command && d.command.script) {
      modal.appendChild(h('details', { class: 'log-details' }, [
        h('summary', {}, ['查看命令脚本']),
        h('pre', { class: 'code log-pre' }, [d.command.script])
      ]));
    }
    modal.appendChild(h('div', { class: 'progress', title: '完成 ' + done + ' / ' + total }, [
      h('div', { style: 'width:' + percent + '%' }, [])
    ]));
    modal.appendChild(hint('运行中/已暂停任务每 4 秒自动刷新执行日志，终态任务停止刷新；日志来自 Agent 阶段上报和最终执行结果。'));

    var batches = d.batches || [];
    modal.appendChild(h('h4', { class: 'detail-subtitle' }, ['批次进度']));
    modal.appendChild(batches.length ? simpleTable(
      ['批次', '状态', '总数', '成功', '失败', '已完成', '开始时间', '结束时间'],
      batches,
      function (b) {
        return ['第 ' + (b.batch_index + 1) + ' 批', batchStatusText(b.status), b.total, b.success,
          b.failed, b.done, fmtTime(b.started_at), fmtTime(b.finished_at)];
      }
    ) : h('div', { class: 'muted' }, ['暂无批次数据']));

    // 当前页主机的指令日志按 task_host_id 聚合；后端只返回当前页主机的日志。
    var cmdByHost = {};
    (d.commands || []).forEach(function (c) {
      if (!c.task_host_id) return;
      (cmdByHost[c.task_host_id] = cmdByHost[c.task_host_id] || []).push(c);
    });
    var hosts = d.hosts || [];
    modal.appendChild(h('h4', { class: 'detail-subtitle' }, ['逐机执行状态']));
    modal.appendChild(hosts.length ? simpleTable(
      ['主机', '批次', '状态', '当前阶段', '最新指令', '版本变化', '尝试', '开始时间', '结束时间', '执行日志', '操作'],
      hosts,
      function (x) {
        var cmds = cmdByHost[x.id] || [];
        var latest = cmds.length ? cmds[cmds.length - 1] : null;
        var logText = '';
        cmds.forEach(function (c) { if (c.log) logText += c.log; });
        if (!logText) logText = x.log || '';
        // 命令任务默认展开执行中/失败主机的日志，轮询刷新时用户无需反复点击。
        var expanded = state.taskDetailOpenLogs[x.id];
        if (expanded === undefined) {
          expanded = d.kind === 'command' &&
            (x.status === 'running' || x.status === 'failed' || x.status === 'timeout');
        }
        var logEl = logText
          ? h('details', {
              class: 'log-details',
              open: expanded ? 'open' : null,
              ontoggle: function (e) {
                state.taskDetailOpenLogs[x.id] = !!e.target.open;
              }
            }, [
              h('summary', {}, ['查看日志' + (latest ? '（' + cmdStatusText(latest.status) + '）' : '')]),
              h('pre', { class: 'code log-pre' }, [logText])
            ])
          : h('span', { class: 'muted' }, ['暂无日志']);
        var ver = (x.from_version || '-') + ' → ' + (x.to_version || '-');
        var stage = stageText(x.stage);
        if (x.exit_code) stage += '（exit ' + x.exit_code + '）';
        var hostAction = null;
        if (isAdmin()) {
          if (['failed', 'timeout', 'canceled', 'skipped', 'rolled_back'].indexOf(x.status) >= 0) {
            hostAction = h('button', {
              class: 'btn ghost small',
              onclick: function () { retryTask(d, [x.id], function () { loadTaskDetail(d.id, state.taskDetailPage, true); }); }
            }, ['重新执行']);
          } else if (x.status === 'running') {
            hostAction = h('button', {
              class: 'btn ghost small',
              onclick: function () { retryTask(d, [x.id], function () { loadTaskDetail(d.id, state.taskDetailPage, true); }); }
            }, ['强制重新执行']);
          }
        }
        return [
          h('div', {}, [h('div', {}, [x.ip]), h('div', { class: 'muted' }, [x.hostname || '-'])]),
          '第 ' + (x.batch + 1) + ' 批',
          h('span', { class: 'tag status-' + x.status }, [hostStatusText(x.status)]),
          stage,
          latest ? cmdStatusText(latest.status) : '-',
          ver,
          x.attempts || 0,
          fmtTime(x.started_at),
          fmtTime(x.finished_at),
          logEl,
          hostAction || h('span', { class: 'muted' }, ['-'])
        ];
      }
    ) : h('div', { class: 'muted empty' }, ['没有命中主机']));

    var pages = d.host_pages || 1;
    var page = d.host_page || 1;
    if (pages > 1 || total > 0) {
      modal.appendChild(h('div', { class: 'pager' }, [
        h('button', {
          class: 'btn ghost small',
          disabled: page <= 1 ? 'disabled' : null,
          onclick: function () { state.taskDetailScroll = 0; loadTaskDetail(d.id, page - 1); }
        }, ['上一页']),
        h('span', { class: 'muted' }, ['第 ' + page + ' / ' + pages + ' 页，共 ' + total + ' 台']),
        h('button', {
          class: 'btn ghost small',
          disabled: page >= pages ? 'disabled' : null,
          onclick: function () { state.taskDetailScroll = 0; loadTaskDetail(d.id, page + 1); }
        }, ['下一页'])
      ]));
    }
    return mask;
  }

  function tasksView() {
    var compMap = {};
    state.components.forEach(function (c) { compMap[c.id] = c; });

    // 表单值放在 state.taskForm 里：预览会触发 render，靠它保留用户输入。
    var f = state.taskForm || (state.taskForm = {
      kind: 'upgrade', name: '', component_id: '', version: '', ip_expr: '',
      script: '', interpreter: '', args: '', stdin: '', tty: false, timeout: '10m',
      mode: 'count', batch_size: '10', canary_count: '1', auto_promote: true
    });
    if (!f.kind) f.kind = 'upgrade';

    function versionsFor(cid) {
      var out = [];
      state.artifacts.forEach(function (a) {
        if (String(a.component_id) === String(cid) && out.indexOf(a.version) < 0) out.push(a.version);
      });
      return out;
    }

    var compEl = h('select', {
      value: f.component_id,
      onchange: function () {
        f.component_id = compEl.value;
        var comp = state.components.filter(function (x) { return String(x.id) === String(f.component_id); })[0];
        var vers = versionsFor(f.component_id);
        if (comp && comp.script_url) f.version = 'latest';
        else if (vers.length && vers.indexOf(f.version) < 0) f.version = '';
        state.preview = null;
        render();
      }
    }, [h('option', { value: '' }, ['请选择组件'])].concat(
      state.components.map(function (c) {
        return h('option', { value: c.id }, [c.name + (c.display_name ? '（' + c.display_name + '）' : '')]);
      })
    ));

    var versions = versionsFor(f.component_id);
    var datalistId = 'task-version-list';
    var verEl = h('input', {
      value: f.version, list: datalistId, placeholder: '如 latest',
      oninput: function () { f.version = verEl.value; }
    }, []);
    var datalist = h('datalist', { id: datalistId }, versions.map(function (v) {
      return h('option', { value: v }, []);
    }));

    var nameEl = h('input', {
      value: f.name, placeholder: '如 node_exporter 升级示例',
      oninput: function () { f.name = nameEl.value; }
    }, []);
    var ipEl = h('input', {
      value: f.ip_expr, placeholder: '如 10.1.255.24-26 或 10.1.255.0/24',
      oninput: function () { f.ip_expr = ipEl.value; }
    }, []);

    var modeEl = h('select', {
      value: f.mode,
      onchange: function () { f.mode = modeEl.value; state.preview = null; render(); }
    }, [
      h('option', { value: 'count' }, ['count（按数量分批）']),
      h('option', { value: 'canary' }, ['canary（金丝雀首批）']),
      h('option', { value: 'percent' }, ['percent（按百分比分批）'])
    ]);
    var kindEl = h('select', {
      value: f.kind,
      onchange: function () { f.kind = kindEl.value; state.preview = null; render(); }
    }, [
      h('option', { value: 'upgrade' }, ['组件升级（upgrade）']),
      h('option', { value: 'command' }, ['执行命令/脚本（command）'])
    ]);
    var scriptEl = h('textarea', {
      rows: '8',
      placeholder: '#!/bin/bash\n# 例如卸载旧监控（install.sh 支持 install/update/uninstall/status）：\nset -e\nurl="https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh"\nf=$(mktemp /tmp/node_exporter-install.XXXXXX.sh)\ncurl -fsSL "$url" -o "$f"\nbash "$f" uninstall\nrm -f "$f"\n',
      value: f.script || '',
      oninput: function () { f.script = scriptEl.value; }
    }, []);
    var interpEl = h('input', {
      value: f.interpreter || '', placeholder: '留空自动 bash/sh',
      oninput: function () { f.interpreter = interpEl.value; }
    }, []);
    var argsEl = h('input', {
      value: f.args || '', placeholder: '如 uninstall（多个用空格分隔）',
      oninput: function () { f.args = argsEl.value; }
    }, []);
    var stdinEl = h('input', {
      value: f.stdin || '', placeholder: '如 Y\\n（交互确认时必填）',
      oninput: function () { f.stdin = stdinEl.value; }
    }, []);
    var ttyEl = h('input', {
      type: 'checkbox', checked: !!f.tty,
      onchange: function () { f.tty = ttyEl.checked; }
    }, []);
    var timeoutEl = h('input', {
      value: f.timeout || '10m', placeholder: '如 10m、30m',
      oninput: function () { f.timeout = timeoutEl.value; }
    }, []);
    var batchEl = h('input', {
      type: 'number', min: '1', value: f.batch_size,
      oninput: function () { f.batch_size = batchEl.value; }
    }, []);
    var canaryEl = h('input', {
      type: 'number', min: '1', value: f.canary_count,
      oninput: function () { f.canary_count = canaryEl.value; }
    }, []);
    var autoEl = h('input', {
      type: 'checkbox', checked: !!f.auto_promote,
      onchange: function () { f.auto_promote = autoEl.checked; }
    }, []);

    function taskBody() {
      var body = {
        kind: f.kind,
        name: f.name.trim() || (f.kind === 'command'
          ? ('执行命令 ' + (new Date()).toLocaleString())
          : ('升级 ' + (compMap[f.component_id] ? compMap[f.component_id].name : '组件') + ' ' + f.version)),
        selector: { ip_expr: f.ip_expr.trim() },
        strategy: {
          mode: f.mode,
          batch_size: parseInt(f.batch_size, 10) || 0,
          canary_count: parseInt(f.canary_count, 10) || 0,
          auto_promote: !!f.auto_promote
        }
      };
      if (f.kind === 'command') {
        body.version = 'command';
        body.command = {
          script: f.script || '',
          interpreter: (f.interpreter || '').trim(),
          args: (f.args || '').split(/\s+/).filter(function (x) { return x; }),
          stdin: unescapeInput(f.stdin || ''),
          tty: !!f.tty,
          timeout: (f.timeout || '10m').trim()
        };
      } else {
        body.component_id = parseInt(f.component_id, 10) || 0;
        body.version = f.version.trim();
      }
      return body;
    }

    // unescapeInput 把用户输入的 \n、\t 转成真实字符，便于填写交互确认内容。
    function unescapeInput(s) {
      return s.replace(/\\n/g, '\n').replace(/\\t/g, '\t').replace(/\\r/g, '\r');
    }

    function checkForm() {
      if (f.kind === 'command') {
        if (!(f.script || '').trim()) {
          state.err = '请填写要执行的 Shell 脚本';
          state.preview = null;
          render();
          return false;
        }
        return true;
      }
      if (!f.component_id || !f.version.trim()) {
        state.err = '请先选择组件并填写目标版本（使用制品时需与制品版本一致；使用 script_url 时可填 latest）';
        state.preview = null;
        render();
        return false;
      }
      return true;
    }

    function doPreview() {
      state.err = '';
      if (!checkForm()) return;
      api('/api/tasks/preview', { method: 'POST', body: JSON.stringify(taskBody()) })
        .then(function (d) { state.preview = d; render(); })
        .catch(function (e) { state.preview = null; state.err = e.message; render(); });
    }

    function doCreate() {
      state.err = '';
      if (!checkForm()) return;
      api('/api/tasks', { method: 'POST', body: JSON.stringify(taskBody()) })
        .then(function (d) {
          state.preview = null;
          alert('任务已创建 #' + d.id + '，请在列表中点“启动”');
          loadTasks();
        })
        .catch(function (e) { state.err = e.message; render(); });
    }

    // 任务页示例与组件示例对齐：安装 / 更新 / 卸载分别对应组件页的三个 exec 示例。
    // 只填任务名与组件，动作由组件自身的 script_url + exec_args 决定。
    var UPGRADE_SAMPLES = {
      // 依次尝试新示例组件名，最后回退到旧版模板使用的 node_exporter / *_cmd。
      install: { comps: ['node_exporter_cmd', 'node_exporter_script', 'node_exporter'], name: '安装 node_exporter（示例）' },
      update: { comps: ['node_exporter_update', 'node_exporter_script_update'], name: '更新 node_exporter（示例）' },
      uninstall: { comps: ['node_exporter_uninstall', 'node_exporter_script_uninstall'], name: '卸载 node_exporter（示例）' }
    };

    function fillUpgradeSample(action) {
      var s = UPGRADE_SAMPLES[action];
      var comp = null;
      for (var i = 0; i < s.comps.length; i++) {
        comp = state.components.filter(function (x) { return x.name === s.comps[i]; })[0];
        if (comp) break;
      }
      if (!comp) {
        alert('请先到“组件”页点“填入示例：' +
          (action === 'install' ? '只执行命令（安装）或脚本安装'
            : action === 'update' ? '只执行命令（更新）或脚本更新' : '只执行命令（卸载）或脚本卸载') +
          '”并保存');
        return;
      }
      f.kind = 'upgrade';
      var vers = versionsFor(comp.id);
      f.component_id = String(comp.id);
      f.version = comp.script_url ? 'latest' : (vers[0] || 'latest');
      f.name = s.name;
      f.ip_expr = '10.1.255.24-26';
      f.mode = 'canary';
      f.batch_size = '10';
      f.canary_count = '1';
      f.auto_promote = false;
      state.preview = null;
      state.err = '';
      render();
    }

    // 命令任务示例：执行一次性脚本（kind=command），脚本内容随任务下发；
    // 组件示例则由组件 script_url + exec_args 决定动作。两者共用 installScript(action) 骨架。

    // fillCommandInstallSample 预填“安装/更新 node_exporter”示例：
    // install.sh 的 install 动作幂等，已安装时会覆盖升级，首次执行即安装。
    function fillCommandInstallSample() {
      f.kind = 'command';
      f.name = '安装/更新 node_exporter（示例）';
      f.ip_expr = '10.1.255.24-26';
      f.script = installScript('install');
      f.interpreter = '/bin/bash';
      f.stdin = '';
      f.tty = false;
      f.timeout = '30m';
      f.mode = 'count';
      f.batch_size = '10';
      f.auto_promote = true;
      state.preview = null;
      state.err = '';
      render();
    }

    // fillCommandUpdateSample 预填“更新 node_exporter”示例：
    // update 会备份旧二进制，启动失败自动回滚。
    function fillCommandUpdateSample() {
      f.kind = 'command';
      f.name = '更新 node_exporter（示例）';
      f.ip_expr = '10.1.255.24-26';
      f.script = installScript('update');
      f.interpreter = '/bin/bash';
      f.stdin = '';
      f.tty = false;
      f.timeout = '30m';
      f.mode = 'count';
      f.batch_size = '10';
      f.auto_promote = true;
      state.preview = null;
      state.err = '';
      render();
    }

    // fillCommandSample 预填“卸载旧监控”示例：
    // 先把 install.sh 下载到临时文件再执行，避免 script(1) 的 -c 字符串
    // 被 /bin/sh 解析而报 `Syntax error: "(" unexpected`（进程替换是 bash 语法）。
    // 卸载确认由平台“标准输入”提供（Y\n），脚本内部用 read 读取，无需 PTY。
    function fillCommandSample() {
      f.kind = 'command';
      f.name = '卸载旧监控（示例）';
      f.ip_expr = '10.1.255.24-26';
      f.script = installScript('uninstall');
      f.interpreter = '/bin/bash';
      f.stdin = 'Y\\n';
      f.tty = false; // 不需要 PTY：install.sh 的 uninstall 直接读取 stdin
      f.timeout = '30m';
      f.mode = 'count';
      f.batch_size = '10';
      f.auto_promote = true;
      state.preview = null;
      state.err = '';
      render();
    }

    var p = state.preview;
    var previewBox = null;
    if (p) {
      var batches = p.batches || [];
      var sample = p.sample_ips || [];
      previewBox = h('div', { class: 'preview' }, [
        h('div', { class: p.total_hosts ? '' : 'err' }, [
          '预览：命中 ' + p.total_hosts + ' 台主机，分 ' + batches.length + ' 批'
        ]),
        h('div', { class: 'hint' }, ['每批台数：' + (batches.length ? batches.join(' / ') : '-')]),
        h('div', { class: 'hint' }, ['示例 IP：' + (sample.join(', ') || '-') + (sample.length >= 20 ? ' …' : '')])
      ]);
    }

    var form = h('div', { class: 'card' }, [
      h('h3', {}, ['创建任务']),
      state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
      field('任务类型', kindEl, '组件升级走 script_url/制品与健康检查流程；执行命令/脚本用于卸载、清理等一次性操作。'),
      h('div', { class: 'grid2' }, [
        field('任务名', nameEl, '留空会自动生成。'),
        f.kind === 'command'
          ? field('IP 段', ipEl, '支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；留空表示所有主机。')
          : field('组件', compEl, state.components.length ? '' : '还没有组件，请先到“组件”页创建。')
      ]),
      f.kind === 'command' ? null : h('div', { class: 'grid2' }, [
        field('目标版本', verEl, '使用制品时从已上传制品中选择；使用 script_url 时可填 latest（仅作记录）。'),        field('IP 段', ipEl, '支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；留空表示所有主机。')
      ]),
      f.kind === 'command' ? null : datalist,
      f.kind === 'command' ? field('命令脚本', scriptEl,
        '脚本会以文件形式交给解释器执行，支持管道、重定向与进程替换。需要交互确认时填写标准输入，必要时勾选伪终端。') : null,
      f.kind === 'command' ? h('div', { class: 'grid2' }, [
        field('解释器', interpEl, '留空自动选择 bash（缺失回退 sh）；仅允许 bash/sh/dash/ksh/zsh。'),
        field('脚本参数', argsEl, '按空格分隔后作为 $1、$2… 传入。')
      ]) : null,
      f.kind === 'command' ? h('div', { class: 'grid2' }, [
        field('标准输入 (stdin)', stdinEl, '输入 \\n 表示回车；如交互确认填 Y\\n。'),
        field('超时', timeoutEl, '如 10m、30m；默认 10m。')
      ]) : null,
      f.kind === 'command' ? h('div', { class: 'field' }, [
        h('label', { class: 'check' }, [ttyEl, ' 分配伪终端（PTY）']),
        hint('脚本必须直接读写 /dev/tty 时勾选。命令脚本本身已用 script(1) 分配伪终端时无需勾选。')
      ]) : null,
      h('div', { class: 'grid2' }, [
        field('灰度策略', modeEl, 'count 每批固定台数；canary 先发首批再按批推进；percent 每批按百分比。'),
        field('每批台数 / 百分比', batchEl, f.mode === 'percent' ? '百分比模式填 1-100。' : '数量模式填每批台数。')
      ]),
      f.mode === 'canary' ? field('金丝雀首批台数', canaryEl, '首批成功后按“每批台数”继续推进。') : null,
      h('div', { class: 'field' }, [
        h('label', { class: 'check' }, [autoEl, ' 自动推进（auto_promote）']),
        hint('取消勾选后，每批完成会暂停，需要手动点“推进”。首次安装建议关闭。')
      ]),
      previewBox,
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn ghost', onclick: doPreview }, ['预览']),
        h('button', { class: 'btn', onclick: doCreate }, ['创建任务']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandInstallSample }, ['填入示例：安装脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('install'); } }, ['填入示例：安装组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandUpdateSample }, ['填入示例：更新脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('update'); } }, ['填入示例：更新组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandSample }, ['填入示例：卸载旧监控'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('uninstall'); } }, ['填入示例：卸载组件'])
      ]),
      hint('创建后任务处于 draft 状态，需要在下方列表点“启动”才会开始下发。')
    ]);

    var filterCompEl = h('select', {
      value: state.taskFilterComp,
      onchange: function () {
        state.taskFilterComp = filterCompEl.value;
        state.taskFilterKind = '';
        loadTasks();
      }
    }, [h('option', { value: '' }, ['全部组件'])].concat(
      state.components.map(function (c) {
        return h('option', { value: String(c.id) }, [c.name + (c.display_name ? '（' + c.display_name + '）' : '')]);
      })
    ));
    var filterKindEl = h('select', {
      value: state.taskFilterKind,
      onchange: function () {
        state.taskFilterKind = filterKindEl.value;
        loadTasks();
      }
    }, [
      h('option', { value: '' }, ['全部类型']),
      h('option', { value: 'upgrade' }, ['组件升级']),
      h('option', { value: 'command' }, ['执行命令'])
    ]);

    var list = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [
        h('span', { class: 'muted' }, ['共 ' + state.tasks.length + ' 个任务；点“详情日志”查看批次进度、逐机阶段和执行日志。']),
        h('label', { class: 'muted' }, ['按组件筛选：', filterCompEl]),
        h('label', { class: 'muted' }, ['类型：', filterKindEl]),
        state.taskFilterComp || state.taskFilterKind
          ? h('button', {
            class: 'btn ghost small', onclick: function () {
              state.taskFilterComp = '';
              state.taskFilterKind = '';
              loadTasks();
            }
          }, ['清除筛选'])
          : null,
        h('span', { class: 'muted auto-refresh-tip' }, ['● 自动刷新中'])
      ]),
      simpleTable(['ID', '类型', '名称', '组件/命令', '版本', '状态', '进度', '总数', '成功', '失败', '操作'], state.tasks,
        function (x) {
          var c = compMap[x.component_id];
          var acts = [detailBtn(x)].concat(isAdmin() ? taskActions(x) : []);
          var pct = x.total ? Math.round(x.success * 100 / x.total) : 0;
          var isCmd = x.kind === 'command';
          return [x.id,
            isCmd ? '命令' : '升级',
            h('a', {
              href: '#', class: 'link',
              onclick: function (e) { e.preventDefault(); openTaskDetail(x.id); }
            }, [x.name]),
            isCmd ? '执行脚本' : (c ? c.name : x.component_id), x.version,
            h('span', { class: 'tag status-' + x.status }, [statusText(x.status)]),
            h('div', { class: 'progress progress-sm', title: '成功 ' + x.success + ' / ' + x.total }, [
              h('div', { style: 'width:' + pct + '%' }, [])
            ]),
            x.total, x.success, x.failed,
            h('div', { class: 'row-actions' }, acts)];
        }),
      state.tasks.length === 0
        ? h('div', { class: 'muted empty' }, ['还没有任务。先上传制品，再用上面的表单创建。'])
        : null
    ]);

    return h('div', {}, [
      isAdmin() ? form : h('div', { class: 'card' }, [
        h('h3', {}, ['创建升级任务']),
        hint('当前账号不是管理员，只能查看任务列表；创建、启动、暂停等操作需要管理员权限。')
      ]),
      list
    ]);
  }

  function passwordView() {
    var oldEl, newEl, confirmEl;
    return h('div', { class: 'card' }, [
      h('h3', {}, ['修改密码']),
      state.err ? h('div', { class: 'err field' }, [state.err]) : null,
      h('div', { class: 'field' }, [h('label', {}, ['原密码']), (oldEl = h('input', { type: 'password' }, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['新密码']), (newEl = h('input', { type: 'password' }, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['确认新密码']), (confirmEl = h('input', { type: 'password' }, []))]),
      h('button', {
        class: 'btn', onclick: function () {
          if (newEl.value !== confirmEl.value) {
            state.err = '两次输入的新密码不一致';
            render();
            return;
          }
          if (newEl.value.length < 8) {
            state.err = '新密码长度不能少于 8 字符';
            render();
            return;
          }
          api('/api/auth/password', {
            method: 'PUT',
            body: JSON.stringify({ old_password: oldEl.value, new_password: newEl.value })
          })
            .then(function () {
              alert('密码修改成功');
              state.err = '';
              switchView('hosts');
            })
            .catch(function (e) { state.err = e.message; render(); });
        }
      }, ['提交'])
    ]);
  }

  function render() {
    app.innerHTML = '';
    if (!state.user) { app.appendChild(loginView()); return; }
    app.appendChild(topbar());
    var body;
    switch (state.view) {
      case 'settings': body = settingsView(); break;
      case 'components': body = componentsView(); break;
      case 'artifacts': body = artifactsView(); break;
      case 'tasks': body = tasksView(); break;
      case 'password': body = passwordView(); break;
      default: body = hostsView();
    }
    // 四个业务页都带上“快速上手”，主机为空时默认展开。
    if (state.view === 'hosts' || state.view === 'components' || state.view === 'artifacts' || state.view === 'tasks') {
      body = h('div', {}, [guide(), body]);
    }
    app.appendChild(h('div', { class: 'container' }, [tabs(), body]));
    if (state.taskDetailId) {
      app.appendChild(taskDetailModal());
      // 自动刷新会重建 DOM，恢复用户原来的滚动位置。
      var mask = document.getElementById('task-detail-mask');
      if (mask) mask.scrollTop = state.taskDetailScroll || 0;
    }
  }

  var hostsReqSeq = 0;
  var tasksReqSeq = 0;
  var taskDetailSeq = 0;
  var taskDetailTimer = null;
  var autoTimer = null;
  var autoBusy = false;
  function loadHosts(page, quiet) {
    if (!quiet) state.view = 'hosts';
    if (page != null) {
      var p = parseInt(page, 10);
      if (p > 0) state.hostPage = p;
    }
    if (!quiet) state.hostsLoading = true;
    var seq = ++hostsReqSeq;
    return api('/api/hosts?q=' + encodeURIComponent(state.q) +
        '&page=' + state.hostPage + '&size=' + state.hostSize)
      .then(function (d) {
        // 快速翻页/搜索时只接受最后一次请求，避免慢响应覆盖新结果。
        if (seq !== hostsReqSeq) return;
        if (quiet && state.view !== 'hosts') return;
        var sig = JSON.stringify([d.list || [], d.total || 0, d.page || 1,
          d.agent_target_version || '', !!d.agent_auto_update]);
        state.hosts = d.list || [];
        state.total = d.total || 0;
        state.hostPage = d.page || 1;
        state.hostSize = d.size || state.hostSize;
        state.hostPages = d.pages || Math.max(1, Math.ceil(state.total / state.hostSize));
        state.agentTargetVersion = d.agent_target_version || '';
        state.agentAutoUpdate = !!d.agent_auto_update;
        state.hostsLoading = false;
        // 自动刷新成功后清除上一次“加载主机失败”提示；操作类错误（删除/保存失败）
        // 保留给用户查看，不被轮询无声清掉。手动刷新则统一清空。
        var hadLoadErr = state.hostErr.indexOf('加载主机失败') === 0;
        if (!quiet || hadLoadErr) state.hostErr = '';
        // 自动刷新时数据没变化就不重建 DOM，避免打断用户滚动/选择文本。
        if (quiet && sig === state.hostsSig && !hadLoadErr) {
          scheduleAutoRefresh();
          return;
        }
        state.hostsSig = sig;
        var sy = window.scrollY;
        render();
        if (quiet) window.scrollTo(0, sy);
        scheduleAutoRefresh();
      })
      .catch(function (e) {
        if (seq !== hostsReqSeq) return;
        state.hostsLoading = false;
        state.hostErr = '加载主机失败: ' + e.message;
        // 自动刷新失败也要让用户看到；用滚动位置保持减少打扰。
        if (!quiet) {
          render();
        } else if (state.view === 'hosts') {
          var sy = window.scrollY;
          render();
          window.scrollTo(0, sy);
        }
        scheduleAutoRefresh();
      });
  }
  function loadSettings() {
    api('/api/settings').then(function (d) { state.settings = d || []; render(); }).catch(function (e) { alert(e.message); });
  }
  function loadComponents() {
    api('/api/components')
      .then(function (d) { state.components = d || []; render(); })
      .catch(function (e) { state.err = e.message; state.components = []; render(); });
  }
  function loadArtifacts() {
    Promise.all([api('/api/artifacts'), api('/api/components')])
      .then(function (rs) {
        state.artifacts = rs[0] || [];
        state.components = rs[1] || [];
        render();
      })
      .catch(function (e) {
        state.err = e.message;
        state.artifacts = [];
        render();
      });
  }
  function tasksURL() {
    var qs = [];
    if (state.taskFilterComp) qs.push('component_id=' + encodeURIComponent(state.taskFilterComp));
    if (state.taskFilterKind) qs.push('kind=' + encodeURIComponent(state.taskFilterKind));
    return '/api/tasks' + (qs.length ? '?' + qs.join('&') : '');
  }
  function loadTasks(quiet) {
    if (!quiet) state.view = 'tasks';
    var seq = ++tasksReqSeq;
    var tasksReq = api(tasksURL());
    // 自动刷新只需要任务状态；组件/制品变化很少，沿用已有数据，
    // 避免每 4 秒重复拉取两个较大的列表。
    var reqs = quiet
      ? [tasksReq, Promise.resolve(state.components), Promise.resolve(state.artifacts)]
      : [tasksReq, api('/api/components'), api('/api/artifacts')];
    return Promise.all(reqs)
      .then(function (rs) {
        if (seq !== tasksReqSeq) return;
        // 请求期间用户可能已切换页签，避免后台任务数据覆盖当前页面。
        if (state.view !== 'tasks') return;
        var sig = JSON.stringify([rs[0] || [], rs[1] || [], rs[2] || []]);
        state.tasks = rs[0] || [];
        state.components = rs[1] || [];
        state.artifacts = rs[2] || [];
        if (quiet && sig === state.tasksSig) {
          scheduleAutoRefresh();
          return;
        }
        state.tasksSig = sig;
        var sy = window.scrollY;
        render();
        if (quiet) window.scrollTo(0, sy);
        scheduleAutoRefresh();
      })
      .catch(function (e) {
        if (seq !== tasksReqSeq) return;
        if (!quiet) {
          state.err = e.message;
          state.tasks = [];
          render();
        }
        scheduleAutoRefresh();
      });
  }
  function stopTaskDetailTimer() {
    if (taskDetailTimer) {
      clearInterval(taskDetailTimer);
      taskDetailTimer = null;
    }
  }
  function closeTaskDetail(skipRender) {
    taskDetailSeq++;
    stopTaskDetailTimer();
    state.taskDetailId = null;
    state.taskDetail = null;
    state.taskDetailPage = 1;
    state.taskDetailLoading = false;
    state.taskDetailErr = '';
    state.taskDetailScroll = 0;
    state.taskDetailOpenLogs = {};
    state.taskDetailSig = '';
    if (!skipRender) render();
  }
  function openTaskDetail(id) {
    taskDetailSeq++;
    stopTaskDetailTimer();
    state.taskDetailId = id;
    state.taskDetail = null;
    state.taskDetailPage = 1;
    state.taskDetailLoading = true;
    state.taskDetailErr = '';
    state.taskDetailScroll = 0;
    state.taskDetailOpenLogs = {};
    state.taskDetailSig = '';
    render();
    loadTaskDetail(id, 1);
  }
  function loadTaskDetail(id, page, quiet) {
    stopTaskDetailTimer();
    var seq = ++taskDetailSeq;
    if (!quiet) state.taskDetailLoading = true;
    return api('/api/tasks/' + id + '?host_page=' + (page || 1) + '&host_size=50')
      .then(function (d) {
        if (seq !== taskDetailSeq || state.taskDetailId !== id) return;
        var sig = JSON.stringify(d);
        state.taskDetail = d;
        state.taskDetailPage = d.host_page || page || 1;
        state.taskDetailLoading = false;
        state.taskDetailErr = '';
        // 自动轮询且数据完全一致时不重建弹窗，保留用户展开的日志与滚动位置。
        if (!quiet || sig !== state.taskDetailSig) {
          state.taskDetailSig = sig;
          render();
        }
        scheduleTaskDetailRefresh();
      })
      .catch(function (e) {
        if (seq !== taskDetailSeq || state.taskDetailId !== id) return;
        state.taskDetailLoading = false;
        state.taskDetailErr = e.message;
        render();
        // 网络抖动后继续轮询，避免详情页停在旧阶段。
        if (state.taskDetail) scheduleTaskDetailRefresh();
      });
  }
  function scheduleTaskDetailRefresh() {
    stopTaskDetailTimer();
    var d = state.taskDetail;
    if (!state.taskDetailId || !d) return;
    // 仅运行中/已暂停任务需要轮询；终态任务不再请求。
    if (d.status !== 'running' && d.status !== 'paused') return;
    taskDetailTimer = setInterval(function () {
      if (!state.taskDetailId || document.hidden) return;
      loadTaskDetail(state.taskDetailId, state.taskDetailPage, true);
    }, 4000);
  }
  function detailBtn(x) {
    return h('button', {
      class: 'btn ghost small',
      onclick: function () { openTaskDetail(x.id); }
    }, ['详情日志']);
  }
  function taskActionBtn(x, label, action, cls, after) {
    return h('button', {
      class: 'btn small ' + (cls || 'ghost'),
      onclick: function () {
        if (action === 'cancel' && !confirm('确定取消任务 #' + x.id + ' 吗？未完成的指令会被取消。')) return;
        api('/api/tasks/' + x.id + '/' + action, { method: 'POST' })
          .then(function () {
            if (after) after();
            loadTasks();
          })
          .catch(function (e) { alert(e.message); });
      }
    }, [label]);
  }
  function deleteTaskBtn(x) {
    return h('button', {
      class: 'btn danger small',
      onclick: function () {
        if (!confirm('确定删除任务 #' + x.id + ' 吗？\n任务批次、逐机状态和指令日志会一并删除，且无法恢复。')) return;
        api('/api/tasks/' + x.id, { method: 'DELETE' })
          .then(function () {
            if (state.taskDetailId === x.id) closeTaskDetail(true);
            loadTasks();
          })
          .catch(function (e) { alert('删除失败: ' + e.message); });
      }
    }, ['删除']);
  }
  function taskActions(x, after) {
    var btns = [];
    if (x.status === 'draft') btns.push(taskActionBtn(x, '启动', 'start', '', after));
    if (x.status === 'running') btns.push(taskActionBtn(x, '暂停', 'pause', '', after));
    if (x.status === 'paused') btns.push(taskActionBtn(x, '推进', 'promote', '', after));
    if (x.status === 'running' || x.status === 'paused' ||
        x.status === 'failed' || x.status === 'canceled' || x.status === 'rolled_back') {
      btns.push(retryTaskBtn(x, null, after));
    }
    if (x.status === 'draft' || x.status === 'running' || x.status === 'paused') {
      btns.push(taskActionBtn(x, '取消', 'cancel', 'danger', after));
    }
    if (deletableTask(x)) btns.push(deleteTaskBtn(x));
    return btns;
  }

  // retryTaskBtn 重新执行失败/卡住主机。force=true 会取消旧的未结束指令，
  // 若旧 Agent 进程仍在执行，可能产生一次重复操作，因此必须二次确认。
  function retryTaskBtn(x, taskHostIDs, after) {
    var one = taskHostIDs && taskHostIDs.length === 1;
    return h('button', {
      class: 'btn ghost small',
      onclick: function () { retryTask(x, taskHostIDs, after); }
    }, [one ? '重新执行' : '重新执行失败/卡住主机']);
  }

  function retryTask(x, taskHostIDs, after) {
    var one = taskHostIDs && taskHostIDs.length === 1;
    var msg = one
      ? '确定重新执行这台主机吗？\n将取消该主机未结束的旧指令并重新下发。'
      : '确定重新执行任务 #' + x.id + ' 中失败/卡住的主机吗？\n已成功的主机会跳过；租约未到期的执行中主机也会跳过。';
    msg += '\n\n注意：若旧 Agent 进程仍在执行，可能出现一次重复操作。';
    if (!confirm(msg)) return;
    var body = { force: true };
    if (taskHostIDs) body.task_host_ids = taskHostIDs;
    api('/api/tasks/' + x.id + '/retry', { method: 'POST', body: JSON.stringify(body) })
      .then(function (d) {
        var out = '已重置 ' + d.reset + ' 台主机的执行状态';
        if (d.skipped_count) out += '，跳过 ' + d.skipped_count + ' 台';
        if (d.skipped && d.skipped.length) {
          out += '\n\n' + d.skipped.slice(0, 8).map(function (s) {
            return (s.ip || ('#' + s.task_host_id)) + '：' + s.reason;
          }).join('\n');
          if (d.skipped_count > 8) out += '\n…';
        }
        alert(out);
        if (after) after();
        loadTasks();
      })
      .catch(function (e) { alert('重新执行失败: ' + e.message); });
  }

  function syncHosts() {
    api('/api/hosts/sync', { method: 'POST' })
      .then(function (d) {
        var msg = '同步完成: 主机 ' + d.upserted + ' 台';
        if (d.deleted) msg += '，清理已移除主机 ' + d.deleted + ' 台';
        if (d.cleanup_skipped && d.cleanup_skipped !== '未开启自动清理') {
          msg += '\n未清理: ' + d.cleanup_skipped;
        }
        msg += '\n耗时: ' + d.duration;
        alert(msg);
        loadHosts();
      })
      .catch(function (e) { alert('同步失败: ' + e.message); });
  }
  // autoRefreshDelay 返回当前页面自动刷新间隔；0 表示不需要轮询。
  // 有升级动作时 4 秒刷新，空闲时 15 秒低频刷新，避免无意义的数据库压力。
  function autoRefreshDelay() {
    if (state.view === 'tasks') {
      var running = state.tasks.some(function (x) {
        return x.status === 'running' || x.status === 'paused';
      });
      return running ? 4000 : 15000;
    }
    if (state.view === 'hosts') {
      var active = state.hosts.some(function (x) {
        if (x.agent_status === 'upgrading' || x.agent_status === 'restarting') return true;
        return x.agent_update_status &&
          ['pending', 'dispatched', 'accepted', 'running'].indexOf(x.agent_update_status) >= 0;
      });
      return active ? 4000 : 15000;
    }
    return 0;
  }

  // isEditingField 判断用户是否正在输入。自动刷新会重建 DOM，
  // 此时跳过本轮，避免输入框失焦/光标跳回开头。
  function isEditingField() {
    var a = document.activeElement;
    if (!a) return false;
    var tag = (a.tagName || '').toUpperCase();
    if (tag === 'TEXTAREA' || a.isContentEditable) return true;
    if (tag === 'SELECT') return true;
    if (tag !== 'INPUT') return false;
    var type = (a.type || 'text').toLowerCase();
    // 勾选框/单选框/按钮聚焦时不影响刷新，避免用户点过复选框后轮询被永久暂停。
    return ['checkbox', 'radio', 'button', 'submit', 'reset', 'file', 'range', 'color'].indexOf(type) < 0;
  }

  function stopAutoRefresh() {
    if (autoTimer) {
      clearTimeout(autoTimer);
      autoTimer = null;
    }
  }

  function scheduleAutoRefresh(delay) {
    stopAutoRefresh();
    if (!state.user) return;
    var d = delay || autoRefreshDelay();
    if (!d) return;
    autoTimer = setTimeout(autoRefreshTick, d);
  }

  function autoRefreshTick() {
    autoTimer = null;
    // 后台标签页不轮询，等 visibilitychange 恢复时再启动，避免无意义的定时唤醒。
    if (document.hidden) return;
    if (!state.user || state.taskDetailId || state.editingHost) {
      scheduleAutoRefresh();
      return;
    }
    if (isEditingField()) {
      scheduleAutoRefresh();
      return;
    }
    if (autoBusy) {
      scheduleAutoRefresh(1000);
      return;
    }
    var p = null;
    if (state.view === 'tasks') p = loadTasks(true);
    else if (state.view === 'hosts') p = loadHosts(state.hostPage, true);
    if (!p) {
      scheduleAutoRefresh();
      return;
    }
    autoBusy = true;
    Promise.resolve(p).then(function () {
      autoBusy = false;
      scheduleAutoRefresh();
    }, function () {
      autoBusy = false;
      scheduleAutoRefresh();
    });
  }

  // 页面重新可见时立即刷新一次，后台标签页不轮询。
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      stopAutoRefresh();
      return;
    }
    if (state.user && state.view !== 'settings' && state.view !== 'components' &&
        state.view !== 'artifacts' && state.view !== 'password') {
      // 先清掉可能仍在排队的定时器，避免同一时刻触发两次请求。
      stopAutoRefresh();
      autoRefreshTick();
    }
  });

  function logout() {
    stopTaskDetailTimer();
    stopAutoRefresh();
    api('/api/auth/logout', { method: 'POST' }).then(function () {
      state.user = null;
      state.taskDetailId = null;
      state.taskDetail = null;
      state.hostsSig = '';
      state.tasksSig = '';
      state.taskDetailSig = '';
      render();
    });
  }

  // 启动：尝试拿当前用户
  api('/api/auth/userinfo').then(function (u) {
    state.user = u;
    loadHosts().then(scheduleAutoRefresh, scheduleAutoRefresh);
  }).catch(function () { render(); });
})();
