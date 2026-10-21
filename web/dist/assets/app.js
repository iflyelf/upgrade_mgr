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
    guideOpen: null, enroll: null, preview: null, editingComp: null, taskForm: null, uploadComponent: '' };

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
    success: '成功', failed: '失败', canceled: '已取消'
  };
  function statusText(s) { return TASK_STATUS[s] || s; }

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

  // post_install：首次安装时根据 init 系统写 systemd unit 或 SysV init 脚本；
  // 服务启动由 Agent 在 post_install 之后完成。已存在的文件不覆盖。
  var NODE_EXPORTER_POST_INSTALL = [
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

  // 组件模板：顶层字段对应表单，其余字段回填到“高级配置 JSON”。
  var COMP_TEMPLATES = [
    {
      key: 'node_exporter',
      label: 'node_exporter（二进制替换 + 自动注册 systemd/SysV）',
      desc: '把从 proxy 下载的 node_exporter 静态二进制作为制品上传。Agent 替换到安装路径后，post_install 会自动判断目标机 init：systemd 写 /etc/systemd/system/node_exporter.service 并 enable；CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11 等无 systemd 系统写 /etc/init.d/node_exporter，并用 chkconfig/update-rc.d/insserv 注册。服务名可以提前填；若机器上已有同名 unit/init 脚本则不会覆盖。',
      comp: {
        name: 'node_exporter', display_name: 'Node Exporter', deploy_mode: 'replace',
        binary_name: 'node_exporter',
        install_path: '/opt/monitor/exporter/node_exporter',
        service_name: 'node_exporter', service_mgr: 'auto', enabled: true,
        post_install: [['/bin/sh', '-c', NODE_EXPORTER_POST_INSTALL]],
        health_cmd: [['pgrep', '-x', 'node_exporter']],
        version_cmd: [['/opt/monitor/exporter/node_exporter', '--version']]
      }
    }
  ];
  var ADV_KEYS = ['exec_path', 'exec_args', 'exec_as_user', 'exec_timeout', 'exec_env', 'work_dir',
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
      h('summary', {}, ['📘 快速上手：接入主机 → 组件 → 制品 → 任务（以 node_exporter 为例）']),
      step('1', '主机 接入 Agent', 'hosts', [
        '管理员在“主机”页选择目标机器架构，点“生成接入命令”，复制到目标机器用 root 执行。',
        'Agent 注册成功后主机自动出现在列表：IP 取服务端看到的来源地址，主机名、OS、ARCH 由 Agent 上报。',
        '接入令牌一次性使用，默认 15 分钟有效，每台机器单独生成一次。',
        '“同步 Consul”也能导入主机，但没装 Agent 的主机在任务下发时会被跳过。'
      ]),
      step('2', '组件 定义装什么、装到哪', 'components', [
        '点“新建组件”，再点“填入示例”，各字段和高级 JSON 会自动填好：安装路径 /opt/monitor/exporter/node_exporter，服务名 node_exporter。',
        '适用 systemd 系统：CentOS/RHEL 7+、Rocky 8+、openEuler 20.03+、Ubuntu 16.04+、Debian 8+、SLES 12+/openSUSE 15+，写 node_exporter.service。',
        'CentOS/RHEL 6、Ubuntu 14.04、Debian 7、SLES 11：无 systemd，写 /etc/init.d/node_exporter。',
        'service_name 首次安装也可以填：服务还不存在时 Agent 会跳过停服，等 post_install 写好 unit 或 init 脚本后再启动。',
        '部署模式 replace 不需要打开 allow_exec_mode；如果改成 exec/replace_exec，需要管理员在“设置”里打开 agent.allow_exec_mode。',
        '命令不经过 shell，按 [["命令","参数1"],["命令2"]] 的二维数组填写；要用管道等语法请写成 ["/bin/sh","-c","..."]。'
      ]),
      step('3', '制品 上传某个版本的文件', 'artifacts', [
        '在能访问 GitHub 的机器上先下载静态编译的 node_exporter 二进制（走 proxy 加速），再到制品页上传。静态二进制不依赖目标机 Go/glibc，CentOS 6 等老内核也可直接运行。',
        '组件选 node_exporter，版本填 latest（创建任务时要选同一个版本），架构必须和主机完全一致：x86_64 选 amd64，aarch64 选 arm64。',
        '当前 iflyelf latest 提供 amd64/arm64 资产；arm/386 请自行静态编译后上传（平台制品支持这四种架构）。',
        '平台不会解压 tar.gz，请上传下载好的可执行文件本身；文件名不影响安装，安装后统一叫 node_exporter。'
      ], codeBlock([
        '# amd64（x86_64）',
        'wget -q -c --no-check-certificate -O /tmp/node_exporter \\',
        '  "https://proxy.xiaonuo.live/?url=https://github.com/iflyelf/node_exporter/releases/download/latest/node_exporter-linux-amd64"',
        '',
        '# arm64（aarch64）：把最后面的 amd64 换成 arm64',
        '# 下载完成后，在“制品”页上传 /tmp/node_exporter',
        '# 服务注册由 post_install 自动完成：systemd 写 unit，无 systemd 写 /etc/init.d/node_exporter'
      ].join('\n'))),
      step('4', '任务 选择主机并执行', 'tasks', [
        '组件选 node_exporter，版本选 latest，IP 段填 10.1.255.24-26 或 10.1.255.0/24。',
        '先点“预览”确认命中的主机数和分批，再点“创建任务”，最后在列表里点“启动”。',
        '想先装一台观察：策略选“金丝雀”、首批 1 台并取消“自动推进”，首批成功后任务暂停，确认无误再点“推进”。'
      ])
    ]);
    d.open = open;
    d.ontoggle = function () { state.guideOpen = d.open; };
    return d;
  }

  function switchView(v) {
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
    var card = h('div', { class: 'card login-card' }, [
      h('h2', {}, ['登录']),
      state.err ? h('div', { class: 'err field' }, [state.err]) : null,
      h('div', { class: 'field' }, [h('label', {}, ['用户名']), (uEl = h('input', { value: '' }, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['密码']), (pEl = h('input', { type: 'password' }, []))]),
      h('button', {
        class: 'btn', onclick: function () {
          api('/api/auth/login', { method: 'POST', body: JSON.stringify({ username: uEl.value, password: pEl.value }) })
            .then(function (u) { state.user = u; state.err = ''; loadHosts(); })
            .catch(function (e) { state.err = e.message; render(); });
        }
      }, ['登录'])
    ]);
    pEl && (pEl.type = 'password');
    return h('div', { class: 'login-wrap' }, [card]);
  }

  function hostsView() {
    var qEl;
    var toolbar = h('div', { class: 'toolbar' }, [
      (qEl = h('input', { value: state.q, placeholder: 'IP/主机名/业务组' }, [])),
      h('button', { class: 'btn', onclick: function () { state.q = qEl.value; loadHosts(); } }, ['搜索']),
      h('button', { class: 'btn', onclick: syncHosts }, ['同步 Consul']),
      h('span', { class: 'muted' }, ['共 ' + state.total + ' 台'])
    ]);
    var head = h('tr', {}, ['IP', '主机名', 'OS', 'ARCH', '业务组', 'app_code', 'Agent', '来源'].map(function (t) { return h('th', {}, [t]); }));
    var rows = state.hosts.map(function (x) {
      return h('tr', {}, [
        h('td', {}, [x.ip]), h('td', {}, [x.hostname || '-']),
        h('td', {}, [x.os_type || x.os_family || '-']), h('td', {}, [x.arch || '-']),
        h('td', {}, [x.biz_group || '-']), h('td', {}, [x.app_code || '-']),
        h('td', {}, [h('span', { class: 'tag' }, [x.agent_status || 'unknown'])]),
        h('td', {}, [x.source])
      ]);
    });
    var empty = state.hosts.length === 0
      ? h('div', { class: 'muted empty' }, ['还没有主机。在上方“接入主机”生成命令，到目标机器上用 root 执行即可。'])
      : null;
    return h('div', {}, [
      isAdmin() ? enrollCard() : null,
      h('div', { class: 'card' }, [toolbar, h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, rows)])]), empty])
    ]);
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
    var archEl = h('select', { value: (state.enroll && state.enroll.arch) || 'amd64' },
      AGENT_ARCHES.map(function (a) { return h('option', { value: a[0] }, [a[1]]); }));
    var e = state.enroll;
    return h('div', { class: 'card' }, [
      h('h3', {}, ['接入主机（安装 Agent）']),
      hint('主机信息来自 Agent 注册：IP 取服务端看到的来源地址，主机名、OS、架构、init 系统由 Agent 上报。所以装好客户端，主机就会带着 IP 出现在列表里。'),
      field('目标机器架构', archEl, '在目标机器执行 uname -m：x86_64 选 amd64，aarch64 选 arm64。镜像里默认只带构建架构的 Agent，选其他架构下载可能 404。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          type: 'button', class: 'btn', onclick: function () {
            var arch = archEl.value;
            api('/api/agent/enroll-token?arch=' + encodeURIComponent(arch))
              .then(function (d) { state.enroll = { arch: arch, cmd: d.install_command, expires: d.expires_in, err: '' }; render(); })
              .catch(function (err) { state.enroll = { arch: arch, err: err.message }; render(); });
          }
        }, ['生成接入命令'])
      ]),
      e && e.err ? h('div', { class: 'err field', role: 'alert' }, [e.err]) : null,
      e && e.cmd ? h('div', {}, [
        hint('在目标机器用 root 执行（令牌一次性使用，' + Math.round((e.expires || 900) / 60) + ' 分钟内有效，每台机器各生成一次）：'),
        codeBlock(e.cmd),
        hint('执行完成后点上方“搜索”刷新列表。如果 IP 显示成了代理地址，检查服务端 server.trust_proxy 配置。')
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

    function fillTemplate() {
      var t = COMP_TEMPLATES[0];
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
      var advEl = h('textarea', { rows: '10', placeholder: '{"post_install":[["/bin/sh","-c","..."]]}' }, []);
      advEl.value = advText(c);

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
          field('部署模式 (deploy_mode)', modeEl, 'exec / replace_exec 需要在“设置”里打开 agent.allow_exec_mode。'),
          field('服务名称 (service_name)', svcEl, 'systemd/sysv 服务名。首次安装时服务尚不存在，Agent 会跳过停服，安装后再启动。')
        ]),
        h('div', { class: 'grid2' }, [
          field('二进制名称 (binary_name)', binEl, '仅记录，当前版本不参与执行。'),
          field('安装路径 (install_path)', pathEl, '必须是绝对路径，且位于 /opt /usr/local /var/lib /tmp 等白名单目录；父目录不存在会自动创建。')
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
        field('高级配置 JSON（可选）', advEl, '支持 ' + ADV_KEYS.join('、') + '。命令是二维数组，例如 [["/bin/sh","-c","echo hi"]]。'),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: saveComp }, ['保存']),
          h('button', { class: 'btn ghost', onclick: function () { state.editingComp = null; state.err = ''; render(); } }, ['取消']),
          !c.id ? h('button', { class: 'btn ghost', onclick: fillTemplate }, ['填入示例：node_exporter']) : null
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
      isAdmin() ? h('button', { class: 'btn ghost', onclick: fillTemplate }, ['填入示例：node_exporter']) : null,
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
            class: 'btn danger small', onclick: function () {
              if (!confirm('确定删除组件 ' + x.name + ' 吗？')) return;
              api('/api/components/' + x.id, { method: 'DELETE' })
                .then(function () { loadComponents(); })
                .catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除'])
        ]) : '-';
        return [x.id, x.name, x.display_name || '-', x.deploy_mode, x.install_path || '-', x.service_name || '-', actions];
      }
    );

    var empty = state.components.length === 0
      ? h('div', { class: 'muted empty' }, ['还没有组件。点击“新建组件”，或直接点“填入示例：node_exporter”。'])
      : null;

    return h('div', {}, [form, h('div', { class: 'card' }, [toolbar, table, empty])]);
  }

  var ARTIFACT_ARCHES = [
    ['amd64', 'amd64（x86_64）'],
    ['arm64', 'arm64（aarch64）'],
    ['arm', 'arm（32 位）'],
    ['386', '386（32 位 x86）']
  ];

  function artifactsView() {
    var compMap = {};
    state.components.forEach(function (c) { compMap[c.id] = c; });

    var uploadForm = null;
    if (isAdmin()) {
      var compEl = h('select', {
        value: state.uploadComponent || '',
        onchange: function () { state.uploadComponent = compEl.value; }
      }, [h('option', { value: '' }, ['请选择组件'])].concat(
        state.components.map(function (c) {
          return h('option', { value: c.id }, [c.name + (c.display_name ? '（' + c.display_name + '）' : '')]);
        })
      ));
      var verEl = h('input', { placeholder: '如 latest 或 1.8.2' }, []);
      var archEl = h('select', { value: 'amd64' }, ARTIFACT_ARCHES.map(function (a) {
        return h('option', { value: a[0] }, [a[1]]);
      }));
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
          field('架构 (arch)', archEl, '必须和目标主机完全一致：x86_64 主机用 amd64，aarch64 主机用 arm64。'),
          field('操作系统 (os_family)', osEl, '留空表示通用；填了以后只匹配同族主机。')
        ]),
        field('文件', fileEl, '上传解压后的可执行文件（如 /tmp/node_exporter）。平台不解压 tar.gz。'),
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
        h('span', { class: 'muted' }, ['任务只匹配 arch 完全相同、os_family 相同或为空的制品'])
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

  function tasksView() {
    var compMap = {};
    state.components.forEach(function (c) { compMap[c.id] = c; });

    // 表单值放在 state.taskForm 里：预览会触发 render，靠它保留用户输入。
    var f = state.taskForm || (state.taskForm = {
      name: '', component_id: '', version: '', ip_expr: '',
      mode: 'count', batch_size: '10', canary_count: '1', auto_promote: true
    });

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
        var vers = versionsFor(f.component_id);
        if (vers.length && vers.indexOf(f.version) < 0) f.version = '';
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
      return {
        name: f.name.trim() || ('升级 ' + (compMap[f.component_id] ? compMap[f.component_id].name : '组件') + ' ' + f.version),
        component_id: parseInt(f.component_id, 10) || 0,
        version: f.version.trim(),
        selector: { ip_expr: f.ip_expr.trim() },
        strategy: {
          mode: f.mode,
          batch_size: parseInt(f.batch_size, 10) || 0,
          canary_count: parseInt(f.canary_count, 10) || 0,
          auto_promote: !!f.auto_promote
        }
      };
    }

    function checkForm() {
      if (!f.component_id || !f.version.trim()) {
        state.err = '请先选择组件并填写目标版本（版本要和制品的版本一致）';
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

    function fillSample() {
      var comp = state.components.filter(function (x) { return x.name === 'node_exporter'; })[0];
      if (!comp) {
        alert('请先到“组件”页点“填入示例：node_exporter”并保存');
        return;
      }
      var vers = versionsFor(comp.id);
      f.component_id = String(comp.id);
      f.version = vers[0] || 'latest';
      f.name = 'node_exporter 升级示例';
      f.ip_expr = '10.1.255.24-26';
      f.mode = 'canary';
      f.batch_size = '10';
      f.canary_count = '1';
      f.auto_promote = false;
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
      h('h3', {}, ['创建升级任务']),
      state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
      h('div', { class: 'grid2' }, [
        field('任务名', nameEl, '留空会自动生成。'),
        field('组件', compEl, state.components.length ? '' : '还没有组件，请先到“组件”页创建。')
      ]),
      h('div', { class: 'grid2' }, [
        field('目标版本', verEl, '从该组件已上传的制品中选择；下拉没有候选说明制品还没传。'),
        field('IP 段', ipEl, '支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；留空表示所有主机。')
      ]),
      datalist,
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
        h('button', { class: 'btn ghost', onclick: fillSample }, ['填入示例'])
      ]),
      hint('创建后任务处于 draft 状态，需要在下方列表点“启动”才会开始下发。')
    ]);

    function actBtn(x, label, action, cls) {
      return h('button', {
        class: 'btn small ' + (cls || 'ghost'),
        onclick: function () {
          if (action === 'cancel' && !confirm('确定取消任务 #' + x.id + ' 吗？')) return;
          api('/api/tasks/' + x.id + '/' + action, { method: 'POST' })
            .then(function () { loadTasks(); })
            .catch(function (e) { alert(e.message); });
        }
      }, [label]);
    }

    function taskActions(x) {
      var btns = [];
      if (x.status === 'draft') btns.push(actBtn(x, '启动', 'start', ''));
      if (x.status === 'running') btns.push(actBtn(x, '暂停', 'pause'));
      if (x.status === 'paused') btns.push(actBtn(x, '推进', 'promote', ''));
      if (x.status === 'draft' || x.status === 'running' || x.status === 'paused') btns.push(actBtn(x, '取消', 'cancel', 'danger'));
      return btns.length ? btns : [h('span', { class: 'muted' }, ['-'])];
    }

    var list = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [h('span', { class: 'muted' }, ['共 ' + state.tasks.length + ' 个任务'])]),
      simpleTable(['ID', '名称', '组件', '版本', '状态', '总数', '成功', '失败', '操作'], state.tasks,
        function (x) {
          var c = compMap[x.component_id];
          return [x.id, x.name, c ? c.name : x.component_id, x.version,
            h('span', { class: 'tag' }, [statusText(x.status)]),
            x.total, x.success, x.failed,
            isAdmin() ? h('div', { class: 'row-actions' }, taskActions(x)) : '-'];
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
  }

  function loadHosts() {
    state.view = 'hosts';
    api('/api/hosts?q=' + encodeURIComponent(state.q) + '&size=100')
      .then(function (d) { state.hosts = d.list || []; state.total = d.total || 0; render(); })
      .catch(function (e) { state.err = e.message; state.hosts = []; render(); });
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
  function loadTasks() {
    Promise.all([api('/api/tasks'), api('/api/components'), api('/api/artifacts')])
      .then(function (rs) {
        state.tasks = rs[0] || [];
        state.components = rs[1] || [];
        state.artifacts = rs[2] || [];
        render();
      })
      .catch(function (e) {
        state.err = e.message;
        state.tasks = [];
        render();
      });
  }
  function syncHosts() {
    api('/api/hosts/sync', { method: 'POST' })
      .then(function (d) { alert('同步完成: 主机 ' + d.upserted + ' 台，耗时 ' + d.duration); loadHosts(); })
      .catch(function (e) { alert('同步失败: ' + e.message); });
  }
  function logout() { api('/api/auth/logout', { method: 'POST' }).then(function () { state.user = null; render(); }); }

  // 启动：尝试拿当前用户
  api('/api/auth/userinfo').then(function (u) { state.user = u; loadHosts(); }).catch(function () { render(); });
})();
