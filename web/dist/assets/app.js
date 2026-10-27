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
  // 移动端浏览器地址栏/状态栏颜色跟随主题，避免暗色主题下顶部仍是一条浅色。
  var THEME_COLORS = { warm: '#f5efe6', cool: '#f4f6f8', dark: '#171a1f' };

  function syncThemeColor(t) {
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', THEME_COLORS[t] || THEME_COLORS.warm);
  }

  function setTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    try { localStorage.setItem('upgrade_mgr_theme', t); } catch (e) {}
    syncThemeColor(t);
    render();
  }
  function curTheme() { return document.documentElement.getAttribute('data-theme') || 'warm'; }

  // 首屏同步主题色（index.html 的内联脚本已设置 data-theme）。
  syncThemeColor(curTheme());

  function api(path, opts) {
    opts = opts || {};
    opts.headers = Object.assign({}, opts.headers || {});
    // FormData 由浏览器自动设置 multipart boundary，不能覆盖 Content-Type。
    if (!(typeof FormData !== 'undefined' && opts.body instanceof FormData)) {
      opts.headers['Content-Type'] = 'application/json';
    }
    return fetch(path, opts).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok || (j && j.code && j.code !== 0)) {
          throw new Error((j && j.message) || ('HTTP ' + r.status));
        }
        return j.data;
      });
    });
  }

  var state = { user: null, view: 'hosts', lang: (function () {
    try { return localStorage.getItem('lang') === 'en' ? 'en' : 'zh'; } catch (e) { return 'zh'; }
  })(), hosts: [], total: 0, q: '', settings: [], components: [], artifacts: [], tasks: [], err: '',
    hostPage: 1, hostSize: 50, hostPages: 1, hostErr: '', hostsLoading: false, editingHost: null,
    agentTargetVersion: '', agentAutoUpdate: false, agentStats: null,
    agentStatsAt: 0, agentStatsSig: '', agentUpdateActive: false,
    compQ: '', compPage: 1, compSize: 50,
    artifactQ: '', artifactFilterComp: '', artifactPage: 1, artifactSize: 50,
    artifactPages: 1, artifactTotal: 0,
    guideOpen: null, guideOS: '', quickstart: undefined, quickstartErr: '', enroll: null,
    enrollMode: 'one_time', automationList: null, automationErr: '', automationLoading: false, automationCreated: null,
    preview: null, editingComp: null, taskForm: null, uploadComponent: '',
    tunnelTest: null,
    taskDetailId: null, taskDetail: null, taskDetailPage: 1, taskDetailLoading: false, taskDetailErr: '', taskDetailScroll: 0,
    taskDetailOpenLogs: {},
    // 自动刷新：记录上一次数据指纹，状态没变化时不重建 DOM，避免滚动/焦点抖动。
    hostsSig: '', tasksSig: '', taskDetailSig: '', loginName: '', loginBusy: false,
    // 任务页按组件/类型/名称筛选与服务端分页。
    taskFilterComp: '', taskFilterKind: '', taskFilterStatus: '', taskFilterCreator: '', taskQ: '',
    taskPage: 1, taskSize: 50, taskPages: 1, taskTotal: 0, taskCreators: null,
    audit: null, auditTotal: 0, auditPage: 1, auditPages: 1, auditErr: '', auditLoading: false,
    auditFilters: { q: '', username: '', action: '', status: '', from: '', to: '' },
    // 外部文件白名单表格（页面增删行）
    extbin: null, extbinErr: '', extbinBusy: false, extbinTest: null,
    orgTab: 'users', org: null, orgErr: '', orgBusy: false, orgSel: null,
    orgSelTeam: null, orgTeamMembers: null, orgTeamPerms: null, orgNewField: null,
    flyiamUsers: null, flyiamQuery: '', flyiamErr: '', hostGroupOptions: [],
    hostFacets: null,
    extbinDraft: null, extbinHosts: null, extbinTTL: null,
    extbinAllowPrivate: null, extbinCIDRs: null, extbinQuery: '', extbinPage: 1,
    orgUserQ: '', orgUserForm: null, orgRoleForm: null, orgGroupForm: null,
    orgRoleQ: '', orgGroupQ: '',
    // 主机字段映射：orgMapForm 为编辑中的规则，orgMapScan 为扫描来源键的弹窗。
    orgMapForm: null, orgMapScan: null,
    // mgrSync 为「从 consul_mgr 同步」的服务组选择弹窗状态。
    mgrSync: null,
    helpOpen: null, cacheStatus: null, cacheErr: '', navOpen: null };

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
    // 容忍调用方把单个字符串/节点直接作为 children 传入，避免整页渲染崩溃。
    if (children == null) children = [];
    else if (!Array.isArray(children)) children = [children];
    children.forEach(function (c) {
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
  function copyBaseLabel() {
    return state.lang === 'en' ? 'Copy command' : '复制命令';
  }
  function copyText(text, btn) {
    var base = copyBaseLabel();
    function done(ok) {
      btn.textContent = ok
        ? (state.lang === 'en' ? 'Copied' : '已复制')
        : (state.lang === 'en' ? 'Copy failed; select manually' : '复制失败，请手动选中');
      setTimeout(function () { btn.textContent = base; }, 1500);
    }
    // 非 HTTPS 页面没有 navigator.clipboard，回退到 execCommand
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(fallbackCopy(text)); });
    } else {
      done(fallbackCopy(text));
    }
  }
  function codeBlock(text) {
    var label = copyBaseLabel();
    var btn = h('button', { type: 'button', class: 'btn ghost small copy-btn', 'aria-label': label }, [label]);
    btn.onclick = function () { copyText(text, btn); };
    return h('div', { class: 'code-wrap' }, [btn, h('pre', { class: 'code' }, [text])]);
  }
  function isAdmin() { return !!(state.user && state.user.IsAdmin); }
  function isSuperAdmin() {
    return !!(state.user && (state.user.Roles || []).indexOf('superadmin') >= 0);
  }

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

  // closeNavMenus 关闭所有顶栏下拉（点击页面其它区域或切换视图时调用）。
  function closeNavMenus() {
    var nodes = document.querySelectorAll('.nav-dropdown.open');
    for (var i = 0; i < nodes.length; i++) nodes[i].classList.remove('open');
    state.navOpen = null;
  }
  document.addEventListener('click', function () { closeNavMenus(); });

  // navLink 生成顶栏一级菜单项。
  function navLink(view, label, activeView) {
    var active = (activeView || state.view) === view;
    return h('button', {
      type: 'button', class: 'nav-link' + (active ? ' active' : ''),
      'aria-current': active ? 'page' : null,
      onclick: function (e) { e.stopPropagation(); closeNavMenus(); switchView(view); }
    }, [label]);
  }

  // navDropdown 生成带下拉子菜单的顶栏菜单。
  // items: [{label, onclick, active}]；parentActive 为 true 时父菜单高亮。
  function navDropdown(label, items, parentActive) {
    // 打开状态保存在 state.navOpen：列表自动刷新重建 DOM 时保持展开，
    // 否则用户刚展开菜单就被下一次刷新关闭，点击必然落空。
    var isOpen = state.navOpen === label;
    var wrap = h('div', { class: 'nav-dropdown' + (isOpen ? ' open' : '') }, []);
    var btn = h('button', {
      type: 'button', class: 'nav-link' + (parentActive ? ' active' : ''),
      'aria-haspopup': 'menu', 'aria-expanded': isOpen ? 'true' : 'false',
      onclick: function (e) {
        e.stopPropagation();
        var open = state.navOpen === label;
        closeNavMenus();
        if (!open) {
          state.navOpen = label;
          wrap.classList.add('open');
          btn.setAttribute('aria-expanded', 'true');
        }
      }
    }, [label, h('span', { class: 'caret' }, ['▾'])]);
    var menu = h('div', { class: 'nav-menu', role: 'menu' },
      items.map(function (it) {
        return h('button', {
          type: 'button', role: 'menuitem',
          class: 'nav-menu-item' + (it.active ? ' active' : ''),
          onclick: function (e) {
            e.stopPropagation();
            closeNavMenus();
            if (typeof it.onclick === 'function') it.onclick();
          }
        }, [it.label]);
      }));
    wrap.appendChild(btn);
    wrap.appendChild(menu);
    wrap.addEventListener('click', function (e) { e.stopPropagation(); });
    return wrap;
  }

  // avatarInitial 取显示名/用户名首字符作为头像。
  function avatarInitial(name) {
    var s = String(name || '?').trim();
    return s ? s.slice(0, 1).toUpperCase() : '?';
  }

  function topbar() {
    var orgTabs = [
      ['users', '用户管理'], ['fields', '用户字段'], ['hostfields', '主机字段'],
      ['hostmaps', '主机字段映射'],
      ['teams', '团队管理'], ['roles', '角色管理'], ['groups', '服务组'],
      ['depts', '组织架构'], ['flyiam', 'FlyIAM 人员']
    ];
    var orgActive = state.view === 'org';
    var opsItems = [];
    if (isAdmin() || isSuperAdmin()) {
      opsItems.push({ label: '审计日志', active: state.view === 'audit', onclick: function () { switchView('audit'); } });
    }
    // 外部文件白名单与系统设置是全局配置，仅 superadmin 可修改（后端同样校验）。
    if (isSuperAdmin()) {
      opsItems.push({ label: '外部文件代缓存', active: state.view === 'extbin', onclick: function () { switchView('extbin'); } });
      opsItems.push({ label: '系统设置', active: state.view === 'settings', onclick: function () { switchView('settings'); } });
    }
    var helpItems = [
      { label: '📘 快速上手', onclick: function () { openHelp('guide'); } },
      { label: '🖥️ 接入主机示例', onclick: function () { openHelp('enroll'); } }
    ];

    return h('header', { class: 'topbar' }, [
      h('div', { class: 'brand' }, [h('span', { class: 'brand-logo' }, ['🚀']), h('span', { class: 'brand-text' }, ['升级控制平台'])]),
      h('nav', { class: 'main-nav', 'aria-label': '主导航' }, [
        navLink('hosts', '主机'),
        navLink('components', '组件'),
        navLink('artifacts', '制品'),
        navLink('tasks', '任务'),
        orgActive || isAdmin() || isSuperAdmin() ? navDropdown('人员组织', orgTabs.map(function (t) {
          return {
            label: t[1], active: orgActive && state.orgTab === t[0],
            onclick: function () {
              state.orgTab = t[0];
              state.orgSel = null;
              state.orgSelTeam = null;
              state.orgUserForm = null;
              state.orgRoleForm = null;
              state.orgGroupForm = null;
              // 「用户字段」与「主机字段」共用该状态，切换页签必须清空。
              state.orgNewField = null;
              switchView('org');
            }
          };
        }), orgActive) : null,
        opsItems.length ? navDropdown('运维', opsItems, state.view === 'extbin' || state.view === 'audit' || state.view === 'settings') : null,
        navDropdown('帮助', helpItems, false)
      ]),
      h('div', { class: 'topbar-actions' }, [
        themeSwitcher(),
        state.user ? h('button', {
          class: 'icon-btn', title: 'Switch language / 切换语言',
          onclick: function (e) { e.stopPropagation(); toggleLang(); }
        }, [state.lang === 'en' ? '中文' : 'EN']) : null,
        state.user ? userMenu() : null
      ])
    ]);
  }

  // userMenu 用户头像下拉：修改密码 / 退出登录。
  function userMenu() {
    var name = state.user.DisplayName || state.user.Username || '用户';
    var isOpen = state.navOpen === '__user__';
    var wrap = h('div', { class: 'nav-dropdown user-dropdown' + (isOpen ? ' open' : '') }, []);
    var btn = h('button', {
      type: 'button', class: 'user-chip',
      'aria-haspopup': 'menu', 'aria-expanded': isOpen ? 'true' : 'false',
      onclick: function (e) {
        e.stopPropagation();
        var open = state.navOpen === '__user__';
        closeNavMenus();
        if (!open) { state.navOpen = '__user__'; wrap.classList.add('open'); btn.setAttribute('aria-expanded', 'true'); }
      }
    }, [
      h('span', { class: 'avatar' }, [avatarInitial(name)]),
      h('span', { class: 'user-name' }, [name]),
      h('span', { class: 'caret' }, ['▾'])
    ]);
    // 用户菜单同时作为移动端入口：桌面下拉在窄屏隐藏，这里补齐帮助/运维入口。
    var items = [
      { label: '📘 快速上手', run: function () { openHelp('guide'); } },
      { label: '🖥️ 接入主机示例', run: function () { openHelp('enroll'); }, role: 'admin' },
      { label: '外部文件代缓存', run: function () { switchView('extbin'); }, role: 'superadmin' },
      { label: '审计日志', run: function () { switchView('audit'); }, role: 'admin' },
      { label: '系统设置', run: function () { switchView('settings'); }, role: 'superadmin' },
      { label: '修改密码', run: function () { switchView('password'); } }
    ];
    var itemNodes = items.filter(function (it) {
      if (it.role === 'admin') return isAdmin() || isSuperAdmin();
      if (it.role === 'superadmin') return isSuperAdmin();
      return true;
    }).map(function (it) {
      return h('button', {
        type: 'button', role: 'menuitem', class: 'nav-menu-item',
        onclick: function (e) { e.stopPropagation(); closeNavMenus(); it.run(); }
      }, [it.label]);
    });
    itemNodes.push(h('button', {
      type: 'button', role: 'menuitem', class: 'nav-menu-item danger',
      onclick: function (e) { e.stopPropagation(); closeNavMenus(); logout(); }
    }, ['退出登录']));
    var menu = h('div', { class: 'nav-menu right', role: 'menu' }, [
      h('div', { class: 'nav-menu-head' }, [
        h('div', { class: 'user-name' }, [name]),
        h('div', { class: 'muted' }, [state.user.Username || ''])
      ])
    ].concat(itemNodes));
    wrap.appendChild(btn);
    wrap.appendChild(menu);
    wrap.addEventListener('click', function (e) { e.stopPropagation(); });
    return wrap;
  }

  // tabs 兼容旧调用：移动端仍使用横向页签（在 render 中渲染）。
  function tabs() {
    var items = [['hosts', '主机'], ['components', '组件'], ['artifacts', '制品'], ['tasks', '任务']];
    if (isAdmin() || isSuperAdmin()) {
      items.push(['org', '人员组织'], ['audit', '审计日志']);
    }
    // 全局配置页仅 superadmin 可见。
    if (isSuperAdmin()) items.push(['extbin', '外部文件'], ['settings', '设置']);
    return h('div', { class: 'tabs mobile-tabs', role: 'tablist' }, items.map(function (it) {
      return h('div', {
        class: 'tab' + (state.view === it[0] ? ' active' : ''),
        role: 'tab',
        'aria-selected': state.view === it[0] ? 'true' : 'false',
        tabindex: '0',
        onclick: function () { switchView(it[0]); },
        onkeydown: function (e) {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); switchView(it[0]); }
        }
      }, [it[1]]);
    }).concat([
      h('div', {
        class: 'tab', role: 'tab', tabindex: '0',
        onclick: function () { openHelp('guide'); },
        onkeydown: function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openHelp('guide'); } }
      }, ['帮助'])
    ]));
  }

  // openHelp 打开帮助弹窗：quickstart（快速上手）或 enroll（接入主机示例）。
  // 接入主机示例同时保留在帮助菜单；主机页顶部也直接展示完整接入卡片。
  function openHelp(kind) {
    if (kind === 'enroll' && !isAdmin()) {
      alert('仅管理员可生成接入命令');
      return;
    }
    state.helpOpen = kind;
    if (kind === 'guide') state.guideOpen = true;
    render();
  }

  // helpModal 帮助弹窗。enroll 复用 enrollCard；guide 复用快速上手内容。
  function helpModal() {
    var kind = state.helpOpen === 'enroll' ? 'enroll' : 'guide';
    var mask = h('div', {
      id: 'help-modal-mask', class: 'modal-mask',
      onclick: function (e) { if (e.target === mask) { state.helpOpen = null; render(); } }
    }, []);
    var title = kind === 'enroll' ? '接入主机示例' : '快速上手';
    var body = kind === 'enroll' ? enrollCard() : guide();
    // 弹窗内也提供切换：移动端从「帮助」页签进入时，可一键切到接入示例。
    var switchBar = h('div', { class: 'tabs', style: 'margin-bottom:12px' }, [
      h('div', {
        class: 'tab' + (kind === 'guide' ? ' active' : ''),
        role: 'tab', tabindex: '0', 'aria-selected': kind === 'guide' ? 'true' : 'false',
        onclick: function () { state.helpOpen = 'guide'; render(); },
        onkeydown: function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); state.helpOpen = 'guide'; render(); } }
      }, ['快速上手']),
      isAdmin() ? h('div', {
        class: 'tab' + (kind === 'enroll' ? ' active' : ''),
        role: 'tab', tabindex: '0', 'aria-selected': kind === 'enroll' ? 'true' : 'false',
        onclick: function () { state.helpOpen = 'enroll'; render(); },
        onkeydown: function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); state.helpOpen = 'enroll'; render(); } }
      }, ['接入主机示例']) : null
    ]);
    var modal = h('div', { class: 'modal help-modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': title }, [
      h('div', { class: 'modal-head' }, [
        h('h3', {}, [title]),
        h('button', {
          class: 'btn ghost small', onclick: function () { state.helpOpen = null; render(); }
        }, ['关闭'])
      ]),
      switchBar,
      body
    ]);
    mask.appendChild(modal);
    return mask;
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
  var INSTALL_SCRIPT_PLATFORM_URL = '/api/external/binary/node-exporter-install.sh';

  // WINDOWS_INSTALL_SCRIPT_URL：windows_exporter 安装脚本（PowerShell，经 proxy 代理）。
  // 与 Linux 的 install.sh 对齐：install / update / uninstall / status 四个动作。
  var WINDOWS_INSTALL_SCRIPT_URL = 'https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/windows_exporter/install.ps1';

  // 平台代缓存的外部文件（控制台预先下载，主机用 http 拉取）：
  // Windows Server 2008 R2 的 PowerShell 2.0 不支持 TLS 1.2，直连 GitHub 必然失败。
  var WINDOWS_INSTALL_SCRIPT_PLATFORM_URL = '/api/external/binary/windows-exporter-install.ps1';

  // windowsInstallScript(action) 生成“先下载 install.ps1 再执行”的 PowerShell 命令脚本。
  // 兼容 PowerShell 2.0：用 System.Net.WebClient，不用 Invoke-WebRequest。
  //
  // 两个 2008 R2 关键点：
  //   1. 下载的 .ps1 必须带 UTF-8 BOM，否则 PowerShell 2.0 按系统 ANSI(GBK) 解码，
  //      中文注释会导致 UnexpectedToken（平台新版已在服务端补齐，这里再兜底一次）；
  //   2. 用子进程 powershell.exe -File 执行，确保语法错误/脚本失败能拿到非 0 退出码，
  //      不会被外层 `exit $LASTEXITCODE` 吞成成功。
  function windowsInstallScript(action) {
    var args = action === 'uninstall' ? ' uninstall -Yes' : ' ' + action;
    // 控制台地址兜底：老 Agent 可能没有注入 UPGRADE_AGENT_SERVER_URL。
    var consoleBase = '';
    try {
      if (location && location.origin && location.origin !== 'null' &&
          /^https?:/i.test(location.origin)) {
        consoleBase = location.origin.replace(/\/+$/, '');
      }
    } catch (e) { }
    return [
      "$ErrorActionPreference = 'Stop'",
      "# 平台地址优先（2008 R2 可用 HTTP，不依赖 GitHub/TLS 1.2）",
      "$base = $env:UPGRADE_AGENT_SERVER_URL",
      "if (-not $base) { $base = '" + consoleBase + "' }",
      "$localUrl = ''",
      "$fullUrl = ''",
      "if ($base) {",
      "  $b = $base.TrimEnd('/')",
      "  $localUrl = $b + '" + WINDOWS_INSTALL_SCRIPT_PLATFORM_URL + "?local=1'",
      "  $fullUrl = $b + '" + WINDOWS_INSTALL_SCRIPT_PLATFORM_URL + "'",
      "}",
      "$proxyUrl = '" + WINDOWS_INSTALL_SCRIPT_URL + "'",
      "$tmpDir = $env:TEMP",
      "if (-not $tmpDir) { $tmpDir = [System.IO.Path]::GetTempPath() }",
      "$f = Join-Path $tmpDir 'windows_exporter-install.ps1'",
      "# 下载前探测 TLS 1.2：2008 R2 默认不支持，直接跳过 HTTPS 避免 WebClient 100 秒超时",
      "$tls12 = $false",
      "try {",
      "  $mask = [int][Net.ServicePointManager]::SecurityProtocol -bor 3072",
      "  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]$mask",
      "  $tls12 = $true",
      "} catch { }",
      "# PS2 兼容下载（显式超时，避免默认 100 秒卡死）",
      "function Save-Url([string]$u, [string]$p, [int]$timeoutMs) {",
      "  $req = [System.Net.HttpWebRequest]::Create($u)",
      "  $req.Timeout = $timeoutMs",
      "  $req.ReadWriteTimeout = $timeoutMs",
      "  $req.UserAgent = 'upgrade-agent/windows_exporter-installer'",
      "  $resp = $null; $stream = $null; $fs = $null",
      "  try {",
      "    $resp = $req.GetResponse()",
      "    $stream = $resp.GetResponseStream()",
      "    $fs = [System.IO.File]::Create($p)",
      "    $buf = New-Object byte[] 8192",
      "    while (($n = $stream.Read($buf, 0, $buf.Length)) -gt 0) { $fs.Write($buf, 0, $n) }",
      "  } finally {",
      "    if ($fs) { $fs.Close() }",
      "    if ($stream) { $stream.Close() }",
      "    if ($resp) { $resp.Close() }",
      "  }",
      "}",
      "$errors = @()",
      "$ok = $false",
      "if ($localUrl) {",
      "  try { Save-Url $localUrl $f 20000; $ok = $true } catch { $errors += ('平台离线缓存: ' + $_.Exception.Message) }",
      "}",
      "# 平台代下载：平台自身有 10 分钟上限；脚本很小，180 秒足够",
      "if (-not $ok -and $fullUrl) {",
      "  try { Save-Url $fullUrl $f 180000; $ok = $true } catch { $errors += ('平台代下载: ' + $_.Exception.Message) }",
      "}",
      "# 直连 GitHub（经代理）只在 TLS 1.2 可用时尝试",
      "if (-not $ok -and $tls12) {",
      "  try { Save-Url $proxyUrl $f 60000; $ok = $true } catch { $errors += ('代理: ' + $_.Exception.Message) }",
      "}",
      "if (-not $ok -or -not (Test-Path $f) -or ((Get-Item $f).Length -eq 0)) {",
      "  [Console]::Error.WriteLine('ERROR: 无法下载 install.ps1：' + ($errors -join '; '))",
      "  if (-not $base) {",
      "    [Console]::Error.WriteLine('未获取到控制台地址（UPGRADE_AGENT_SERVER_URL 为空），且本机不支持 TLS 1.2；请升级 Agent 或手动指定脚本。')",
      "  } else {",
      "    [Console]::Error.WriteLine('请在控制台「外部文件」为 windows-exporter-install.ps1 上传脚本或刷新缓存后重试。')",
      "  }",
      "  exit 1",
      "}",
      "# 防止代理/网关把错误页当 200 返回",
      "$head = [System.IO.File]::ReadAllText($f)",
      "if ($head -match '(?i)^\\s*<(!doctype|html|head|body|\\?xml)') {",
      "  [Console]::Error.WriteLine('ERROR: 下载内容不是 PowerShell 脚本（可能是错误页）')",
      "  exit 1",
      "}",
      "# 确保 UTF-8 BOM：PowerShell 2.0 只有看到 BOM 才按 UTF-8 解析",
      "$bytes = [System.IO.File]::ReadAllBytes($f)",
      "if ($bytes.Length -lt 3 -or $bytes[0] -ne 0xEF -or $bytes[1] -ne 0xBB -or $bytes[2] -ne 0xBF) {",
      "  [System.IO.File]::WriteAllText($f, $head, (New-Object System.Text.UTF8Encoding($true)))",
      "}",
      "# 子进程执行：语法错误/脚本失败都会返回非 0 退出码",
      "$ps = $env:WINDOWS_EXPORTER_POWERSHELL",
      "if (-not $ps) { $ps = 'powershell.exe' }",
      "if ($env:SystemRoot) {",
      "  $candidate = Join-Path $env:SystemRoot 'System32\\WindowsPowerShell\\v1.0\\powershell.exe'",
      "  if (Test-Path $candidate) { $ps = $candidate }",
      "}",
      "$code = 1",
      "try {",
      "  & $ps -NoProfile -ExecutionPolicy Bypass -File $f" + args + "",
      "  if ($LASTEXITCODE -ne $null) { $code = $LASTEXITCODE } else { $code = 0 }",
      "} catch {",
      "  [Console]::Error.WriteLine('ERROR: ' + $_.Exception.Message)",
      "  $code = 1",
      "}",
      "exit $code"
    ].join('\n');
  }

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
        script_url: INSTALL_SCRIPT_PLATFORM_URL,
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
        script_url: INSTALL_SCRIPT_PLATFORM_URL,
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
        script_url: INSTALL_SCRIPT_PLATFORM_URL,
        exec_args: ['uninstall', '-y'],
        exec_timeout: '30m'
      }
    },
    {
      key: 'node_exporter_script',
      label: 'node_exporter_script（脚本安装，Agent 下载后执行）',
      desc: '直接使用真实可用的 script_url（优先从平台 /api/external/binary 下载，外网不通时自动切换），不需要上传制品。Agent 下载脚本后用 Shell 执行；脚本会自行从 iflyelf/node_exporter latest 下载静态二进制（当前 1.12.1，仅 amd64/arm64），并按目标机 init 写 systemd unit 或 SysV init。script 模式与命令任务默认可用（由 agent.allow_exec_mode 控制，默认开启，可在"设置"里关闭）；高级配置 JSON 已预填 script_url、exec_env、exec_timeout、health_cmd 与 version_cmd，post_install 脚本文本框也填入了一段可选的安装后检查示例，可直接修改参考。若不想依赖外链，删除高级 JSON 中的 script_url，改为上传 examples/node_exporter/install.sh 作为制品即可。',
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
        script_url: INSTALL_SCRIPT_PLATFORM_URL,
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
        script_url: INSTALL_SCRIPT_PLATFORM_URL,
        exec_args: ['uninstall', '-y'],
        exec_timeout: '10m'
      }
    },
    {
      key: 'windows_exporter_script',
      label: 'windows_exporter_script（Windows 脚本安装）',
      desc: 'Windows 专用示例：script_url 指向平台 /api/external/binary（自动代理外网或返回本地上传文件）。Agent 在 Windows 主机上默认用 powershell.exe -NoProfile -ExecutionPolicy Bypass -File 执行，脚本按系统版本自动选择 exe：Windows 10/Server 2016+ 用 latest，Windows 7/Server 2008 R2~2012 R2 用 legacy（v0.23.1 + Go 1.20，默认启用 hardware_info），注册 Windows 服务 windows_exporter、创建防火墙规则并写入默认配置。适合 Windows Server 2008 R2 ~ Windows 11；需要 PowerShell 2.0+（系统自带）。',
      comp: {
        name: 'windows_exporter_script', display_name: 'Windows Exporter（Win 脚本安装）', deploy_mode: 'script',
        binary_name: 'windows_exporter',
        install_path: 'C:\\Program Files\\windows_exporter',
        service_name: 'windows_exporter', service_mgr: 'auto', enabled: true,
        script_url: WINDOWS_INSTALL_SCRIPT_PLATFORM_URL,
        exec_args: ['install'],
        exec_timeout: '15m',
        health_cmd: [['powershell.exe', '-NoProfile', '-Command', "if ((Get-Service windows_exporter -ErrorAction Stop).Status -ne 'Running') { exit 1 }"]],
        version_cmd: [['powershell.exe', '-NoProfile', '-Command', "& 'C:\\Program Files\\windows_exporter\\windows_exporter.exe' --version"]]
      }
    },
    {
      key: 'windows_exporter_script_update',
      label: 'windows_exporter_script_update（Win 脚本更新，失败自动回滚）',
      desc: '与 Windows 安装示例同源，只把 exec_args 改成 update：脚本先备份旧 exe 为 windows_exporter.exe.bak，替换后重启服务；启动失败自动回滚旧版本。组件名独立（windows_exporter_script_update），避免与安装组件互相覆盖。',
      comp: {
        name: 'windows_exporter_script_update', display_name: 'Windows Exporter（Win 脚本更新）', deploy_mode: 'script',
        binary_name: 'windows_exporter',
        install_path: 'C:\\Program Files\\windows_exporter',
        service_name: 'windows_exporter', service_mgr: 'auto', enabled: true,
        script_url: WINDOWS_INSTALL_SCRIPT_PLATFORM_URL,
        exec_args: ['update'],
        exec_timeout: '15m',
        health_cmd: [['powershell.exe', '-NoProfile', '-Command', "if ((Get-Service windows_exporter -ErrorAction Stop).Status -ne 'Running') { exit 1 }"]],
        version_cmd: [['powershell.exe', '-NoProfile', '-Command', "& 'C:\\Program Files\\windows_exporter\\windows_exporter.exe' --version"]]
      }
    },
    {
      key: 'windows_exporter_script_uninstall',
      label: 'windows_exporter_script_uninstall（Win 脚本卸载，非交互）',
      desc: 'exec_args 为 uninstall -Yes（-Yes 跳过交互确认，非交互场景必需）。脚本停止并删除 Windows 服务 windows_exporter、防火墙规则与安装目录。卸载后服务已不存在，因此本示例不填 health_cmd / version_cmd，也不填服务名，避免平台在卸载后尝试启服并做健康检查导致任务必然失败。',
      comp: {
        name: 'windows_exporter_script_uninstall', display_name: 'Windows Exporter（Win 脚本卸载）', deploy_mode: 'script',
        binary_name: 'windows_exporter',
        install_path: 'C:\\Program Files\\windows_exporter',
        service_name: '', service_mgr: 'auto', enabled: true,
        script_url: WINDOWS_INSTALL_SCRIPT_PLATFORM_URL,
        exec_args: ['uninstall', '-Yes'],
        exec_timeout: '15m'
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
    function step(no, title, view, lines, extra, jumpLabel) {
      return h('div', { class: 'guide-step' }, [
        h('div', { class: 'guide-title' }, [
          h('span', { class: 'guide-no' }, [no]), title,
          view ? jump(view, jumpLabel || ('去' + title.split(' ')[0])) : null
        ]),
        h('ul', {}, lines.map(function (l) { return h('li', {}, [l]); })),
        extra || null
      ]);
    }

    var qs = state.quickstart;
    var lang = state.lang === 'en' ? 'en' : 'zh';
    function pick(v) {
      if (v == null) return '';
      if (typeof v === 'string') return v;
      return v[lang] || v.zh || '';
    }
    function pickList(v) {
      if (!v) return [];
      return v[lang] || v.zh || [];
    }
    if (!qs || !qs.systems || !qs.systems.linux) {
      return h('details', { class: 'card help' }, [
        h('summary', {}, ['📘 ' + (lang === 'en' ? 'Quick start' : '快速上手')]),
        h('div', { class: 'muted' }, [state.quickstartErr
          ? (lang === 'en' ? 'Failed to load quick start: ' : '快速上手加载失败：') + state.quickstartErr
          : (lang === 'en' ? 'Loading quick start…' : '正在加载快速上手…')])
      ]);
    }

    // 按现有主机自动选择示例系统；无主机时默认 Linux。
    var os = state.guideOS;
    if (!os) {
      var winCount = 0, linuxCount = 0;
      (state.hosts || []).forEach(function (h) {
        if ((h.os_family || '').toLowerCase() === 'windows') winCount++; else linuxCount++;
      });
      os = (winCount > 0 && linuxCount === 0) ? 'windows' : 'linux';
    }
    var sys = qs.systems[os] || qs.systems.linux;

    var osBar = h('div', { class: 'toolbar' }, [
      h('span', { class: 'muted' }, [pick(qs.labels && qs.labels.system)]),
      h('button', {
        type: 'button', class: 'btn small' + (os === 'linux' ? '' : ' ghost'),
        onclick: function () { state.guideOS = 'linux'; render(); }
      }, ['Linux']),
      h('button', {
        type: 'button', class: 'btn small' + (os === 'windows' ? '' : ' ghost'),
        onclick: function () { state.guideOS = 'windows'; render(); }
      }, ['Windows'])
    ]);

    var steps = (sys.steps || []).map(function (st) {
      var code = pick(st.code);
      return step(
        st.no, pick(st.title), st.view, pickList(st.lines),
        code && code.trim() ? codeBlock(code) : null,
        pick(st.jump)
      );
    });
    var footers = pickList(qs.footer).map(function (t) { return hint(t); });

    var d = h('details', { class: 'card help' }, [
      h('summary', {}, ['📘 ' + (pick(qs.summary) || (lang === 'en' ? 'Quick start' : '快速上手'))]),
      osBar
    ].concat(steps).concat(footers));
    d.open = open;
    d.ontoggle = function () { state.guideOpen = d.open; };
    return d;
  }

  function switchView(v) {
    // 切换页签时关闭任务详情/帮助弹窗并停止轮询，避免后台定时器继续渲染旧弹窗。
    closeTaskDetail(true);
    stopAutoRefresh();
    state.helpOpen = null;
    state.view = v;
    state.err = '';
    if (v === 'settings') loadSettings();
    else if (v === 'components') loadComponents();
    else if (v === 'artifacts') loadArtifacts();
    else if (v === 'tasks') loadTasks();
    else if (v === 'extbin') loadExternalBinaries();
    else if (v === 'org') loadOrg();
    else if (v === 'audit') loadAudit();
    else if (v === 'password') render();
    else loadHosts();
  }

  // ---------- URL 路由（hash） ----------
  //
  // 刷新/前进后退时恢复页面与主要筛选；使用 replaceState 避免每次渲染都堆历史。
  // 只持久化列表页的关键状态，弹窗/编辑中的表单不写入 URL。
  var KNOWN_VIEWS = {
    hosts: 1, components: 1, artifacts: 1, tasks: 1, org: 1,
    extbin: 1, audit: 1, settings: 1, password: 1
  };

  function parseHash() {
    var raw = (location.hash || '').replace(/^#\/?/, '');
    if (!raw) return null;
    var idx = raw.indexOf('?');
    var view = idx >= 0 ? raw.slice(0, idx) : raw;
    if (!KNOWN_VIEWS[view]) return null;
    var params = {};
    if (idx >= 0) {
      raw.slice(idx + 1).split('&').forEach(function (kv) {
        if (!kv) return;
        var p = kv.split('=');
        try { params[decodeURIComponent(p[0])] = decodeURIComponent((p[1] || '').replace(/\+/g, ' ')); } catch (e) {}
      });
    }
    return { view: view, params: params };
  }

  function applyHashToState(r) {
    var p = r.params || {};
    var n = function (k, d) { var v = parseInt(p[k], 10); return isNaN(v) ? d : v; };
    if (r.view === 'hosts') {
      state.q = p.q || '';
      state.hostPage = n('page', 1);
    } else if (r.view === 'components') {
      state.compQ = p.q || '';
      state.compPage = n('page', 1);
    } else if (r.view === 'artifacts') {
      state.artifactQ = p.q || '';
      state.artifactFilterComp = p.comp || '';
      state.artifactPage = n('page', 1);
    } else if (r.view === 'tasks') {
      state.taskFilterKind = p.kind || '';
      state.taskFilterStatus = p.status || '';
      state.taskFilterComp = p.component_id || '';
      state.taskFilterCreator = p.created_by || '';
      state.taskQ = p.q || '';
      state.taskPage = n('page', 1);
    } else if (r.view === 'org') {
      state.orgTab = p.tab || 'users';
    } else if (r.view === 'audit') {
      state.auditFilters = {
        q: p.q || '', username: p.username || '', action: p.action || '',
        status: p.status || '', from: p.from || '', to: p.to || ''
      };
      state.auditPage = n('page', 1);
    }
  }

  function syncHash() {
    if (!state.user) return;
    var qs = [];
    var add = function (k, v) { if (v !== '' && v != null) qs.push(k + '=' + encodeURIComponent(v)); };
    if (state.view === 'hosts') {
      add('q', state.q); add('page', state.hostPage > 1 ? state.hostPage : '');
    } else if (state.view === 'components') {
      add('q', state.compQ); add('page', state.compPage > 1 ? state.compPage : '');
    } else if (state.view === 'artifacts') {
      add('q', state.artifactQ); add('comp', state.artifactFilterComp);
      add('page', state.artifactPage > 1 ? state.artifactPage : '');
    } else if (state.view === 'tasks') {
      add('kind', state.taskFilterKind); add('status', state.taskFilterStatus);
      add('component_id', state.taskFilterComp); add('created_by', state.taskFilterCreator);
      add('q', state.taskQ); add('page', state.taskPage > 1 ? state.taskPage : '');
    } else if (state.view === 'org') {
      if (state.orgTab && state.orgTab !== 'users') add('tab', state.orgTab);
    } else if (state.view === 'audit') {
      var f = state.auditFilters || {};
      add('q', f.q); add('username', f.username); add('action', f.action);
      add('status', f.status); add('from', f.from); add('to', f.to);
      add('page', state.auditPage > 1 ? state.auditPage : '');
    }
    var h = '#/' + state.view + (qs.length ? '?' + qs.join('&') : '');
    try {
      if (location.hash === h) return;
      if (history.replaceState) history.replaceState(null, '', h);
      else location.hash = h;
    } catch (e) {}
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
          state.myPerms = undefined;
          state.quickstart = undefined;
          state.quickstartErr = '';
          state.taskForm = null;
          state.loginBusy = false;
          state.loginName = '';
          state.err = '';
          var route = parseHash();
          if (route) {
            applyHashToState(route);
            switchView(route.view);
          } else {
            loadHosts();
          }
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
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn', disabled: state.loginBusy ? 'disabled' : null,
          onclick: doLogin
        }, [state.loginBusy ? '登录中…' : '登录']),
        h('button', {
          class: 'btn ghost', title: 'Switch language',
          onclick: function () { toggleLang(); }
        }, [state.lang === 'en' ? '中文' : 'EN'])
      ])
    ]);
    uEl.onkeydown = onKey;
    pEl.onkeydown = onKey;
    // 登录页自动聚焦用户名；密码框按回车同样提交。
    setTimeout(function () {
      try {
        if (state.user) return;
        // 触屏设备不自动聚焦：避免一打开登录页就弹出软键盘并放大页面。
        if (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) return;
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
      (qEl = h('input', { value: state.q, placeholder: 'IP/出口 IP/隧道地址/主机名/业务组' }, [])),
      h('button', { class: 'btn', onclick: doSearch }, ['搜索']),
      state.q ? h('button', { class: 'btn ghost', onclick: function () { state.q = ''; state.hostPage = 1; loadHosts(); } }, ['清除']) : null,
      // 同步会写入/清理全局主机清单，后端要求 superadmin；其他角色不展示入口。
      isSuperAdmin() ? h('button', { class: 'btn ghost', onclick: syncHosts }, ['同步 Consul']) : null,
      // consul_mgr 同步仅 superadmin 可见：后端为 RequireSuperAdmin，其他角色点了必然 403。
      // 点击后先弹窗选择服务组（列表来自 consul_mgr），不在配置里写死 group_id。
      isSuperAdmin() ? h('button', {
        class: 'btn ghost', title: '从 consul_mgr 汇总的多套 Consul 同步主机',
        onclick: openConsulMgrSync
      }, ['同步 consul_mgr…']) : null,
      state.hostsLoading ? h('span', { class: 'muted' }, ['加载中…']) : null,
      h('span', { class: 'muted auto-refresh-tip' }, ['● 自动刷新中'])
    ]);
    qEl.onkeydown = function (ev) { if (ev.key === 'Enter') doSearch(); };

    var listHostDefs = (state.hostFieldDefs || [])
      .filter(function (d) { return d.showInList; })
      .sort(function (a, b) { return (a.sortOrder || 0) - (b.sortOrder || 0); });
    var cols = ['IP', '主机名', 'OS', 'ARCH', '业务组', 'app_code', 'Agent', '来源']
      .concat(listHostDefs.map(function (d) { return d.label; }));
    if (isAdmin()) cols.push('操作');
    var head = h('tr', {}, cols.map(function (t) { return h('th', { scope: 'col' }, [t]); }));
    var rows = state.hosts.map(function (x) {
      var cells = [
        h('td', {}, [ipCell(x)]), h('td', {}, [x.hostname || '-']),
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
      listHostDefs.forEach(function (d) {
        cells.push(h('td', {}, [(x.custom_fields && x.custom_fields[d.fieldKey]) || '-']));
      });
      if (isAdmin()) {
        cells.push(h('td', {}, [h('div', { class: 'row-actions' }, [
          x.agent_id ? h('button', {
            class: 'btn ghost small', onclick: function () { upgradeAgents([x.id]); }
          }, ['升级 Agent']) : null,
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.editingHost = Object.assign({}, x);
              state.hostErr = '';
              state.tunnelTest = null;
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
        state.q ? '没有匹配的主机。' : (isAdmin()
          ? '还没有主机。在上方「接入主机（安装 Agent）」生成命令，到目标机器上执行即可。'
          : '还没有主机。请联系管理员接入主机。')
      ])
      : null;
    return h('div', {}, [
      // 主机接入是高频操作：完整接入卡片直接放在主机页顶部，
      // 「帮助 → 接入主机示例」仍保留，方便从菜单进入。
      isAdmin() ? enrollCard() : null,
      agentStatsCard(),
      state.hostErr ? h('div', { class: 'card' }, [h('div', { class: 'err', role: 'alert' }, [state.hostErr])]) : null,
      isAdmin() ? agentUpdateCard() : null,
      isAdmin() ? hostForm() : null,
      h('div', { class: 'card' }, [
        toolbar,
        (function () {
          var hostTable = h('table', {}, [h('thead', {}, [head]), h('tbody', {}, rows)]);
          var hostWrap = h('div', { class: 'table-wrap' }, [hostTable]);
          var hostBlock = h('div', { class: 'table-block' }, [hostWrap]);
          attachTableTools(hostTable, 'hosts', false, hostBlock);
          return hostBlock;
        })(),
        empty,
        state.total > 0 ? hostsPager() : null
      ])
    ]);
  }

  function loadAgentStats() {
    state.agentStatsAt = Date.now();
    return api('/api/agents/stats')
      .then(function (d) {
        // 自动刷新时版本分布通常不变，避免每次轮询都重建 DOM。
        var sig = JSON.stringify(d || null);
        if (sig === state.agentStatsSig) return;
        state.agentStatsSig = sig;
        state.agentStats = d;
        render();
      })
      .catch(function () { state.agentStats = null; });
  }

  function agentStatsCard() {
    var st = state.agentStats;
    if (!st || !st.total) return null;
    var rows = (st.list || []).map(function (x) {
      return [
        x.version || '未知',
        x.os || '-',
        x.arch || '-',
        x.count || 0,
        x.offline || 0,
        x.outdated
          ? h('span', { class: 'tag status-failed' }, ['旧版本'])
          : h('span', { class: 'tag status-success' }, ['最新'])
      ];
    });
    var offlineNote = st.offline_after
      ? '（离线按 agent 状态或心跳超过 ' + st.offline_after + ' 统计）'
      : '（离线按 agent 状态或心跳超时统计）';
    return h('div', { class: 'card' }, [
      h('h3', {}, ['Agent 版本分布']),
      hint('目标版本：' + (st.target_version || '-') +
        '；离线 ' + (st.offline || 0) + ' 台；旧版本 ' + (st.outdated || 0) +
        ' 台' + offlineNote + '。'),
      simpleTable(['版本', 'OS', '架构', '数量', '离线', '状态'], st.list || [],
        function (x, i) { return rows[i]; })
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

  // ipCell 展示主机地址：
  //   - 主地址为注册来源 IP（服务端观察到的地址，作为主机身份）；
  //   - 多网卡时补充 Agent 探测到的出口 IP；
  //   - 主机隧道 IP 仅作展示/筛选；
  //   - 连接地址覆盖非空时展示，并用 Agent 自报的连接地址确认是否已切换。
  function ipCell(x) {
    var out = [h('div', {}, [x.ip])];
    if (x.exit_ip && x.exit_ip !== x.ip) {
      out.push(h('div', { class: 'muted' }, ['出口 ' + x.exit_ip]));
    }
    if (x.tunnel_ip) {
      out.push(h('div', { class: 'muted' }, ['主机隧道 ' + x.tunnel_ip]));
    }
    if (x.server_url_override) {
      out.push(h('div', { class: 'muted' }, ['连接覆盖 ' + x.server_url_override]));
      var connected = x.agent_server_url || '';
      out.push(h('div', { class: 'muted' }, [
        connected && connected.indexOf('://') >= 0 && sameEndpoint(connected, x.server_url_override)
          ? '✓ 已连接到覆盖地址'
          : (connected ? ('当前连接 ' + connected) : '等待 Agent 心跳切换')
      ]));
      out.push(tunnelCheckLine(x));
    }
    return h('div', {}, out);
  }

  // tunnelCheckLine 展示连接地址保存后自动拨测的结论（平台侧 + 主机侧）。
  // 未检查完成时显示「检查中」，失败标红并在悬停时给出阶段明细。
  function tunnelCheckLine(x) {
    if (x.tunnel_check_ok === undefined || x.tunnel_check_ok === null) {
      return x.tunnel_check_detail
        ? h('div', { class: 'muted' }, [x.tunnel_check_detail])
        : h('div', { class: 'muted' }, ['隧道自检：检查中…']);
    }
    var where = x.tunnel_check_source === 'agent' ? '主机侧' : '平台侧';
    var text = (x.tunnel_check_ok ? '✓ ' : '✗ ') + where + '自检' +
      (x.tunnel_check_ok ? '通过' : '未通过') +
      (x.tunnel_check_latency_ms ? '（' + x.tunnel_check_latency_ms + 'ms）' : '');
    var cls = x.tunnel_check_ok ? 'tag tunnel-ok' : 'tag tunnel-fail';
    return h('div', { title: x.tunnel_check_detail || '' }, [
      h('span', { class: cls }, [text])
    ]);
  }

  // sameEndpoint 判断 Agent 自报地址是否命中配置的平台隧道地址（忽略协议/结尾斜杠）。
  function sameEndpoint(a, b) {
    var strip = function (v) {
      return String(v || '').replace(/^https?:\/\//, '').replace(/\/$/, '').toLowerCase();
    };
    return strip(a) === strip(b);
  }

  // tunnelTestBlock 渲染隧道拨测结果：逐步展示 DNS / TCP / TLS / HTTP 阶段。
  function tunnelTestBlock() {
    var t = state.tunnelTest;
    if (!t) return null;
    if (t.err) return h('div', { class: 'err field', role: 'alert' }, ['拨测失败: ' + t.err]);
    if (!t.result) return h('div', { class: 'hint' }, ['正在拨测 ' + (t.target || '已保存地址') + '…']);
    var r = t.result;
    return h('div', { class: 'card' }, [
      h('h3', {}, [r.ok ? '✅ 连接地址拨测通过' : '❌ 连接地址拨测未通过']),
      hint('拨测地址：' + (r.url || t.target) + ' · 耗时 ' + (r.latency_ms || 0) + ' ms'),
      h('div', { class: 'table-wrap' }, [h('table', {}, [
        h('thead', {}, [h('tr', {}, [h('th', {}, ['阶段']), h('th', {}, ['结果']), h('th', {}, ['耗时']), h('th', {}, ['详情'])])]),
        h('tbody', {}, (r.stages || []).map(function (st) {
          return h('tr', {}, [
            h('td', {}, [st.name]),
            h('td', {}, [st.ok ? '✅' : '❌']),
            h('td', {}, [st.latency_ms + ' ms']),
            h('td', {}, [st.detail || '-'])
          ]);
        }))
      ])]),
      r.tls ? hint('TLS 证书：' + (r.tls.subject || '-') + '；签发者 ' + (r.tls.issuer || '-') +
        '；到期 ' + (r.tls.not_after || '-') + (r.tls.verified ? '' : '；校验失败：' + (r.tls.verify_error || ''))) : null,
      r.hint ? hint('💡 ' + r.hint) : null,
      hint('说明：这是从平台侧发起的拨测，只能证明平台所在环境能到达该地址；目标主机能否经隧道访问仍需看 Agent 心跳（约 30 秒内自动切换）。')
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
    var hostTunnelEl = h('input', {
      value: host.tunnel_ip || '',
      placeholder: '如 172.16.0.9'
    }, []);
    var serverOverrideEl = h('input', {
      value: host.server_url_override || '',
      placeholder: '如 10.8.0.5、10.8.0.5:8090 或 https://mgr.example.com'
    }, []);

    // 主机自定义字段：定义来自 /api/field-defs/host（在 loadHosts 中加载一次）。
    // 值来自 host.custom_fields；保存时整体提交，服务端按键合并。
    var hostFieldInputs = {};
    var hostFieldNodes = (state.hostFieldDefs || [])
      .filter(function (d) { return d.showInForm && d.editable; })
      .sort(function (a, b) { return (a.sortOrder || 0) - (b.sortOrder || 0); })
      .map(function (d) {
        var cur = (host.custom_fields && host.custom_fields[d.fieldKey]) || '';
        var el;
        if (d.fieldType === 'select') {
          var opts = [];
          try { opts = JSON.parse(d.options || '[]'); } catch (e) { opts = []; }
          el = h('select', {}, [h('option', { value: '' }, ['（未设置）'])].concat(opts.map(function (x) {
            return h('option', { value: x }, [x]);
          })));
          el.value = cur;
        } else if (d.fieldType === 'textarea') {
          el = h('textarea', { rows: '2' }, []);
          el.value = cur;
        } else {
          el = h('input', {
            type: d.fieldType === 'number' ? 'number' : (d.fieldType === 'date' ? 'date' : 'text'),
            value: cur
          }, []);
        }
        hostFieldInputs[d.fieldKey] = el;
        return field(d.label, el, '');
      });

    // testTunnel 从平台侧拨测候选连接地址；结果保留在 state.tunnelTest，
    // 便于在保存前确认端口/证书是否正常。
    var testTunnel = function () {
      var target = (serverOverrideEl.value || '').trim();
      state.tunnelTest = { target: target, loading: true, result: null, err: '' };
      render();
      api('/api/hosts/' + host.id + '/tunnel-test', {
        method: 'POST',
        body: JSON.stringify({ target: target })
      })
        .then(function (d) {
          state.tunnelTest = { target: target, loading: false, result: d.result, err: '' };
          render();
        })
        .catch(function (e) {
          state.tunnelTest = { target: target, loading: false, result: null, err: e.message };
          render();
        });
    };

    var save = function () {
      var payload = {
        hostname: nameEl.value,
        account: accountEl.value,
        app_code: appCodeEl.value,
        biz_group: bizEl.value,
        department: deptEl.value,
        vendor: vendorEl.value,
        host_type: typeEl.value,
        tunnel_ip: hostTunnelEl.value,
        server_url_override: serverOverrideEl.value
      };
      if (Object.keys(hostFieldInputs).length) {
        payload.custom_fields = {};
        Object.keys(hostFieldInputs).forEach(function (k) {
          payload.custom_fields[k] = hostFieldInputs[k].value;
        });
      }
      api('/api/hosts/' + host.id, { method: 'PUT', body: JSON.stringify(payload) })
        .then(function () {
          state.editingHost = null;
          state.hostErr = '';
          state.tunnelTest = null;
          loadHosts();
          // 保存会触发平台侧拨测 + 主机侧自检，几秒后再刷新一次让结论尽早出现。
          if (payload.server_url_override) {
            setTimeout(loadHosts, 3000);
            setTimeout(loadHosts, 12000);
          }
        })
        .catch(function (e) { state.hostErr = '保存失败: ' + e.message; render(); });
    };

    return h('div', { class: 'card' }, [
      h('h3', {}, ['编辑主机 ' + host.ip]),
      hint('IP、OS、ARCH、Agent 状态由 Agent 上报，不能手工修改。Agent 重新注册会覆盖主机名；Consul 同步时，若 Consul 返回非空的业务字段，会覆盖对应字段。出口 IP 与连接地址覆盖见下方说明。'),
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
      h('div', { class: 'grid2' }, [
        field('主机侧隧道 IP (tunnel_ip)', hostTunnelEl,
          '可选。主机在隧道网段里的地址（如 WireGuard/IPSec 分配的 172.16.x.x），仅用于展示与筛选。' +
          '平台不主动反连主机，指令仍由 Agent 外连取回。'),
        field('连接地址覆盖 (server_url_override)', serverOverrideEl,
          '可选。临时改写该主机 Agent 连接平台的地址，例如该主机只能走某条专线/VPN 入口。' +
          '注意：它不参与隧道建立——隧道由隧道软件负责，这里只切换 Agent 的连接入口。' +
          '支持纯 IP、IP:端口或完整 http(s) URL，缺省协议/端口沿用安装时的地址。' +
          '保存后 Agent 在下一次心跳（约 30 秒内）自动切换并持久化；清空后回退默认地址。' +
          '连续 3 次心跳失败会自动回退并在 5 分钟内不再重试，避免失联。')
      ]),
      hostFieldNodes.length ? h('div', {}, [
        h('h3', {}, ['自定义字段']),
        h('div', { class: 'grid2' }, hostFieldNodes)
      ]) : null,
      (host.exit_ip ? hint('Agent 上报的出口 IP：' + host.exit_ip + '（多网卡时用于确认实际出口网卡）') : null),
      (host.agent_server_url ? hint('Agent 当前连接地址：' + host.agent_server_url) : null),
      (host.server_url_override && host.tunnel_check_ok === false
        ? h('div', { class: 'err' }, ['上次连接地址自检未通过：' + (host.tunnel_check_detail || '')])
        : null),
      tunnelTestBlock(),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', { class: 'btn ghost', onclick: testTunnel, disabled: (state.tunnelTest && state.tunnelTest.loading) ? 'disabled' : null },
          [(state.tunnelTest && state.tunnelTest.loading) ? '拨测中…' : '测试连接地址']),
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

  var AGENT_OS = [
    ['linux', 'Linux（CentOS / Ubuntu / 麒麟 / 统信等）'],
    ['windows', 'Windows（Server 2008 R2 ~ Windows 11）']
  ];

  var AGENT_ARCHES = {
    linux: [
      ['amd64', 'amd64（x86_64）'],
      ['arm64', 'arm64（aarch64，鲲鹏/飞腾等）'],
      ['arm', 'arm（32 位）'],
      ['386', '386（32 位 x86）'],
      ['amd64-legacy', 'amd64-legacy（CentOS 6 等老内核）'],
      ['386-legacy', '386-legacy（老内核 32 位）'],
      ['arm64-legacy', 'arm64-legacy（老内核 arm64）']
    ],
    windows: [
      ['amd64', 'amd64（x64，绝大多数 Windows）'],
      ['arm64', 'arm64（Windows on ARM）']
    ]
  };

  function archOptions(osName) {
    return (AGENT_ARCHES[osName] || AGENT_ARCHES.linux).map(function (a) {
      return h('option', { value: a[0] }, [a[1]]);
    });
  }

  // enrollCard 包含两种接入令牌：
  //   - 一次性令牌：保持原有 15 分钟、单次使用、每台机器一个；
  //   - 自动化令牌：给自动化平台使用，可永久/自定义到期，可限制次数/CIDR，可注销。
  function enrollCard() {
    var mode = state.enrollMode || 'one_time';
    return h('div', { class: 'card' }, [
      h('h3', {}, ['接入主机（安装 Agent）']),
      h('div', { class: 'tabs', role: 'tablist', style: 'margin-bottom:12px' }, [
        enrollModeTab('one_time', '一次性令牌'),
        enrollModeTab('automation', '自动化令牌')
      ]),
      mode === 'automation' ? automationEnrollBody() : oneTimeEnrollBody()
    ]);
  }

  function enrollModeTab(mode, label) {
    return h('div', {
      class: 'tab' + (state.enrollMode === mode ? ' active' : ''),
      role: 'tab', tabindex: '0', 'aria-selected': state.enrollMode === mode ? 'true' : 'false',
      onclick: function () { setEnrollMode(mode); },
      onkeydown: function (e) {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setEnrollMode(mode); }
      }
    }, [label]);
  }

  function setEnrollMode(mode) {
    state.enrollMode = mode;
    state.automationErr = '';
    if (mode === 'automation' && state.automationList === null) loadAutomationTokens();
    render();
  }

  function oneTimeEnrollBody() {
    var e = state.enroll;
    var forceEl = h('input', { type: 'checkbox', checked: !!(e && e.force) }, []);
    var osEl = h('select', { value: (e && e.os) || 'linux' },
      AGENT_OS.map(function (a) { return h('option', { value: a[0] }, [a[1]]); }));
    var curOS = (e && e.os) || 'linux';
    var archEl = h('select', { value: (e && e.arch) || 'amd64' }, archOptions(curOS));
    // 切换操作系统时同步刷新可选架构：Windows 只有 amd64/arm64，且无 legacy。
    osEl.onchange = function () {
      var next = osEl.value;
      var keep = archEl.value;
      var allowed = (AGENT_ARCHES[next] || []).map(function (a) { return a[0]; });
      var arch = allowed.indexOf(keep) >= 0 ? keep : 'amd64';
      state.enroll = { os: next, arch: arch, force: forceEl.checked, err: '' };
      render();
    };
    return h('div', {}, [
      hint('主机信息来自 Agent 注册：IP 取服务端看到的来源地址，主机名、OS、架构、init 系统由 Agent 上报。所以装好客户端，主机就会带着 IP 出现在列表里。'),
      field('目标操作系统', osEl, 'Windows 支持 Server 2008 R2 ~ Windows 11（amd64/arm64）；Windows 安装命令需在“管理员 PowerShell”中执行。'),
      field('目标机器架构', archEl, 'Linux 上执行 uname -m：x86_64 选 amd64，aarch64 选 arm64。Windows 绝大多数为 amd64；arm64 仅用于 Windows on ARM。镜像里默认只带构建架构的 Agent，选其他架构下载可能 404。'),
      field('强制重新注册', forceEl, '勾选后命令会带 --force，覆盖目标机已有 state.json 并重新注册。仅在换服务端、数据库重置或重新接入同一台机器时使用；普通重装请勿勾选。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          type: 'button', class: 'btn', onclick: function () {
            var osName = osEl.value;
            var arch = archEl.value;
            var force = forceEl.checked;
            api('/api/agent/enroll-token?os=' + encodeURIComponent(osName) + '&arch=' + encodeURIComponent(arch) + (force ? '&force=1' : ''), { method: 'POST' })
              .then(function (d) { state.enroll = { os: osName, arch: arch, force: force, base: d.base_url, cmd: d.install_command, expires: d.expires_in, err: '' }; render(); })
              .catch(function (err) { state.enroll = { os: osName, arch: arch, force: force, err: err.message }; render(); });
          }
        }, ['生成接入命令'])
      ]),
      e && e.err ? h('div', { class: 'err field', role: 'alert' }, [e.err]) : null,
      e && e.cmd ? h('div', {}, [
        e.base ? hint('安装命令使用地址：' + e.base + '。可在“设置”页修改 server.external_url，保存后立即生效；Nginx 域名场景填 https://your-domain。') : null,
        hint(((e.os === 'windows') ? '在目标机器用“管理员 PowerShell”执行（令牌一次性使用，' : '在目标机器用 root 执行（令牌一次性使用，') + Math.round((e.expires || 900) / 60) + ' 分钟内有效，每台机器各生成一次）：'),
        codeBlock(e.cmd),
        e.os === 'windows' ? hint('Windows 安装使用 PowerShell + System.Net.WebClient，兼容 Server 2008 R2 自带的 PowerShell 2.0。cmd 可先执行 powershell -NoProfile -ExecutionPolicy Bypass 再粘贴命令。服务以 LocalSystem 运行，重启后自动恢复。') : null,
        e.force ? hint('当前为强制重新注册命令（含 --force）：会覆盖目标机现有 Agent 身份，旧 Agent 记录将不再被该主机使用。') : null,
        hint('执行完成后点上方“搜索”刷新列表。如果 IP 显示成了代理地址，检查服务端 server.trust_proxy 配置。'),
        hint('目标机已注册时，普通命令会保留原身份（本次令牌不会被使用）；需要覆盖重注册请勾选“强制重新注册”后重新生成。')
      ]) : null
    ]);
  }

  function automationEnrollBody() {
    var created = state.automationCreated;
    var nameEl = h('input', { placeholder: 'prod-ops-automation' }, []);
    var expiryEl = h('select', { value: 'permanent' }, [
      h('option', { value: 'permanent' }, ['永久有效']),
      h('option', { value: '24h' }, ['24 小时']),
      h('option', { value: '168h' }, ['7 天']),
      h('option', { value: '720h' }, ['30 天']),
      h('option', { value: '2160h' }, ['90 天']),
      h('option', { value: 'custom' }, ['自定义时间'])
    ]);
    var customEl = h('input', { type: 'datetime-local' }, []);
    var customField = field('自定义到期时间', customEl, '使用浏览器本地时间；提交时转换为带时区的 RFC3339。');
    customField.style.display = 'none';
    expiryEl.onchange = function () {
      customField.style.display = expiryEl.value === 'custom' ? '' : 'none';
    };
    var maxUsesEl = h('input', { type: 'number', min: '0', step: '1', value: '0' }, []);
    var cidrEl = h('input', { placeholder: '10.0.0.0/8,192.168.1.0/24' }, []);
    var osEl = h('select', { value: 'linux' },
      AGENT_OS.map(function (a) { return h('option', { value: a[0] }, [a[1]]); }));
    var archEl = h('select', { value: 'amd64' }, archOptions('linux'));
    osEl.onchange = function () {
      var next = osEl.value;
      var keep = archEl.value;
      var allowed = (AGENT_ARCHES[next] || []).map(function (a) { return a[0]; });
      archEl.innerHTML = '';
      archOptions(next).forEach(function (o) { archEl.appendChild(o); });
      archEl.value = allowed.indexOf(keep) >= 0 ? keep : 'amd64';
    };
    var forceEl = h('input', { type: 'checkbox' }, []);

    var createdBlock = created ? h('div', { class: 'field' }, [
      hint('自动化令牌已生成（仅显示一次，请立即复制到自动化平台）：'),
      codeBlock(created.token),
      created.install_command ? hint('安装命令示例（token 已嵌入）：') : null,
      created.install_command ? codeBlock(created.install_command) : null,
      created.expires_at ? hint('到期时间：' + fmtTime(created.expires_at)) : hint('永久有效：不会自动过期，请妥善保管并定期轮换。'),
      h('button', { type: 'button', class: 'btn ghost small', onclick: function () { state.automationCreated = null; render(); } }, ['关闭提示'])
    ]) : null;

    var list = state.automationList;
    var listBlock;
    if (state.automationLoading) {
      listBlock = hint('加载中…');
    } else if (state.automationErr) {
      listBlock = h('div', { class: 'err', role: 'alert' }, [state.automationErr]);
    } else if (!list || !list.length) {
      listBlock = hint('暂无自动化令牌。');
    } else {
      listBlock = simpleTable(['名称', 'Token', '租户', '到期', '已用 / 上限', '最后使用', '状态', '操作'], list,
        function (x) {
          var status = x.status || (x.enabled ? 'active' : 'revoked');
          var statusText = { active: '启用', revoked: '已注销', expired: '已过期', exhausted: '已达上限' }[status] || status;
          var usage = x.max_uses ? x.used_count + ' / ' + x.max_uses : x.used_count + ' / 不限';
          return [
            x.name || '-',
            (x.token_prefix || '-') + '…',
            String(x.tenant_id || ''),
            x.expires_at ? fmtTime(x.expires_at) : '永久',
            usage,
            x.last_used_at ? fmtTime(x.last_used_at) + (x.last_used_ip ? ' · ' + x.last_used_ip : '') : '-',
            h('span', { class: 'tag' + (status === 'active' ? ' status-success' : ' status-failed') }, [statusText]),
            h('div', { class: 'row-actions' }, [
              x.enabled ? h('button', {
                type: 'button', class: 'btn ghost small',
                onclick: function () { revokeAutomationToken(x); }
              }, ['注销']) : null,
              x.enabled ? h('button', {
                type: 'button', class: 'btn ghost small',
                onclick: function () { rotateAutomationToken(x, osEl.value, archEl.value, forceEl.checked); }
              }, ['轮换']) : null
            ])
          ];
        });
    }

    return h('div', {}, [
      createdBlock,
      hint('自动化令牌给自动化平台使用：可永久或自定义到期，可限制使用次数和来源 CIDR。注销只阻止新主机注册，不影响已运行 Agent。'),
      field('令牌名称', nameEl, '用于区分不同自动化平台/环境，例如 prod-ops-automation。'),
      field('有效期', expiryEl, '永久有效不会自动过期；也可选择固定时长或自定义日期时间。'),
      customField,
      field('最大使用次数（0=不限）', maxUsesEl, '达到上限后令牌自动失效；永久令牌建议至少设置来源 CIDR。'),
      field('IP/CIDR 白名单（可选）', cidrEl, '逗号分隔，例如 10.0.0.0/8,192.168.1.0/24；为空表示不限制来源 IP。'),
      field('安装命令目标系统', osEl, '令牌本身与操作系统无关；这里只影响生成的安装命令示例。'),
      field('安装命令架构', archEl, '仅用于生成安装命令示例，不会限制令牌本身。'),
      field('强制重新注册', forceEl, '安装命令是否带 --force；普通接入不要勾选。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          type: 'button', class: 'btn', onclick: function () {
            var maxUses = parseInt(maxUsesEl.value, 10);
            if (isNaN(maxUses) || maxUses < 0) {
              alert('最大使用次数需为 0 或正整数');
              return;
            }
            var payload = {
              name: nameEl.value,
              max_uses: maxUses,
              allowed_cidrs: cidrEl.value.split(',').map(function (v) { return v.trim(); }).filter(Boolean),
              os: osEl.value,
              arch: archEl.value,
              force: forceEl.checked
            };
            if (expiryEl.value === 'permanent') {
              payload.permanent = true;
            } else if (expiryEl.value === 'custom') {
              if (!customEl.value) { alert('请选择自定义到期时间'); return; }
              var dt = new Date(customEl.value);
              if (isNaN(dt.getTime())) { alert('自定义到期时间格式无效'); return; }
              payload.expires_at = dt.toISOString();
            } else {
              payload.expires_in = expiryEl.value;
            }
            api('/api/agent/enroll-tokens', { method: 'POST', body: JSON.stringify(payload) })
              .then(function (d) {
                state.automationCreated = d;
                state.automationErr = '';
                state.automationList = null;
                loadAutomationTokens();
                render();
              })
              .catch(function (err) { state.automationErr = err.message; render(); });
          }
        }, ['生成自动化令牌']),
        h('button', {
          type: 'button', class: 'btn ghost', onclick: function () {
            state.automationList = null;
            loadAutomationTokens();
            render();
          }
        }, ['刷新列表'])
      ]),
      h('h3', { style: 'margin-top:18px' }, ['自动化令牌列表']),
      listBlock
    ]);
  }

  function loadAutomationTokens() {
    state.automationLoading = true;
    state.automationErr = '';
    return api('/api/agent/enroll-tokens')
      .then(function (d) {
        state.automationList = (d && d.list) || [];
        state.automationLoading = false;
        render();
      })
      .catch(function (e) {
        state.automationLoading = false;
        state.automationErr = e.message;
        state.automationList = [];
        render();
      });
  }

  function revokeAutomationToken(x) {
    if (!confirm('确定注销自动化令牌“' + (x.name || x.id) + '”吗？注销后只阻止新主机注册，已运行 Agent 不受影响。')) return;
    api('/api/agent/enroll-tokens/' + x.id + '/revoke', { method: 'POST' })
      .then(function () {
        if (state.automationCreated && state.automationCreated.id === x.id) state.automationCreated = null;
        state.automationList = null;
        loadAutomationTokens();
        render();
      })
      .catch(function (e) { alert('注销失败: ' + e.message); });
  }

  function rotateAutomationToken(x, osName, arch, force) {
    if (!confirm('确定轮换令牌“' + (x.name || x.id) + '”吗？旧 token 会立即失效。')) return;
    api('/api/agent/enroll-tokens/' + x.id + '/rotate?os=' + encodeURIComponent(osName || 'linux') +
      '&arch=' + encodeURIComponent(arch || 'amd64') + (force ? '&force=1' : ''), { method: 'POST' })
      .then(function (d) {
        state.automationCreated = d;
        state.automationList = null;
        loadAutomationTokens();
        render();
      })
      .catch(function (e) { alert('轮换失败: ' + e.message); });
  }

  // ---------- 外部文件代缓存（表格页：增删行 + 测试下载） ----------
  //
  // 用途：老系统（Windows Server 2008 R2 + PowerShell 2.0）不支持 TLS 1.2，
  // 由控制台代下载公网文件，主机用 http 从 /api/external/binary/<name> 拉取。
  function loadExternalBinaries() {
    state.extbinErr = '';
    state.extbinDraft = null;
    state.extbinHosts = null;
    state.extbinTTL = null;
    state.extbinAllowPrivate = null;
    state.extbinCIDRs = null;

    var page = state.extbinPage || 1;
    var q = encodeURIComponent(state.extbinQuery || '');
    api('/api/external/binaries?page=' + page + '&size=20&q=' + q)
      .then(function (d) {
        state.extbin = d;
        // 服务端分页：草稿只含当前页条目；其余页面不参与保存。
        state.extbinDraft = (d.items || []).map(function (x) {
          return { name: x.name, url: x.url, script: !!x.script, saved: true,
            cached: x.cached, cached_size: x.cached_size, platform_url: x.platform_url };
        });
        state.extbinDeleted = [];
        state.extbinBusy = false;
        render();
      })
      .catch(function (e) {
        state.extbinErr = e.message;
        state.extbinBusy = false;
        render();
      });
  }

  // extbinReloadPage 保持搜索词重载当前页。
  function extbinReloadPage(page) {
    state.extbinPage = page || 1;
    state.extbinDraft = null;
    loadExternalBinaries();
  }

  function fmtBytes(n) {
    if (!n) return '-';
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
    return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
  }

  function externalBinariesView() {
    var d = state.extbin;
    if (!d && !state.extbinErr) {
      return h('div', { class: 'card' }, [h('h3', {}, ['外部文件代缓存']),
        h('div', { class: 'muted' }, ['加载中…'])]);
    }

    // 草稿放 state.extbinDraft：新增/编辑行后 render 仍保留，保存时整体提交。
    var draft = state.extbinDraft;
    if (!draft) {
      draft = (d && d.items ? d.items : []).map(function (x) {
        return { name: x.name, url: x.url, script: !!x.script,
          cached: x.cached, cached_size: x.cached_size, platform_url: x.platform_url,
          local: !!x.local, local_size: x.local_size };
      });
      state.extbinDraft = draft;
    }
    var hostsText = state.extbinHosts != null ? state.extbinHosts
      : ((d && d.hosts) || []).join(', ');
    var ttlText = state.extbinTTL != null ? state.extbinTTL : ((d && d.ttl) || '24h');
    var allowPrivate = state.extbinAllowPrivate != null ? state.extbinAllowPrivate
      : !!(d && d.allow_private);
    var cidrsText = state.extbinCIDRs != null ? state.extbinCIDRs
      : ((d && d.allow_cidrs) || []).join(', ');

    var hostsEl = h('input', {
      value: hostsText, placeholder: 'github.com, release-assets.githubusercontent.com, proxy.xiaonuo.live',
      oninput: function () { state.extbinHosts = hostsEl.value; }
    }, []);
    var ttlEl = h('input', {
      value: ttlText, placeholder: '24h',
      oninput: function () { state.extbinTTL = ttlEl.value; }
    }, []);
    var allowPrivateEl = h('input', {
      type: 'checkbox', checked: allowPrivate,
      onchange: function () { state.extbinAllowPrivate = allowPrivateEl.checked; }
    }, []);
    var cidrsEl = h('input', {
      value: cidrsText, placeholder: '如 10.0.0.0/8,192.168.1.10',
      oninput: function () { state.extbinCIDRs = cidrsEl.value; }
    }, []);

    // 搜索 + 分页（前端过滤：白名单通常几十条，避免额外接口）
    var qEl = h('input', {
      id: 'extbin-q', value: state.extbinQuery || '', placeholder: '按名称 / 上游地址过滤（回车搜索）',
      onkeydown: function (ev) {
        if (ev.key === 'Enter') {
          state.extbinQuery = qEl.value;
          state.focusId = 'extbin-q';
          extbinReloadPage(1);
        }
      }
    }, []);
    var searchBtn = h('button', {
      class: 'btn ghost', onclick: function () {
        state.extbinQuery = qEl.value;
        extbinReloadPage(1);
      }
    }, ['搜索']);
    if (state.extbinQuery) {
      searchBtn = h('button', {
        class: 'btn ghost', onclick: function () {
          state.extbinQuery = '';
          qEl.value = '';
          extbinReloadPage(1);
        }
      }, ['清除']);
    }

    // 服务端已分页，draft 即当前页；下标用于删除定位。
    var page = (d && d.page) || 1;
    var pages = (d && d.pages) || 1;
    var total = (d && d.total) || draft.length;
    var q = (state.extbinQuery || '').trim();

    var rows = draft.map(function (row, idx) {
      var nameId = 'extbin-name-' + idx;
      var urlId = 'extbin-url-' + idx;
      var nameEl = h('input', {
        id: nameId, value: row.name || '', placeholder: '如 custom-tool',
        oninput: function () { row.name = nameEl.value; }
      }, []);
      var urlEl = h('input', {
        id: urlId, value: row.url || '', placeholder: 'https://github.com/.../tool.exe',
        oninput: function () { row.url = urlEl.value; }
      }, []);
      var scriptEl = h('input', {
        type: 'checkbox', checked: !!row.script,
        onchange: function () { row.script = scriptEl.checked; }
      }, []);
      var testOut = h('div', { class: 'muted' }, []);
      var platformURL = row.platform_url || ('/api/external/binary/' + (row.name || '<name>'));
      var fileEl = h('input', { type: 'file', style: 'display:none' }, []);
      fileEl.onchange = function () {
        var f = fileEl.files && fileEl.files[0];
        if (!f) return;
        var nm = (row.name || '').trim();
        if (!nm) { alert('请先保存白名单条目'); fileEl.value = ''; return; }
        if (!confirm('上传 ' + f.name + ' 作为 ' + nm + ' 的离线文件？\n上传后平台将优先返回该文件。')) {
          fileEl.value = '';
          return;
        }
        var fd = new FormData();
        fd.append('file', f);
        testOut.textContent = '上传中…';
        api('/api/external/binaries/' + encodeURIComponent(nm) + '/upload', {
          method: 'POST', body: fd
        }).then(function () {
          testOut.textContent = '';
          state.extbin = null;
          state.extbinDraft = null;
          loadExternalBinaries();
        }).catch(function (e) {
          testOut.textContent = '';
          testOut.appendChild(h('span', { class: 'tag tunnel-fail' }, ['✗ ' + e.message]));
          fileEl.value = '';
        });
      };

      return h('tr', {}, [
        h('td', {}, [nameEl]),
        h('td', {}, [urlEl]),
        h('td', {}, [h('label', { class: 'check' }, [scriptEl, ' 脚本'])]),
        h('td', {}, [h('div', { class: 'muted' }, [platformURL])]),
        h('td', {}, [row.cached
          ? h('span', { class: 'tag status-success' }, ['已缓存 ' + fmtBytes(row.cached_size)])
          : h('span', { class: 'muted' }, ['未缓存'])]),
        h('td', {}, [row.local
          ? h('span', { class: 'tag tunnel-ok' }, ['本地 ' + fmtBytes(row.local_size)])
          : h('span', { class: 'muted' }, ['-'])]),
        h('td', {}, [h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              testOut.textContent = '测试中…';
              api('/api/external/binaries/test', {
                method: 'POST',
                body: JSON.stringify({ name: (nameEl.value || '').trim(), url: (urlEl.value || '').trim() })
              }).then(function (r) {
                testOut.textContent = '';
                if (r.ok) {
                  testOut.appendChild(h('span', { class: 'tag tunnel-ok' }, [
                    '✓ 可用' + (r.status_code ? '（HTTP ' + r.status_code + '）' : '') +
                    '，大小 ' + fmtBytes(r.size) + '，耗时 ' + r.elapsed_ms + 'ms']));
                } else {
                  testOut.appendChild(h('span', { class: 'tag tunnel-fail' }, ['✗ ' + (r.error || '测试失败')]));
                  if (r.hint) testOut.appendChild(h('div', { class: 'muted' }, [r.hint]));
                }
              }).catch(function (e) {
                testOut.textContent = '';
                testOut.appendChild(h('span', { class: 'tag tunnel-fail' }, ['✗ ' + e.message]));
              });
            }
          }, ['测试']),
          h('button', {
            class: 'btn ghost small', onclick: function () {
              var text = (nameEl.value || '').trim()
                ? (window.location.origin + platformURL)
                : platformURL;
              copyText(text, this);
            }
          }, ['复制地址']),
          h('button', {
            class: 'btn ghost small', onclick: function () {
              if (!(nameEl.value || '').trim()) {
                alert('请先填写名称，并先点「保存」再刷新缓存');
                return;
              }
              if (!confirm('强制重新下载 ' + nameEl.value.trim() + ' 的缓存？上游更新后主机将立即拿到新文件。')) return;
              testOut.textContent = '重新下载中…';
              api('/api/external/binaries/refresh', {
                method: 'POST',
                body: JSON.stringify({ name: (nameEl.value || '').trim() })
              }).then(function (r) {
                testOut.textContent = '';
                testOut.appendChild(h('span', { class: 'tag tunnel-ok' }, [
                  '✓ 已刷新 ' + fmtBytes(r.size) + '（' + r.elapsed_ms + 'ms）']));
                state.extbin = null;
                state.extbinDraft = null;
                loadExternalBinaries();
              }).catch(function (e) {
                testOut.textContent = '';
                testOut.appendChild(h('span', { class: 'tag tunnel-fail' }, ['✗ ' + e.message]));
              });
            }
          }, ['刷新缓存']),
          h('button', {
            class: 'btn ghost small', onclick: function () {
              fileEl.click();
            }
          }, ['上传离线文件']),
          row.local ? h('button', {
            class: 'btn ghost small', onclick: function () {
              if (!confirm('清除 ' + row.name + ' 的离线文件？之后将回退到平台缓存/上游下载。')) return;
              api('/api/external/binaries/' + encodeURIComponent((row.name || '').trim()) + '/upload', {
                method: 'DELETE'
              }).then(function () {
                state.extbin = null;
                state.extbinDraft = null;
                loadExternalBinaries();
              }).catch(function (e) { alert('清除失败: ' + e.message); });
            }
          }, ['清除离线文件']) : null,
          h('button', {
            class: 'btn danger small', onclick: function () {
              var nm = (row.name || '').trim();
              if (nm && row.saved) {
                // 已保存过的条目：记录下来，保存时从白名单删除。
                state.extbinDeleted = (state.extbinDeleted || []).concat([nm]);
              }
              draft.splice(idx, 1);
              render();
            }
          }, ['删除']),
          fileEl
        ]), testOut])
      ]);
    });

    var doSave = function () {
      var payload = {
        // merge：只把当前页改动合并进白名单，不会清空其它页。
        merge: true,
        items: draft.map(function (r) {
          return { name: (r.name || '').trim(), url: (r.url || '').trim(), script: !!r.script };
        }).filter(function (x) { return x.name && x.url; }),
        deleted: state.extbinDeleted || [],
        hosts: hostsEl.value.split(',').map(function (x) { return x.trim(); })
          .filter(function (x) { return x; }),
        ttl: ttlEl.value.trim(),
        allow_private: allowPrivateEl.checked,
        allow_cidrs: cidrsEl.value.split(',').map(function (x) { return x.trim(); })
          .filter(function (x) { return x; })
      };
      state.extbinBusy = true;
      state.extbinErr = '';
      render();
      api('/api/external/binaries', { method: 'PUT', body: JSON.stringify(payload) })
        .then(function () {
          state.extbinBusy = false;
          state.extbin = null;
          state.extbinDraft = null;
          loadExternalBinaries();
        })
        .catch(function (e) {
          state.extbinBusy = false;
          state.extbinErr = e.message;
          render();
        });
    };

    var pager = null;
    if (pages > 1 || total > 0) {
      pager = h('div', { class: 'pager' }, [
        h('button', {
          class: 'btn ghost small', disabled: page <= 1 ? 'disabled' : null,
          onclick: function () { extbinReloadPage(page - 1); }
        }, ['上一页']),
        h('span', { class: 'muted' }, ['第 ' + page + ' / ' + pages + ' 页，共 ' + total + ' 条']),
        h('button', {
          class: 'btn ghost small', disabled: page >= pages ? 'disabled' : null,
          onclick: function () { extbinReloadPage(page + 1); }
        }, ['下一页'])
      ]);
    }

    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['外部文件代缓存']),
        hint('由控制台代下载公网/内网文件，目标主机通过 http 从「平台地址」拉取。' +
          '用于老系统（Windows Server 2008 R2 + PowerShell 2.0 不支持 TLS 1.2）' +
          '或无外网出口的场景。白名单保存后立即生效，无需重启。'),
        d && d.source === 'default'
          ? hint('当前使用内置默认白名单（windows_exporter amd64/arm64 + install.ps1）；保存后以本页配置为准。')
          : null,
        d && d.dir ? hint('缓存目录：' + d.dir + '/.external-cache') : null,
        state.extbinErr ? h('div', { class: 'err field', role: 'alert' }, [state.extbinErr]) : null
      ]),
      h('div', { class: 'card' }, [
        h('div', { class: 'toolbar' }, [
          h('button', {
            class: 'btn', onclick: function () {
              // 仅在当前页追加（不重置搜索/页码），保存时按 merge 合并。
              state.extbinDraft = draft.concat([{ name: '', url: '', script: false, saved: false }]);
              render();
            }
          }, ['新增一行']),
          h('button', {
            class: 'btn', disabled: state.extbinBusy ? 'disabled' : null, onclick: doSave
          }, [state.extbinBusy ? '保存中…' : '保存']),
          qEl,
          searchBtn,
          h('button', {
            class: 'btn ghost', onclick: function () {
              if (!confirm('重新下载全部条目的缓存？条目较多时耗时较长。')) return;
              state.extbinBusy = true;
              render();
              api('/api/external/binaries/refresh-all', { method: 'POST', body: '{}' })
                .then(function (r) {
                  state.extbinBusy = false;
                  alert('批量刷新完成：成功 ' + r.ok + ' 条，失败 ' + r.failed + ' 条' +
                    (r.failed ? '\n\n' + (r.items || []).filter(function (x) { return !x.ok; })
                      .slice(0, 8).map(function (x) { return x.name + '：' + x.error; }).join('\n') : ''));
                  extbinReloadPage(page);
                })
                .catch(function (e) {
                  state.extbinBusy = false;
                  alert('批量刷新失败: ' + e.message);
                  render();
                });
            }
          }, ['批量刷新']),
          h('span', { class: 'muted' }, ['共 ' + total + ' 条' + (q ? '（搜索：' + q + '）' : '')]),
          h('span', { class: 'muted' }, ['保存后立即生效（热加载）'])
        ]),
        hint('平台无法出网（如 Windows Server 2008 R2 无 TLS 1.2）时：在本机下载 exe/脚本，' +
          '点对应行的「上传离线文件」；/api/external/binary 会优先返回本地文件，平台无需访问 GitHub。'),
        h('div', { class: 'table-wrap' }, [h('table', {}, [
          h('thead', {}, [h('tr', {}, [
            h('th', {}, ['名称 (name)']), h('th', {}, ['上游地址']), h('th', {}, ['类型']),
            h('th', {}, ['平台地址']), h('th', {}, ['缓存']), h('th', {}, ['本地离线']),
            h('th', {}, ['操作'])
          ])]),
          h('tbody', {}, rows)
        ])]),
        draft.length === 0 ? h('div', { class: 'muted empty' }, ['还没有条目，点「新增一行」添加。']) : null,
        draft.length === 0 && q
          ? h('div', { class: 'muted empty' }, ['没有匹配的条目。']) : null,
        pager
      ]),
      h('div', { class: 'card' }, [
        h('h3', {}, ['安全设置']),
        h('div', { class: 'grid2' }, [
          field('允许的上游域名', hostsEl, '逗号分隔。只允许这些域名被代下载；留空用默认 github.com / raw.githubusercontent.com / release-assets.githubusercontent.com / proxy.xiaonuo.live。内网镜像需把其域名/主机加进来。'),
          field('缓存有效期', ttlEl, '如 24h / 30m。过期后下次请求重新下载。')
        ]),
        h('div', { class: 'grid2' }, [
          h('div', { class: 'field' }, [
            h('label', { class: 'check' }, [allowPrivateEl, ' 允许访问内网/环回地址']),
            hint('内网镜像站（如 http://nexus.corp:8081）的域名解析到私网 IP 时需要。建议优先用下方「放行网段」精确放开。')
          ]),
          field('放行网段', cidrsEl, '逗号分隔，如 10.0.0.0/8,192.168.1.10。仅这些内网地址可被代下载，其余内网地址仍拒绝。')
        ]),
        hint('⚠️ 代下载是服务端主动出网能力：只放行确实需要的域名与网段，避免被当作访问内网或云元数据的跳板。')
      ])
    ]);
  }

  // ---------- 人员组织（用户字段 / 角色 / 团队 / 服务组） ----------
  //
  // 与 consul_mgr 模型对齐：团队是权限主体，按「服务组」授权；
  // 服务组 = 组件集合；用户与主机均支持自定义字段（字段定义存库，零硬编码）。
  function loadOrg() {
    state.orgErr = '';
    var jobs = [
      api('/api/users/fields'),
      api('/api/roles'),
      api('/api/teams'),
      api('/api/service-groups'),
      api('/api/host-groups')
    ];
    // 用显式下标，避免「组件已加载/未加载」时后续任务错位。
    var compIdx = -1;
    if (!(state.components || []).length) {
      compIdx = jobs.length;
      jobs.push(api('/api/components'));
    }
    var deptIdx = jobs.length;
    jobs.push(api('/api/org/departments'));
    // 主机字段定义：与用户字段同样支持页面增删改（后端 /api/field-defs/host）。
    var hostDefIdx = jobs.length;
    jobs.push(api('/api/field-defs/host'));
    // 主机字段映射：外部来源键 → 本地字段，零硬编码（后端 /api/host-field-mappings）。
    var hostMapIdx = jobs.length;
    jobs.push(api('/api/host-field-mappings'));
    Promise.all(jobs).then(function (res) {
      if (compIdx >= 0) state.components = res[compIdx] || [];
      state.hostGroupOptions = (res[4] && res[4].groups) || [];
      // 主机字段定义同时回填给主机页，保证两处列表/表单看到同一份定义。
      state.hostFieldDefs = res[hostDefIdx] || [];
      var maps = res[hostMapIdx] || {};
      state.org = {
        users: res[0].users || [],
        userDefs: res[0].defs || [],
        userValues: res[0].values || {},
        roles: res[1] || [],
        teams: res[2] || [],
        groups: res[3] || [],
        depts: res[deptIdx] || [],
        hostDefs: res[hostDefIdx] || [],
        hostMaps: maps.mappings || [],
        // 来源/内置列/保留键均由后端给出，页面不硬编码任何键名。
        mapSources: maps.sources || [],
        mapColumns: maps.columns || [],
        mapReserved: maps.reserved_keys || {}
      };
      render();
    }).catch(function (e) {
      state.orgErr = e.message;
      render();
    });
  }

  // switchOrgTab 切换人员组织子页签，并清空所有子表单状态。
  //
  // 「用户字段」与「主机字段」共用 state.orgNewField，必须在切换时清空，
  // 否则在一个页签打开表单后切到另一个，会把表单带过去并提交到错误的 kind。
  function switchOrgTab(tab) {
    state.orgTab = tab;
    state.orgUserForm = null;
    state.orgRoleForm = null;
    state.orgGroupForm = null;
    state.orgNewField = null;
    state.orgMapForm = null;
    state.orgScan = null;
    state.orgSel = null;
    state.orgSelTeam = null;
    render();
  }

  function orgTabs() {
    var tabs = [['users', '用户管理'], ['fields', '用户字段'], ['hostfields', '主机字段'],
      ['hostmaps', '主机字段映射'],
      ['teams', '团队管理'], ['roles', '角色管理'], ['groups', '服务组'],
      ['depts', '组织架构'], ['flyiam', 'FlyIAM 人员']];
    return h('div', { class: 'tabs', role: 'tablist', style: 'margin-bottom:12px' }, tabs.map(function (t) {
      return h('div', {
        class: 'tab' + (state.orgTab === t[0] ? ' active' : ''),
        role: 'tab',
        'aria-selected': state.orgTab === t[0] ? 'true' : 'false',
        tabindex: '0',
        onclick: function () {
          switchOrgTab(t[0]);
        },
        onkeydown: function (e) {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); switchOrgTab(t[0]); }
        }
      }, [t[1]]);
    }));
  }

  function orgView() {
    var o = state.org;
    if (!o && !state.orgErr) {
      return h('div', { class: 'card' }, [h('h3', {}, ['人员组织']), h('div', { class: 'muted' }, ['加载中…'])]);
    }
    var body;
    if (state.orgTab === 'users') body = orgUsers(o);
    else if (state.orgTab === 'depts') body = orgDepts(o);
    else if (state.orgTab === 'fields') body = orgUserFields(o);
    else if (state.orgTab === 'hostfields') body = orgHostFields(o);
    else if (state.orgTab === 'hostmaps') body = orgHostMaps(o);
    else if (state.orgTab === 'roles') body = orgRoles(o);
    else if (state.orgTab === 'teams') body = orgTeams(o);
    else if (state.orgTab === 'flyiam') body = orgFlyIAMUsers(o);
    else body = orgGroups(o);

    return h('div', {}, [
      orgTabs(),
      state.orgErr ? h('div', { class: 'card' }, [h('div', { class: 'err', role: 'alert' }, [state.orgErr])]) : null,
      body
    ]);
  }

  // 人员信息：账号管理（新增/编辑/删除/重置密码/禁用）+ 人事扩展字段。
  // 与 consul_mgr 的「用户管理」布局对齐：工具栏（搜索/新增/刷新）+ 表格 + 弹窗表单。
  function orgUsers(o) {
    var q = (state.orgUserQ || '').trim().toLowerCase();
    var users = o.users.filter(function (u) {
      if (!q) return true;
      return (u.username || '').toLowerCase().indexOf(q) >= 0 ||
        (u.display_name || '').toLowerCase().indexOf(q) >= 0 ||
        (u.roles || []).join(',').toLowerCase().indexOf(q) >= 0;
    });
    var listDefs = o.userDefs.filter(function (d) { return d.showInList; })
      .sort(function (a, b) { return (a.sortOrder || 0) - (b.sortOrder || 0); });
    var cols = ['用户', '租户', '角色', '状态']
      .concat(listDefs.map(function (d) { return d.label; }))
      .concat(['操作']);

    var rows = users.map(function (u) {
      var vals = o.userValues[u.username] || {};
      var roleTags = (u.roles || []).map(function (r) {
        return h('span', { class: 'tag' + (r === 'superadmin' ? ' status-failed' : (r === 'admin' ? ' status-running' : '')) }, [r]);
      });
      var cells = [
        h('div', { class: 'user-cell' }, [
          h('span', { class: 'avatar sm' }, [avatarInitial(u.display_name || u.username)]),
          h('div', {}, [
            h('div', { class: 'user-cell-name' }, [u.display_name || u.username]),
            h('div', { class: 'muted user-cell-sub' }, [u.username])
          ])
        ]),
        u.tenant_id,
        h('div', { class: 'row-actions' }, roleTags.length ? roleTags : ['-']),
        u.disabled
          ? h('span', { class: 'tag status-failed' }, ['已禁用'])
          : h('span', { class: 'tag status-success' }, ['正常'])
      ].concat(listDefs.map(function (d) { return vals[d.fieldKey] || '-'; }));

      var actions = [
        h('button', {
          class: 'btn ghost small', onclick: function () { state.orgSel = u.username; state.orgUserForm = null; render(); }
        }, ['扩展字段']),
        h('button', {
          class: 'btn ghost small', onclick: function () { openUserForm(u); }
        }, ['编辑']),
        h('button', {
          class: 'btn ghost small', onclick: function () { resetUserPassword(u); }
        }, ['重置密码']),
        h('button', {
          class: 'btn ghost small', onclick: function () { toggleUserDisabled(u); }
        }, [u.disabled ? '启用' : '禁用']),
        h('button', {
          class: 'btn danger small', onclick: function () { deleteUser(u); }
        }, ['删除'])
      ];
      cells.push(h('div', { class: 'row-actions' }, actions));
      return cells;
    });

    var qEl = h('input', {
      id: 'org-user-q', value: state.orgUserQ || '', placeholder: '用户名 / 显示名称 / 角色',
      oninput: function () {
        state.orgUserQ = qEl.value;
        state.focusId = 'org-user-q';
        render();
      }
    }, []);

    var toolbar = h('div', { class: 'toolbar' }, [
      qEl,
      h('button', {
        class: 'btn', onclick: function () {
          state.orgUserForm = { mode: 'create', username: '', display_name: '', password: '', tenant_id: 1, roles: ['user'], disabled: false };
          state.orgSel = null;
          render();
        }
      }, ['新增用户']),
      h('button', { class: 'btn ghost', onclick: function () { state.orgUserForm = null; loadOrg(); } }, ['刷新']),
      h('span', { class: 'muted' }, ['共 ' + users.length + ' 个用户' +
        (q ? '（全部 ' + o.users.length + '）' : '')])
    ]);

    var editing = o.users.filter(function (u) { return u.username === state.orgSel; })[0];
    var form = editing ? userFieldForm(o, editing) : null;
    return h('div', {}, [
      h('div', { class: 'card' }, [
        toolbar,
        simpleTable(cols, users, function (u, i) { return rows[i]; }),
        users.length === 0 ? h('div', { class: 'muted empty' }, ['没有匹配的用户']) : null
      ]),
      state.orgUserForm ? userAccountForm(o) : null,
      form
    ]);
  }

  // isSuperAdmin 已在文件上方定义；rolesForEdit 生成可分配角色列表。
  function rolesForEdit() {
    var roles = [{ value: 'user', label: 'user（普通用户）' }, { value: 'admin', label: 'admin（租户管理员）' }];
    if (isSuperAdmin()) roles.push({ value: 'superadmin', label: 'superadmin（跨租户）' });
    return roles;
  }

  // openUserForm 打开账号编辑表单。
  function openUserForm(u) {
    state.orgUserForm = {
      mode: 'edit', username: u.username, display_name: u.display_name || '',
      password: '', tenant_id: u.tenant_id || 1, roles: (u.roles || ['user']).slice(),
      disabled: !!u.disabled
    };
    state.orgSel = null;
    render();
  }

  // userAccountForm 新增/编辑账号（对齐 consul_mgr 的弹窗表单，这里以卡片内嵌）。
  function userAccountForm(o) {
    var f = state.orgUserForm;
    var isCreate = f.mode === 'create';
    var nameEl = h('input', { value: f.username || '', placeholder: '唯一登录名，如 zhangsan', disabled: isCreate ? null : 'disabled' }, []);
    var dispEl = h('input', { value: f.display_name || '', placeholder: '姓名/显示名称' }, []);
    var pwdEl = h('input', {
      type: 'password',
      placeholder: isCreate ? '至少 8 位；留空则系统生成' : '留空表示不修改密码'
    }, []);
    var tenantEl = h('input', { type: 'number', min: '1', value: String(f.tenant_id || 1) }, []);
    var disabledEl = h('input', { type: 'checkbox', checked: !!f.disabled }, []);

    var roleEls = {};
    var roleBoxes = rolesForEdit().map(function (r) {
      var cb = h('input', { type: 'checkbox', checked: (f.roles || []).indexOf(r.value) >= 0 }, []);
      roleEls[r.value] = cb;
      return h('label', { class: 'check' }, [cb, ' ' + r.label]);
    });

    function save() {
      var username = (nameEl.value || '').trim();
      if (!username) { alert('请填写用户名'); return; }
      var roles = Object.keys(roleEls).filter(function (k) { return roleEls[k].checked; });
      if (!roles.length) roles = ['user'];
      var tenantID = parseInt(tenantEl.value, 10) || 1;
      if (isCreate) {
        var payload = {
          username: username, display_name: dispEl.value.trim(),
          password: pwdEl.value, tenant_id: tenantID, roles: roles
        };
        api('/api/users', { method: 'POST', body: JSON.stringify(payload) })
          .then(function (r) {
            var pwd = pwdEl.value;
            state.orgUserForm = null;
            loadOrg();
            if (!pwd && r && r.initial_password) {
              alert('用户已创建。初始密码（仅显示一次）：' + r.initial_password);
            }
          })
          .catch(function (e) { alert('创建失败: ' + e.message); });
        return;
      }
      // 编辑：先改租户/角色，再按需改禁用状态与密码。
      api('/api/users/' + encodeURIComponent(username) + '/tenant-roles', {
        method: 'PUT', body: JSON.stringify({ tenant_id: tenantID, roles: roles })
      }).then(function () {
        var jobs = [];
        if (pwdEl.value) {
          jobs.push(api('/api/users/' + encodeURIComponent(username) + '/reset-password', {
            method: 'POST', body: JSON.stringify({ password: pwdEl.value })
          }));
        }
        jobs.push(api('/api/users/' + encodeURIComponent(username) + '/disabled', {
          method: 'PUT', body: JSON.stringify({ disabled: !!disabledEl.checked })
        }));
        return Promise.all(jobs);
      }).then(function () {
        state.orgUserForm = null;
        loadOrg();
      }).catch(function (e) { alert('保存失败: ' + e.message); });
    }

    var tenantField = isSuperAdmin()
      ? field('租户 ID', tenantEl, '仅 superadmin 可跨租户分配')
      : field('租户 ID', tenantEl, '当前账号只能管理本租户用户');
    if (!isSuperAdmin()) tenantEl.setAttribute('disabled', 'disabled');

    return h('div', { class: 'card' }, [
      h('h3', {}, [isCreate ? '新增用户' : ('编辑用户：' + f.username)]),
      h('div', { class: 'grid2' }, [
        field('用户名', nameEl, '创建后不可修改'),
        field('显示名称', dispEl, '')
      ]),
      h('div', { class: 'grid2' }, [
        field(isCreate ? '初始密码' : '新密码', pwdEl, '至少 8 位；留空则不改'),
        tenantField
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['角色']),
        h('div', { class: 'check-group' }, roleBoxes),
        hint('admin 可管理本租户；superadmin 可跨租户并创建租户。')
      ]),
      !isCreate ? h('div', { class: 'field' }, [
        h('label', { class: 'check' }, [disabledEl, ' 禁用该账号'])
      ]) : null,
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', { class: 'btn ghost', onclick: function () { state.orgUserForm = null; render(); } }, ['取消'])
      ])
    ]);
  }

  function resetUserPassword(u) {
    var pwd = prompt('为 ' + u.username + ' 设置新密码（至少 8 位）');
    if (pwd === null) return;
    if (pwd.length < 8) { alert('密码不能少于 8 位'); return; }
    api('/api/users/' + encodeURIComponent(u.username) + '/reset-password', {
      method: 'POST', body: JSON.stringify({ password: pwd })
    }).then(function () { alert('密码已重置，该用户的旧会话会立即失效'); })
      .catch(function (e) { alert('重置失败: ' + e.message); });
  }

  function toggleUserDisabled(u) {
    var next = !u.disabled;
    if (next && !confirm('确定禁用 ' + u.username + '？禁用后该账号无法登录。')) return;
    api('/api/users/' + encodeURIComponent(u.username) + '/disabled', {
      method: 'PUT', body: JSON.stringify({ disabled: next })
    }).then(function () { loadOrg(); })
      .catch(function (e) { alert('操作失败: ' + e.message); });
  }

  function deleteUser(u) {
    if (!confirm('确定删除用户 ' + u.username + '？\n\n将同时移除其团队成员关系与扩展字段值；审计日志保留。')) return;
    api('/api/users/' + encodeURIComponent(u.username), { method: 'DELETE' })
      .then(function () { state.orgSel = null; loadOrg(); })
      .catch(function (e) { alert('删除失败: ' + e.message); });
  }

  // userFieldForm 编辑某用户的扩展字段值。
  function userFieldForm(o, user) {
    var vals = Object.assign({}, o.userValues[user.username] || {});
    var inputs = {};
    var fields = o.userDefs.filter(function (d) { return d.showInForm && d.editable; })
      .sort(function (a, b) { return (a.sortOrder || 0) - (b.sortOrder || 0); });
    var controls = fields.map(function (d) {
      var el;
      if (d.fieldType === 'select') {
        var opts = [];
        try { opts = JSON.parse(d.options || '[]'); } catch (e) { opts = []; }
        el = h('select', {}, [h('option', { value: '' }, ['（未设置）'])].concat(opts.map(function (x) {
          return h('option', { value: x }, [x]);
        })));
        el.value = vals[d.fieldKey] || '';
      } else if (d.fieldType === 'textarea') {
        el = h('textarea', { rows: '3' }, []);
        el.value = vals[d.fieldKey] || '';
      } else {
        el = h('input', {
          value: vals[d.fieldKey] || '',
          type: d.fieldType === 'number' ? 'number' : (d.fieldType === 'date' ? 'date' : 'text')
        }, []);
      }
      inputs[d.fieldKey] = el;
      return field(d.label, el, '');
    });

    var save = function () {
      var values = {};
      Object.keys(inputs).forEach(function (k) { values[k] = inputs[k].value; });
      api('/api/users/' + encodeURIComponent(user.username) + '/fields', {
        method: 'PUT', body: JSON.stringify({ values: values })
      }).then(function () {
        state.orgSel = null;
        loadOrg();
      }).catch(function (e) { alert('保存失败: ' + e.message); });
    };

    return h('div', { class: 'card' }, [
      h('h3', {}, ['编辑人员字段：' + user.username]),
      h('div', { class: 'grid2' }, controls),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', {
          class: 'btn ghost', onclick: function () { state.orgSel = null; render(); }
        }, ['取消'])
      ])
    ]);
  }

  // 用户字段定义管理（增删改）。
  function orgUserFields(o) {
    var defs = o.userDefs.slice().sort(function (a, b) { return (a.sortOrder || 0) - (b.sortOrder || 0); });
    var rows = defs.map(function (d) {
      return [
        d.fieldKey, d.label, d.fieldType,
        d.showInList ? '是' : '否', d.showInForm ? '是' : '否',
        d.builtin ? '内置' : '自定义',
        d.builtin ? h('span', { class: 'muted' }, ['不可删除'])
          : (isSuperAdmin() ? h('button', {
            class: 'btn danger small', onclick: function () {
              if (!confirm('删除字段 ' + d.label + '？已填写的值也会一并删除。')) return;
              api('/api/field-defs/user/' + d.id, { method: 'DELETE' })
                .then(loadOrg).catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除']) : h('span', { class: 'muted' }, ['仅 superadmin 可管理']))
      ];
    });
    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['用户字段定义']),
        hint('用于人员信息（人事字段）。字段键需与 FlyIAM 一致才能接收其同步数据；' +
          '可在「设置 → FlyIAM」配置地址与令牌后点「同步字段」自动对齐。'),
        isSuperAdmin() ? h('div', { class: 'toolbar' }, [
          h('button', {
            class: 'btn', onclick: function () { state.orgNewField = {}; render(); }
          }, ['新增字段']),
          h('button', {
            class: 'btn ghost', onclick: function () {
              api('/api/flyiam/sync-user-fields', { method: 'POST', body: '{}' })
                .then(function (r) {
                  alert('同步完成：新增 ' + r.added + '，更新 ' + r.updated + '，共 ' + r.total);
                  loadOrg();
                })
                .catch(function (e) { alert('同步失败: ' + e.message); });
            }
          }, ['从 FlyIAM 同步字段'])
        ]) : hint('字段定义是全局配置，仅 superadmin 可修改。'),
        simpleTable(['键', '名称', '类型', '列表显示', '表单显示', '来源', '操作'], defs, function (d, i) {
          return rows[i];
        }),
        state.orgNewField !== undefined && state.orgNewField !== null ? newFieldDefForm('user') : null
      ])
    ]);
  }

  // 主机字段定义管理（增删改）。
  //
  // 与「用户字段」同构，复用 newFieldDefForm/后端 /api/field-defs/host。
  // 值存在 hosts.custom_fields(JSONB)，由主机列表列与编辑表单共用：
  // showInList 控制是否作为列表列，showInForm 控制是否出现在编辑主机表单。
  function orgHostFields(o) {
    var defs = (o.hostDefs || []).slice().sort(function (a, b) {
      return (a.sortOrder || 0) - (b.sortOrder || 0);
    });
    var rows = defs.map(function (d) {
      return [
        d.fieldKey, d.label, d.fieldType,
        d.showInList ? '是' : '否', d.showInForm ? '是' : '否',
        d.builtin ? '内置' : '自定义',
        isSuperAdmin() ? h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.orgNewField = Object.assign({}, d);
              render();
            }
          }, ['编辑']),
          d.builtin ? h('span', { class: 'muted' }, ['不可删除'])
            : h('button', {
              class: 'btn danger small', onclick: function () {
                if (!confirm('删除主机字段「' + d.label + '」？各主机已填写的该字段值也会一并删除。')) return;
                api('/api/field-defs/host/' + d.id, { method: 'DELETE' })
                  .then(function () {
                    // 主机页缓存的定义同步失效，避免列表仍按旧定义渲染列。
                    state.hostFieldDefs = undefined;
                    loadOrg();
                  })
                  .catch(function (e) { alert('删除失败: ' + e.message); });
              }
            }, ['删除'])
        ]) : h('span', { class: 'muted' }, [d.builtin ? '内置' : '仅 superadmin 可管理'])
      ];
    });
    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['主机字段定义']),
        hint('为主机增加自定义字段（如机房、负责人、资产编号）。字段值在「主机 → 编辑」中填写，' +
          '勾选「在列表显示」后会作为主机列表的一列；字段键用于 API 与 CSV 导出，建议用英文。'),
        h('div', { class: 'toolbar' }, [
          isSuperAdmin() ? h('button', {
            class: 'btn', onclick: function () { state.orgNewField = {}; render(); }
          }, ['新增字段']) : null,
          h('button', {
            class: 'btn ghost', onclick: function () {
              state.hostFieldDefs = undefined;
              loadOrg();
            }
          }, ['刷新'])
        ]),
        defs.length ? simpleTable(['键', '名称', '类型', '列表显示', '表单显示', '来源', '操作'], defs, function (d, i) {
          return rows[i];
        }) : h('div', { class: 'muted empty' }, ['还没有主机自定义字段，点「新增字段」添加。']),
        state.orgNewField !== undefined && state.orgNewField !== null ? newFieldDefForm('host') : null
      ])
    ]);
  }

  // newFieldDefForm 字段定义表单（用户/主机共用；带 id 为编辑，否则新增）。
  function newFieldDefForm(kind) {
    var f = state.orgNewField || {};
    var editing = !!f.id;
    // 编辑时锁定字段键：改键不会迁移已填写的值（用户值表 / 主机 JSONB），
    // 会把历史数据变成孤儿。需要换键时请新建字段。
    var keyEl = h('input', {
      value: f.fieldKey || '', placeholder: '如 empCode',
      disabled: editing ? 'disabled' : null
    }, []);
    var labelEl = h('input', { value: f.label || '', placeholder: '如 工号' }, []);
    var typeEl = h('select', {}, [
      h('option', { value: 'text' }, ['text']),
      h('option', { value: 'textarea' }, ['textarea']),
      h('option', { value: 'number' }, ['number']),
      h('option', { value: 'select' }, ['select']),
      h('option', { value: 'date' }, ['date'])
    ]);
    typeEl.value = f.fieldType || 'text';
    var optionsEl = h('input', { value: f.options || '', placeholder: '仅 select 需要，JSON 数组如 ["A","B"]' }, []);
    var listEl = h('input', { type: 'checkbox', checked: f.showInList !== false }, []);
    var formEl = h('input', { type: 'checkbox', checked: f.showInForm !== false }, []);
    var orderEl = h('input', { type: 'number', value: f.sortOrder || 100 }, []);

    var save = function () {
      var payload = {
        fieldKey: keyEl.value.trim(),
        label: labelEl.value.trim(),
        fieldType: typeEl.value,
        options: typeEl.value === 'select' ? optionsEl.value.trim() : '',
        showInList: listEl.checked,
        showInForm: formEl.checked,
        editable: f.editable !== false,
        sortOrder: parseInt(orderEl.value, 10) || 0
      };
      if (!payload.fieldKey || !payload.label) {
        alert('字段键与显示名称不能为空');
        return;
      }
      var req = editing
        ? api('/api/field-defs/' + kind + '/' + f.id, { method: 'PUT', body: JSON.stringify(payload) })
        : api('/api/field-defs/' + kind, { method: 'POST', body: JSON.stringify(payload) });
      req.then(function () {
        state.orgNewField = null;
        // 主机字段定义被主机页缓存，改动后置空以便下次进入重新拉取。
        if (kind === 'host') state.hostFieldDefs = undefined;
        loadOrg();
      }).catch(function (e) { alert((editing ? '保存失败: ' : '新增失败: ') + e.message); });
    };

    return h('div', { class: 'card' }, [
      h('h3', {}, [editing ? '编辑字段' : '新增字段']),
      h('div', { class: 'grid2' }, [
        field('字段键 (fieldKey)', keyEl,
          editing ? '创建后不可修改；需要换键请新建字段' : '字母开头，仅字母数字下划线'),
        field('显示名称', labelEl, '')
      ]),
      h('div', { class: 'grid2' }, [
        field('类型', typeEl, ''),
        field('选项（select）', optionsEl, '')
      ]),
      h('div', { class: 'grid2' }, [
        h('div', { class: 'field' }, [h('label', { class: 'check' }, [listEl, ' 在列表显示'])]),
        h('div', { class: 'field' }, [h('label', { class: 'check' }, [formEl, ' 在表单显示'])])
      ]),
      field('排序', orderEl, '数值越小越靠前'),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', {
          class: 'btn ghost', onclick: function () { state.orgNewField = null; render(); }
        }, ['取消'])
      ])
    ]);
  }

  // 主机字段映射：外部来源键 → 本地字段（内置列或自定义字段）。
  //
  // 零硬编码：来源列表、可选内置列、平台保留键全部来自后端
  // /api/host-field-mappings 的响应，新增来源或列不需要改前端。
  function orgHostMaps(o) {
    var maps = (o.hostMaps || []).slice();
    var columns = o.mapColumns || [];
    var reserved = o.mapReserved || {};
    // 内置列的中文名查表，表格里展示「业务组 (biz_group)」这种可读形式。
    var colLabel = {};
    columns.forEach(function (c) { colLabel[c.key] = c.label; });

    var targetText = function (m) {
      if (m.targetKind === 'column') {
        var lbl = colLabel[m.targetKey];
        return '内置列 ' + (lbl ? lbl + ' (' + m.targetKey + ')' : m.targetKey);
      }
      return '自定义字段 ' + m.targetKey;
    };

    var reload = function () {
      state.orgMapForm = null;
      state.orgMapScan = null;
      loadOrg();
    };

    var rows = maps.map(function (m) {
      return [
        m.source,
        h('div', {}, [
          h('div', {}, [m.sourceKey]),
          reserved[m.sourceKey] ? h('div', { class: 'muted' }, [reserved[m.sourceKey]]) : null
        ]),
        targetText(m),
        m.overwrite ? '覆盖' : '仅补空',
        m.enabled ? h('span', { class: 'tag status-success' }, ['启用'])
          : h('span', { class: 'tag' }, ['停用']),
        String(m.sortOrder || 0),
        isSuperAdmin() ? h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.orgMapForm = Object.assign({}, m);
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn ghost small', onclick: function () {
              // 快速启停：不进表单，直接提交反转后的 enabled。
              var payload = Object.assign({}, m, { enabled: !m.enabled });
              api('/api/host-field-mappings/' + m.id, {
                method: 'PUT', body: JSON.stringify(payload)
              }).then(reload).catch(function (e) { alert('操作失败: ' + e.message); });
            }
          }, [m.enabled ? '停用' : '启用']),
          h('button', {
            class: 'btn danger small', onclick: function () {
              if (!confirm('删除映射「' + m.sourceKey + ' → ' + m.targetKey + '」？\n已同步到主机的值不会被删除。')) return;
              api('/api/host-field-mappings/' + m.id, { method: 'DELETE' })
                .then(reload).catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除'])
        ]) : h('span', { class: 'muted' }, ['仅 superadmin 可管理'])
      ];
    });

    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['主机字段映射']),
        hint('把外部来源（Consul ServiceMeta / consul_mgr）的键映射到本地字段，同步时按此规则写入。' +
          '「仅补空」只填充本地空值，不覆盖人工维护的内容；「覆盖」则每次同步都以外部值为准。' +
          '不清楚对方有哪些键时，先点「扫描来源键」。'),
        h('div', { class: 'toolbar' }, [
          isSuperAdmin() ? h('button', {
            class: 'btn', onclick: function () { state.orgMapForm = {}; render(); }
          }, ['新增映射']) : null,
          isSuperAdmin() ? h('button', {
            class: 'btn ghost', onclick: function () { openMapScan('consul'); }
          }, ['扫描来源键']) : null,
          h('button', { class: 'btn ghost', onclick: reload }, ['刷新'])
        ]),
        maps.length ? simpleTable(
          ['来源', '来源键', '目标字段', '写入方式', '状态', '排序', '操作'],
          maps, function (m, i) { return rows[i]; }
        ) : h('div', { class: 'muted empty' }, ['还没有映射规则。点「扫描来源键」查看外部实际有哪些键，再勾选导入。']),
        state.orgMapForm ? hostMapForm(o) : null
      ]),
      state.orgMapScan ? mapScanModal(o) : null
    ]);
  }

  // hostMapForm 新增/编辑一条映射。
  function hostMapForm(o) {
    var f = state.orgMapForm || {};
    var editing = !!f.id;
    var sources = o.mapSources || [];
    var columns = o.mapColumns || [];
    var hostDefs = (o.hostDefs || []).slice().sort(function (a, b) {
      return (a.sortOrder || 0) - (b.sortOrder || 0);
    });

    var sourceEl = h('select', {}, sources.map(function (s) {
      return h('option', { value: s }, [s]);
    }));
    sourceEl.value = f.source || (sources[0] || 'consul');

    var keyEl = h('input', { value: f.sourceKey || '', placeholder: '如 app_code、rack' }, []);

    var kindEl = h('select', {}, [
      h('option', { value: 'custom' }, ['自定义字段']),
      h('option', { value: 'column' }, ['内置列'])
    ]);
    kindEl.value = f.targetKind || 'custom';

    // 目标下拉随类型切换：内置列用后端白名单，自定义字段用已定义的主机字段。
    var targetWrap = h('div', {}, []);
    var targetEl = null;
    var renderTarget = function () {
      targetWrap.innerHTML = '';
      if (kindEl.value === 'column') {
        targetEl = h('select', {}, columns.map(function (c) {
          return h('option', { value: c.key }, [c.label + ' (' + c.key + ')']);
        }));
        if (f.targetKind === 'column' && f.targetKey) targetEl.value = f.targetKey;
      } else if (hostDefs.length) {
        targetEl = h('select', {}, hostDefs.map(function (d) {
          return h('option', { value: d.fieldKey }, [d.label + ' (' + d.fieldKey + ')']);
        }));
        if (f.targetKind !== 'column' && f.targetKey) targetEl.value = f.targetKey;
      } else {
        // 没有自定义字段时给出明确指引，而不是一个空下拉。
        targetEl = null;
        targetWrap.appendChild(h('div', { class: 'muted' }, [
          '还没有主机自定义字段，请先到「主机字段」创建，或把目标类型改为「内置列」。'
        ]));
        return;
      }
      targetWrap.appendChild(targetEl);
    };
    renderTarget();
    kindEl.onchange = renderTarget;

    var overwriteEl = h('input', { type: 'checkbox', checked: !!f.overwrite }, []);
    var enabledEl = h('input', { type: 'checkbox', checked: f.enabled !== false }, []);
    var orderEl = h('input', { type: 'number', value: f.sortOrder != null ? f.sortOrder : 100 }, []);

    var save = function () {
      if (!targetEl) {
        alert('请先选择目标字段');
        return;
      }
      var payload = {
        source: sourceEl.value,
        sourceKey: keyEl.value.trim(),
        targetKind: kindEl.value,
        targetKey: targetEl.value,
        overwrite: overwriteEl.checked,
        enabled: enabledEl.checked,
        sortOrder: parseInt(orderEl.value, 10) || 0
      };
      if (!payload.sourceKey) {
        alert('来源键不能为空');
        return;
      }
      var req = editing
        ? api('/api/host-field-mappings/' + f.id, { method: 'PUT', body: JSON.stringify(payload) })
        : api('/api/host-field-mappings', { method: 'POST', body: JSON.stringify(payload) });
      req.then(function () {
        state.orgMapForm = null;
        loadOrg();
      }).catch(function (e) { alert((editing ? '保存失败: ' : '新增失败: ') + e.message); });
    };

    return h('div', { class: 'card' }, [
      h('h3', {}, [editing ? '编辑映射' : '新增映射']),
      h('div', { class: 'grid2' }, [
        field('来源', sourceEl, 'consul=直连 Consul；consul_mgr=经 consul_mgr 汇总'),
        field('来源键', keyEl, 'Consul ServiceMeta 的键名，大小写敏感')
      ]),
      h('div', { class: 'grid2' }, [
        field('目标类型', kindEl, ''),
        field('目标字段', targetWrap, '')
      ]),
      h('div', { class: 'grid2' }, [
        h('div', { class: 'field' }, [h('label', { class: 'check' }, [overwriteEl,
          h('span', {}, [' 覆盖已有值', h('span', { class: 'muted' }, ['不勾选时只填充本地空值'])])])]),
        h('div', { class: 'field' }, [h('label', { class: 'check' }, [enabledEl, ' 启用'])])
      ]),
      field('排序', orderEl, '多条映射指向同一字段时，数值小的先生效（可配置回退链）'),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: save }, ['保存']),
        h('button', {
          class: 'btn ghost', onclick: function () { state.orgMapForm = null; render(); }
        }, ['取消'])
      ])
    ]);
  }

  // openMapScan 扫描某来源实际存在的键（真实拉一次外部数据，只读）。
  function openMapScan(source) {
    state.orgMapScan = { source: source, loading: true, data: null, err: '', picked: {} };
    render();
    api('/api/host-field-mappings/scan?source=' + encodeURIComponent(source))
      .then(function (d) {
        if (!state.orgMapScan || state.orgMapScan.source !== source) return;
        state.orgMapScan.loading = false;
        state.orgMapScan.data = d;
        render();
      })
      .catch(function (e) {
        if (!state.orgMapScan || state.orgMapScan.source !== source) return;
        state.orgMapScan.loading = false;
        state.orgMapScan.err = e.message;
        render();
      });
  }

  // mapScanModal 扫描结果弹窗：勾选未映射的键，一键导入为「字段定义 + 映射」。
  //
  // 这里刻意不自动创建任何字段：扫描只做展示，导入由管理员显式勾选后触发。
  function mapScanModal(o) {
    var sc = state.orgMapScan;
    var mask = h('div', {
      class: 'modal-mask',
      onclick: function (e) { if (e.target === mask) { state.orgMapScan = null; render(); } }
    }, []);

    var sources = o.mapSources || [];
    var switchBar = h('div', { class: 'tabs', style: 'margin-bottom:12px' }, sources.map(function (s) {
      return h('div', {
        class: 'tab' + (sc.source === s ? ' active' : ''),
        role: 'tab', tabindex: '0',
        onclick: function () { openMapScan(s); }
      }, [s]);
    }));

    var body;
    if (sc.loading) {
      body = h('div', { class: 'muted' }, ['正在从 ' + sc.source + ' 拉取数据…']);
    } else if (sc.err) {
      body = h('div', { class: 'err', role: 'alert' }, [sc.err]);
    } else {
      var keys = (sc.data && sc.data.keys) || [];
      var pending = keys.filter(function (k) { return !k.mapped; });
      if (!keys.length) {
        body = h('div', { class: 'muted empty' }, ['未扫描到任何键。确认该来源已有主机并配置了 ServiceMeta。']);
      } else {
        var rows = keys.map(function (k) {
          var cb = null;
          if (!k.mapped) {
            cb = h('input', {
              type: 'checkbox', checked: !!sc.picked[k.key],
              onchange: function () {
                if (cb.checked) sc.picked[k.key] = true; else delete sc.picked[k.key];
              }
            }, []);
          }
          return [
            k.mapped ? h('span', { class: 'muted' }, ['已映射']) : cb,
            h('div', {}, [
              h('div', {}, [k.key]),
              k.note ? h('div', { class: 'muted' }, [k.note]) : null
            ]),
            String(k.count),
            h('div', { class: 'muted' }, [(k.samples || []).join(' / ') || '-']),
            k.mapped ? h('span', { class: 'muted' }, [k.mapped_to || '']) : ''
          ];
        });
        body = h('div', {}, [
          hint('共扫描 ' + (sc.data.hosts_scanned || 0) + ' 台主机。勾选未映射的键后点「导入所选」，' +
            '会按键名创建主机自定义字段并建立映射（写入方式为「仅补空」，可在列表中再调整）。'),
          simpleTable(['选择', '来源键', '主机数', '样例值', '现有映射'], keys, function (k, i) {
            return rows[i];
          }),
          pending.length ? null : h('div', { class: 'muted' }, ['所有键都已建立映射。'])
        ]);
      }
    }

    var importBtn = h('button', {
      class: 'btn', onclick: function () {
        var picked = Object.keys(sc.picked || {});
        if (!picked.length) {
          alert('请先勾选要导入的键');
          return;
        }
        // 每个键提交为一条导入项：后端按 targetKind=custom 创建字段并建映射。
        // label 取扫描结果里的说明（保留键有中文名），否则用键名本身。
        var byKey = {};
        ((sc.data && sc.data.keys) || []).forEach(function (k) { byKey[k.key] = k; });
        var items = picked.map(function (key) {
          var info = byKey[key] || {};
          return {
            sourceKey: key,
            label: info.note || key,
            targetKind: 'custom',
            // 默认不占列表列位：主机列表列数有限，导入后可在「主机字段」里再勾。
            showInList: false,
            overwrite: false
          };
        });
        api('/api/host-field-mappings/import', {
          method: 'POST',
          body: JSON.stringify({ source: sc.source, keys: items })
        }).then(function (r) {
          var msg = '导入完成：新建字段 ' + r.created_fields +
            '，新建映射 ' + r.created_mappings;
          if (r.skipped) msg += '，跳过已映射 ' + r.skipped;
          if (r.failed && r.failed.length) {
            msg += '\n\n失败 ' + r.failed.length + ' 项：\n' +
              r.failed.map(function (f) { return f.key + '：' + f.error; }).join('\n');
          }
          alert(msg);
          state.orgMapScan = null;
          // 字段定义已变：清掉主机页缓存，下次进入重新拉取。
          state.hostFieldDefs = undefined;
          loadOrg();
        }).catch(function (e) { alert('导入失败: ' + e.message); });
      }
    }, ['导入所选']);

    var modal = h('div', {
      class: 'modal help-modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': '扫描来源键'
    }, [
      h('div', { class: 'modal-head' }, [
        h('h3', {}, ['扫描来源键']),
        h('button', {
          class: 'btn ghost small', onclick: function () { state.orgMapScan = null; render(); }
        }, ['关闭'])
      ]),
      switchBar,
      body,
      h('div', { class: 'toolbar' }, [
        sc.loading || sc.err ? null : importBtn,
        h('button', {
          class: 'btn ghost', onclick: function () { openMapScan(sc.source); }
        }, ['重新扫描'])
      ])
    ]);
    mask.appendChild(modal);
    return mask;
  }

  // FlyIAM 人员（只读）：与本地账号对照「已接入 / 未接入」。
  function orgFlyIAMUsers(o) {
    var data = state.flyiamUsers;
    var q = (state.flyiamQuery || '').trim().toLowerCase();

    var reload = function () {
      state.flyiamErr = '';
      api('/api/flyiam/users')
        .then(function (r) { state.flyiamUsers = r; render(); })
        .catch(function (e) { state.flyiamErr = e.message; state.flyiamUsers = null; render(); });
    };

    var qEl = h('input', {
      id: 'flyiam-q', value: state.flyiamQuery || '', placeholder: '按用户名 / 姓名 / 邮箱过滤',
      oninput: function () { state.flyiamQuery = qEl.value; state.focusId = 'flyiam-q'; render(); }
    }, []);

    if (!data && !state.flyiamErr) {
      return h('div', { class: 'card' }, [
        h('h3', {}, ['FlyIAM 人员']),
        hint('展示 FlyIAM 中的用户，并与本系统已接入账号对照；可为未接入用户一键创建本地账号。'),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: reload }, ['加载 FlyIAM 用户'])
        ]),
        state.flyiamErr ? h('div', { class: 'err field', role: 'alert' }, [state.flyiamErr]) : null
      ]);
    }

    var localSet = {};
    (o.users || []).forEach(function (u) { localSet[u.username] = true; });
    var all = (data && data.users) || [];
    var filtered = all.filter(function (u) {
      if (!q) return true;
      return (u.username || '').toLowerCase().indexOf(q) >= 0 ||
        (u.displayName || '').toLowerCase().indexOf(q) >= 0 ||
        (u.email || '').toLowerCase().indexOf(q) >= 0;
    });

    var provision = function (u) {
      if (!confirm('为 ' + u.username + ' 创建本地账号？\n\n' +
        '将生成一次性随机密码（仅显示一次），并把 FlyIAM 中同名的扩展字段写入本地。')) return;
      api('/api/flyiam/users/' + encodeURIComponent(u.username) + '/provision', {
        method: 'POST',
        body: JSON.stringify({ sync_fields: true })
      }).then(function (r) {
        var msg = '已创建本地账号：' + r.username;
        if (r.password) {
          msg += '\n\n初始密码（仅显示一次）：' + r.password +
            '\n请立即转告用户登录后修改密码。';
        }
        if (r.synced_fields) msg += '\n\n已同步扩展字段：' + r.synced_fields + ' 项';
        alert(msg);
        loadOrg();
        reload();
      }).catch(function (e) { alert('创建失败: ' + e.message); });
    };

    var rows = filtered.slice(0, 200).map(function (u) {
      return [
        u.username || '-',
        u.displayName || '-',
        u.email || '-',
        localSet[u.username]
          ? h('span', { class: 'tag status-success' }, ['已接入'])
          : h('div', { class: 'row-actions' }, [
              h('span', { class: 'muted' }, ['未接入']),
              h('button', {
                class: 'btn small', onclick: function () { provision(u); }
              }, ['创建本地账号'])
            ]),
        (u.properties && u.properties.empCode) || '-'
      ];
    });

    return h('div', { class: 'card' }, [
      h('h3', {}, ['FlyIAM 人员']),
      hint('数据来自 FlyIAM（/api/users/export）。「已接入」表示本平台已有同名账号；' +
        '未接入的用户可点「创建本地账号」一键开通（生成一次性随机密码，' +
        '并同步 FlyIAM 中同名的扩展字段）。账号增删改仍以 Casdoor / FlyIAM 为准。'),
      state.flyiamErr ? h('div', { class: 'err field', role: 'alert' }, [state.flyiamErr]) : null,
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn ghost', onclick: reload }, ['刷新']),
        qEl,
        h('span', { class: 'muted' }, ['共 ' + all.length + ' 人' +
          (q ? '（匹配 ' + filtered.length + '）' : '') +
          '，已接入 ' + all.filter(function (u) { return localSet[u.username]; }).length + ' 人'])
      ]),
      simpleTable(['用户名', '姓名', '邮箱', '本平台', '工号'], filtered.slice(0, 200), function (u, i) {
        return rows[i];
      }),
      filtered.length > 200 ? h('div', { class: 'muted' }, ['仅显示前 200 条，请用搜索缩小范围']) : null
    ]);
  }

  // tagPicker 多选标签输入：从候选值选择或手填，回车/加号添加，点 × 移除。
  // 当前用于团队授权的主机分组范围（替代只能提示、无法累加的 datalist）。
  function tagPicker(candidates, placeholder, datalistID) {
    var values = [];
    var listID = datalistID || ('tag-options-' + (++fieldSeq));
    var tagsEl = h('div', { class: 'tag-picker-values' }, []);
    var inputEl = h('input', {
      list: listID,
      placeholder: placeholder || '选择或输入后回车添加',
      onkeydown: function (e) {
        if (e.key === 'Enter') { e.preventDefault(); add(inputEl.value); }
      }
    }, []);
    var addBtn = h('button', {
      class: 'btn ghost small', type: 'button', title: '添加',
      onclick: function () { add(inputEl.value); }
    }, ['+ 添加']);

    function add(raw) {
      String(raw || '').split(/[,，\s]+/).forEach(function (v) {
        v = v.trim();
        if (v && values.indexOf(v) < 0) values.push(v);
      });
      inputEl.value = '';
      renderTags();
    }
    function renderTags() {
      tagsEl.textContent = '';
      if (!values.length) {
        tagsEl.appendChild(h('span', { class: 'muted' }, ['未选择（表示不限主机）']));
      }
      values.forEach(function (v, i) {
        tagsEl.appendChild(h('span', { class: 'tag' }, [
          v,
          h('button', {
            class: 'tag-x', type: 'button', title: '移除', 'aria-label': '移除 ' + v,
            onclick: function () { values.splice(i, 1); renderTags(); }
          }, ['×'])
        ]));
      });
    }
    renderTags();
    return {
      el: h('div', { class: 'tag-picker' }, [
        tagsEl,
        h('div', { class: 'toolbar' }, [
          inputEl,
          h('datalist', { id: listID }, (candidates || []).map(function (g) {
            return h('option', { value: g }, []);
          })),
          addBtn
        ])
      ]),
      getValues: function () { add(inputEl.value); return values.slice(); }
    };
  }

  // 组织架构：FlyIAM 组织树快照（由用户部门属性推导）。
  function orgDepts(o) {
    var deps = o.depts || [];
    var syncedAt = deps.length && deps[0].synced_at
      ? String(deps[0].synced_at).replace('T', ' ').slice(0, 19) : '';

    var rows = deps.map(function (d) {
      var indent = new Array((d.level || 0) + 1).join('　');
      return [
        indent + (d.dept_name || d.dept_id),
        (d.level || 0) + 1,
        d.user_count || 0,
        d.full_path || '-',
        d.dept_id
      ];
    });

    return h('div', { class: 'card' }, [
      h('h3', {}, ['组织架构']),
      hint('组织数据来自 FlyIAM，由其用户属性（deptIdLv0/1/2、deptNameLv0/1/2）推导。' +
        '每次同步整体替换本地快照；人数按用户最深一级部门统计。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn', onclick: function () {
            if (!confirm('从 FlyIAM 拉取最新组织架构并整体替换本地快照？')) return;
            api('/api/flyiam/sync-departments', { method: 'POST', body: '{}' })
              .then(function (r) { alert('同步完成：' + r.total + ' 个部门'); loadOrg(); })
              .catch(function (e) { alert('同步失败: ' + e.message); });
          }
        }, ['从 FlyIAM 同步组织']),
        h('span', { class: 'muted' }, deps.length
          ? ('共 ' + deps.length + ' 个部门' + (syncedAt ? '；上次同步 ' + syncedAt : ''))
          : '尚未同步')
      ]),
      deps.length
        ? simpleTable(['部门', '层级', '人数', '完整路径', '部门 ID'], deps,
            function (d, i) { return rows[i]; })
        : h('div', { class: 'muted empty' }, ['还没有组织数据，点击「从 FlyIAM 同步组织」拉取。'])
    ]);
  }

  // 角色管理。
  var ROLE_PERMS = [
    ['read', '只读', '查看组件、制品与关联任务'],
    ['write', '写入', '创建/修改组件与上传制品'],
    ['delete', '删除', '删除组件、制品与相关资源'],
    ['deploy', '部署', '创建任务并对授权范围内的主机下发升级']
  ];

  // orgRoles 角色管理：与 consul_mgr 一致，使用内联表单（名称/说明/权限勾选）。
  function orgRoles(o) {
    var f = state.orgRoleForm;
    var q = (state.orgRoleQ || '').trim().toLowerCase();
    var roles = o.roles.filter(function (r) {
      if (!q) return true;
      return (r.name || '').toLowerCase().indexOf(q) >= 0 ||
        (r.description || '').toLowerCase().indexOf(q) >= 0 ||
        (r.permissions || []).join(',').toLowerCase().indexOf(q) >= 0;
    });
    var rows = roles.map(function (r) {
      return [
        r.name, r.description || '-',
        h('div', { class: 'row-actions' }, (r.permissions || []).map(function (p) {
          return h('span', { class: 'tag' }, [p]);
        })),
        r.team_count || 0,
        h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.orgRoleForm = {
                id: r.id, name: r.name, code: r.code || '', description: r.description || '',
                permissions: (r.permissions || []).slice()
              };
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn danger small', onclick: function () {
              if (!confirm('删除角色 ' + r.name + '？' +
                ((r.team_count || 0) > 0 ? '\n\n该角色已被 ' + r.team_count + ' 个团队引用，需先解除引用。' : ''))) return;
              api('/api/roles/' + r.id, { method: 'DELETE' })
                .then(loadOrg).catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除'])
        ])
      ];
    });

    var form = null;
    if (f) {
      var nameEl = h('input', { value: f.name || '', placeholder: '如 运维只读、发布管理员' }, []);
      var codeEl = h('input', {
        value: f.code || '', placeholder: '可空，默认同名称',
        disabled: f.id ? 'disabled' : null
      }, []);
      var descEl = h('input', { value: f.description || '', placeholder: '用途说明（可空）' }, []);
      var permEls = {};
      var permBoxes = ROLE_PERMS.map(function (p) {
        var cb = h('input', { type: 'checkbox', checked: (f.permissions || []).indexOf(p[0]) >= 0 }, []);
        permEls[p[0]] = cb;
        return h('label', { class: 'check' }, [cb, h('span', {}, [
          h('strong', {}, [p[1] + '（' + p[0] + '）']),
          h('span', { class: 'muted' }, [' — ' + p[2]])
        ])]);
      });
      function save() {
        var name = (nameEl.value || '').trim();
        if (!name) { alert('请填写角色名称'); return; }
        var perms = Object.keys(permEls).filter(function (k) { return permEls[k].checked; });
        if (!perms.length) { alert('至少选择一个权限'); return; }
        var payload = {
          name: name, description: (descEl.value || '').trim(), permissions: perms
        };
        if (f.id) {
          api('/api/roles/' + f.id, { method: 'PUT', body: JSON.stringify(payload) })
            .then(function () { state.orgRoleForm = null; loadOrg(); })
            .catch(function (e) { alert('保存失败: ' + e.message); });
          return;
        }
        payload.code = (codeEl.value || '').trim();
        api('/api/roles', { method: 'POST', body: JSON.stringify(payload) })
          .then(function () { state.orgRoleForm = null; loadOrg(); })
          .catch(function (e) { alert('创建失败: ' + e.message); });
      }
      form = h('div', { class: 'card' }, [
        h('h3', {}, [f.id ? '编辑角色' : '新增角色']),
        h('div', { class: 'grid2' }, [
          field('角色名称', nameEl, '租户内唯一'),
          field('编码', codeEl, '可空；编辑时不可修改')
        ]),
        field('说明', descEl, ''),
        h('div', { class: 'field' }, [
          h('label', {}, ['权限']),
          h('div', { class: 'check-group' }, permBoxes),
          hint('团队授权可直接给出这些权限，也可引用角色后取并集。')
        ]),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: save }, ['保存']),
          h('button', { class: 'btn ghost', onclick: function () { state.orgRoleForm = null; render(); } }, ['取消'])
        ])
      ]);
    }

    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['角色管理']),
        hint('角色是可复用的权限集合（read/write/delete/deploy），可被团队授权引用；' +
          '被引用的角色不能删除。'),
        h('div', { class: 'toolbar' }, [
          h('input', {
            id: 'org-role-q', value: state.orgRoleQ || '', placeholder: '按名称 / 说明 / 权限搜索',
            oninput: function (e) {
              state.orgRoleQ = e.target.value;
              state.focusId = 'org-role-q';
              render();
            }
          }, []),
          h('button', {
            class: 'btn', onclick: function () {
              state.orgRoleForm = { name: '', code: '', description: '', permissions: ['read'] };
              render();
            }
          }, ['新增角色']),
          h('span', { class: 'muted' }, ['共 ' + roles.length + ' 个角色' +
            (q ? '（全部 ' + o.roles.length + '）' : '')])
        ]),
        simpleTable(['名称', '说明', '权限', '被引用', '操作'], roles,
          function (r, i) { return rows[i]; }),
        roles.length === 0 ? h('div', { class: 'muted empty' }, ['没有匹配的角色']) : null
      ]),
      form
    ]);
  }

  // 团队管理（成员 + 授权）。
  // ownerPolicyText 团队任务操作策略的中文标签。
  function ownerPolicyText(v) {
    if (v === 'on') return '仅本人';
    if (v === 'off') return '团队共享';
    return '跟随全局';
  }

  function orgTeams(o) {
    var newName = h('input', { placeholder: '团队名称（必填）' }, []);
    var newCode = h('input', { placeholder: '编码（可空）' }, []);
    var newDesc = h('input', { placeholder: '说明（可空）' }, []);
    var newPolicy = h('select', {}, [
      h('option', { value: 'inherit' }, ['跟随全局设置']),
      h('option', { value: 'on' }, ['仅本人可操作自己创建的任务']),
      h('option', { value: 'off' }, ['团队成员可互相操作任务'])
    ]);
    var rows = o.teams.map(function (t) {
      return [
        t.name, t.description || '-',
        h('span', { class: 'tag' }, [ownerPolicyText(t.owner_only)]),
        t.member_count || 0, t.group_count || 0,
        h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.orgSelTeam = t.id;
              loadTeamMembers(t.id);
            }
          }, ['成员/授权']),
          h('button', {
            class: 'btn danger small', onclick: function () {
              if (!confirm('删除团队 ' + t.name + '？成员与授权会一并删除。')) return;
              api('/api/teams/' + t.id, { method: 'DELETE' })
                .then(loadOrg).catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除'])
        ])
      ];
    });
    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['团队管理']),
        hint('团队 = 成员集合 + 对服务组的授权。授权支持直接权限，也可引用角色（取并集）。' +
          '「任务操作策略」可按团队覆盖全局的「仅创建者可操作」：on 更严格，off 允许团队内协作。'),
        h('div', { class: 'grid2' }, [
          field('团队名称', newName, ''),
          field('编码', newCode, '可空，便于与外部系统对应')
        ]),
        h('div', { class: 'grid2' }, [
          field('说明', newDesc, ''),
          field('任务操作策略', newPolicy, 'on = 仅本人；off = 团队共享；inherit = 跟随设置页全局开关')
        ]),
        h('div', { class: 'toolbar' }, [
          h('button', {
            class: 'btn', onclick: function () {
              if (!newName.value.trim()) { alert('请填写团队名称'); return; }
              api('/api/teams', {
                method: 'POST',
                body: JSON.stringify({
                  name: newName.value.trim(),
                  code: newCode.value.trim(),
                  description: newDesc.value.trim(),
                  owner_only: newPolicy.value
                })
              }).then(function () { loadOrg(); })
                .catch(function (e) { alert('创建失败: ' + e.message); });
            }
          }, ['新增团队'])
        ]),
        simpleTable(['名称', '说明', '任务操作策略', '成员数', '授权数', '操作'], o.teams,
          function (t, i) { return rows[i]; })
      ]),
      state.orgSelTeam ? teamDetailCard(o, state.orgSelTeam) : null
    ]);
  }

  function loadTeamMembers(teamID) {
    Promise.all([
      api('/api/teams/' + teamID + '/members'),
      api('/api/teams/' + teamID + '/permissions')
    ]).then(function (res) {
      state.orgTeamMembers = res[0] || [];
      state.orgTeamPerms = res[1] || [];
      render();
    }).catch(function (e) { alert('加载团队详情失败: ' + e.message); });
  }

  function teamDetailCard(o, teamID) {
    var team = o.teams.filter(function (t) { return t.id === teamID; })[0];
    var members = state.orgTeamMembers || [];
    var perms = state.orgTeamPerms || [];

    var memberRows = members.map(function (m) {
      return [m.user_id, h('button', {
        class: 'btn danger small', onclick: function () {
          api('/api/teams/' + teamID + '/members/' + encodeURIComponent(m.user_id), { method: 'DELETE' })
            .then(function () { loadTeamMembers(teamID); })
            .catch(function (e) { alert('移除失败: ' + e.message); });
        }
      }, ['移除'])];
    });

    var permRows = perms.map(function (p) {
      return [
        p.group_name || p.group_id,
        (p.permissions || []).join(', ') || '-',
        (p.role_ids || []).join(', ') || '-',
        (p.services || []).join(', ') || '-',
        (p.host_groups || []).join(', ') || '不限',
        h('button', {
          class: 'btn danger small', onclick: function () {
            api('/api/teams/' + teamID + '/permissions/' + p.group_id, { method: 'DELETE' })
              .then(function () { loadTeamMembers(teamID); })
              .catch(function (e) { alert('解除失败: ' + e.message); });
          }
        }, ['解除'])
      ];
    });

    var policyEl = h('select', {}, [
      h('option', { value: 'inherit' }, ['跟随全局设置']),
      h('option', { value: 'on' }, ['仅本人可操作自己创建的任务']),
      h('option', { value: 'off' }, ['团队成员可互相操作任务'])
    ]);
    if (team && team.owner_only) policyEl.value = team.owner_only;

    // 成员添加：从本地用户中多选（排除已在团队的），避免手填用户名出错。
    var memberSet = {};
    members.forEach(function (m) { memberSet[m.user_id] = true; });
    var candidates = (o.users || []).filter(function (u) { return !memberSet[u.username]; });
    var addSel = h('select', {
      multiple: 'multiple',
      size: String(Math.min(6, Math.max(3, candidates.length || 3)))
    }, candidates.map(function (u) {
      return h('option', { value: u.username }, [
        u.username + (u.display_name && u.display_name !== u.username ? '（' + u.display_name + '）' : '')
      ]);
    }));

    return h('div', { class: 'card' }, [
      h('h3', {}, ['团队详情：' + (team ? team.name : teamID)]),
      h('div', { class: 'grid2' }, [
        field('任务操作策略', policyEl, '修改后立即对团队成员生效'),
        h('div', { class: 'field' }, [
          h('label', {}, ['\u00a0']),
          h('button', {
            class: 'btn', onclick: function () {
              if (!team) { alert('团队信息未加载，请刷新'); return; }
              api('/api/teams/' + teamID, {
                method: 'PUT',
                body: JSON.stringify({
                  name: team.name,
                  description: team.description || '',
                  status: team.status || 0,
                  owner_only: policyEl.value
                })
              }).then(function () { loadOrg(); })
                .catch(function (e) { alert('保存失败: ' + e.message); });
            }
          }, ['保存策略'])
        ])
      ]),
      candidates.length
        ? h('div', { class: 'toolbar' }, [
            h('label', { class: 'muted' }, ['添加成员（可多选）：', addSel]),
            h('button', {
              class: 'btn', onclick: function () {
                var sel = addSel.selectedOptions || [];
                var names = Array.prototype.slice.call(sel).map(function (o) { return o.value; });
                if (!names.length) { alert('请先选择要添加的用户'); return; }
                api('/api/teams/' + teamID + '/members', {
                  method: 'POST', body: JSON.stringify({ members: names })
                }).then(function () { loadTeamMembers(teamID); })
                  .catch(function (e) { alert('添加失败: ' + e.message); });
              }
            }, ['添加所选成员'])
          ])
        : h('div', { class: 'muted' }, ['所有本地用户都已加入该团队。']),
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn ghost', onclick: function () { state.orgSelTeam = null; render(); }
        }, ['关闭'])
      ]),
      h('h3', {}, ['成员']),
      simpleTable(['用户名', '操作'], members, function (m, i) { return memberRows[i]; }),
      h('h3', {}, ['服务组授权']),
      simpleTable(['服务组', '直接权限', '引用角色', '授权服务', '主机分组', '操作'], perms,
        function (p, i) { return permRows[i]; }),
      teamPermForm(o, teamID)
    ]);
  }

  // teamPermForm 新增/覆盖某服务组的授权。
  function teamPermForm(o, teamID) {
    var groupEl = h('select', {}, o.groups.map(function (g) {
      return h('option', { value: g.id }, [g.name]);
    }));
    var permEl = h('input', { value: 'read', placeholder: 'read,write,delete,deploy' }, []);
    var roleEl = h('input', { placeholder: '角色 ID，逗号分隔（可空）' }, []);
    var svcEl = h('input', { value: '*', placeholder: '* 表示组内全部组件' }, []);
    var hgPicker = tagPicker(state.hostGroupOptions || [], '选择或输入 app_code / biz_group 后回车');
    return h('div', { class: 'card' }, [
      h('h3', {}, ['设置授权']),
      h('div', { class: 'grid2' }, [
        field('服务组', groupEl, ''),
        field('直接权限', permEl, '逗号分隔')
      ]),
      h('div', { class: 'grid2' }, [
        field('引用角色', roleEl, '角色 ID，可空'),
        field('授权服务', svcEl, '* 或组件名，逗号分隔')
      ]),
      field('主机分组范围', hgPicker.el, '限定可操作的主机（按 app_code / biz_group 匹配；留空表示不限）。超出范围的主机会拒绝创建任务。'),
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn', onclick: function () {
            var split = function (v) {
              return v.split(',').map(function (x) { return x.trim(); }).filter(Boolean);
            };
            api('/api/teams/' + teamID + '/permissions', {
              method: 'PUT',
              body: JSON.stringify({
                group_id: parseInt(groupEl.value, 10),
                permissions: split(permEl.value),
                role_ids: split(roleEl.value).map(function (x) { return parseInt(x, 10); })
                  .filter(function (x) { return !isNaN(x); }),
                services: split(svcEl.value),
                host_groups: hgPicker.getValues()
              })
            }).then(function () { loadTeamMembers(teamID); })
              .catch(function (e) { alert('保存失败: ' + e.message); });
          }
        }, ['保存授权'])
      ])
    ]);
  }

  // 服务组管理。
  // orgGroups 服务组管理：内联表单选择组件集合（带搜索），与 consul_mgr 的服务组对齐。
  function orgGroups(o) {
    var comps = state.components || [];
    var f = state.orgGroupForm;
    var compMap = {};
    comps.forEach(function (c) { compMap[c.id] = c; });
    var q = (state.orgGroupQ || '').trim().toLowerCase();
    var groups = o.groups.filter(function (g) {
      if (!q) return true;
      var names = (g.components || []).join(',');
      return (g.name || '').toLowerCase().indexOf(q) >= 0 ||
        (g.description || '').toLowerCase().indexOf(q) >= 0 ||
        names.toLowerCase().indexOf(q) >= 0;
    });

    var rows = groups.map(function (g) {
      var ids = g.component_ids || [];
      return [
        g.name, g.description || '-',
        h('div', { class: 'row-actions' }, (function () {
          if (!ids.length) return ['-'];
          var names = ids.map(function (id) {
            var c = compMap[id];
            return c ? (c.display_name || c.name) : ('#' + id);
          });
          var shown = names.slice(0, 3).map(function (n) {
            return h('span', { class: 'tag clip', title: n }, [n]);
          });
          if (names.length > shown.length) {
            shown.push(h('span', {
              class: 'tag', title: names.slice(3).join(', ')
            }, ['+' + (names.length - 3)]));
          }
          return shown;
        })()),
        g.team_count || 0,
        h('div', { class: 'row-actions' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              state.orgGroupForm = {
                id: g.id, name: g.name, description: g.description || '',
                component_ids: ids.slice(), q: ''
              };
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn danger small', onclick: function () {
              if (!confirm('删除服务组 ' + g.name + '？相关授权会一并删除。')) return;
              api('/api/service-groups/' + g.id, { method: 'DELETE' })
                .then(loadOrg).catch(function (e) { alert('删除失败: ' + e.message); });
            }
          }, ['删除'])
        ])
      ];
    });

    var form = null;
    if (f) {
      var nameEl = h('input', { value: f.name || '', placeholder: '如 核心采集器、Windows 服务' }, []);
      var descEl = h('input', { value: f.description || '', placeholder: '用途说明（可空）' }, []);
      var qEl = h('input', {
        id: 'group-comp-q', value: f.q || '', placeholder: '搜索组件名称后勾选',
        oninput: function () {
          f.q = qEl.value;
          state.focusId = 'group-comp-q';
          render();
        }
      }, []);
      var selected = {};
      (f.component_ids || []).forEach(function (id) { selected[id] = true; });
      var q = (f.q || '').trim().toLowerCase();
      var matches = comps.filter(function (c) {
        if (!q) return true;
        return (c.name || '').toLowerCase().indexOf(q) >= 0 ||
          (c.display_name || '').toLowerCase().indexOf(q) >= 0;
      });
      var shown = matches.slice(0, 60);
      var boxes = shown.map(function (c) {
        var cb = h('input', {
          type: 'checkbox', checked: !!selected[c.id],
          onchange: function () {
            if (cb.checked) selected[c.id] = true;
            else delete selected[c.id];
            // 直接写回表单，避免仅切换页签时丢失未保存的选择。
            f.component_ids = Object.keys(selected).map(function (x) { return parseInt(x, 10); });
          }
        }, []);
        return h('label', { class: 'check check-row' }, [
          cb, h('span', {}, [
            h('strong', {}, [c.display_name || c.name]),
            h('span', { class: 'muted' }, [' #' + c.id + (c.display_name ? ' · ' + c.name : '')])
          ])
        ]);
      });
      var selectedCount = (f.component_ids || []).length;
      function save() {
        var name = (nameEl.value || '').trim();
        if (!name) { alert('请填写服务组名称'); return; }
        var payload = {
          name: name, description: (descEl.value || '').trim(),
          component_ids: f.component_ids || []
        };
        var req = f.id
          ? api('/api/service-groups/' + f.id, { method: 'PUT', body: JSON.stringify(payload) })
          : api('/api/service-groups', { method: 'POST', body: JSON.stringify(payload) });
        req.then(function () { state.orgGroupForm = null; loadOrg(); })
          .catch(function (e) { alert('保存失败: ' + e.message); });
      }
      form = h('div', { class: 'card' }, [
        h('h3', {}, [f.id ? '编辑服务组' : '新增服务组']),
        h('div', { class: 'grid2' }, [
          field('服务组名称', nameEl, '租户内唯一'),
          field('说明', descEl, '')
        ]),
        h('div', { class: 'field' }, [
          h('label', {}, ['包含组件（已选 ' + selectedCount + ' 个）']),
          qEl,
          boxes.length ? h('div', { class: 'check-group scroll' }, boxes)
            : h('div', { class: 'muted' }, ['没有匹配的组件']),
          matches.length > shown.length
            ? hint('仅显示前 ' + shown.length + ' 个匹配项，请继续输入缩小范围。') : null
        ]),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: save }, ['保存']),
          h('button', {
            class: 'btn ghost', onclick: function () { state.orgGroupForm = null; render(); }
          }, ['取消'])
        ])
      ]);
    }

    return h('div', {}, [
      h('div', { class: 'card' }, [
        h('h3', {}, ['服务组管理']),
        hint('服务组 = 组件集合，是团队授权的单位。与 consul_mgr 的「服务组」概念一致，' +
          '但这里挂载的是本平台的组件。'),
        h('div', { class: 'toolbar' }, [
          h('input', {
            id: 'org-group-q', value: state.orgGroupQ || '', placeholder: '按名称 / 说明 / 组件搜索',
            oninput: function (e) {
              state.orgGroupQ = e.target.value;
              state.focusId = 'org-group-q';
              render();
            }
          }, []),
          h('button', {
            class: 'btn', onclick: function () {
              state.orgGroupForm = { name: '', description: '', component_ids: [], q: '' };
              render();
            }
          }, ['新增服务组']),
          h('span', { class: 'muted' }, ['共 ' + groups.length + ' 个服务组' +
            (q ? '（全部 ' + o.groups.length + '）' : '')])
        ]),
        simpleTable(['名称', '说明', '组件', '授权团队数', '操作'], groups,
          function (g, i) { return rows[i]; }),
        groups.length === 0 ? h('div', { class: 'muted empty' }, ['没有匹配的服务组']) : null
      ]),
      form
    ]);
  }

  // ---------- 审计日志 ----------
  //
  // 服务端按租户过滤（superadmin 可跨租户），保留天数在「设置 → 服务器」调整。
  function auditFilterQS() {
    var f = state.auditFilters || {};
    var qs = [];
    ['q', 'username', 'action', 'status', 'from', 'to'].forEach(function (k) {
      if (f[k]) qs.push(k + '=' + encodeURIComponent(f[k]));
    });
    return qs.join('&');
  }

  function auditQuery() {
    var qs = auditFilterQS();
    return 'page=' + (state.auditPage || 1) + '&size=50' + (qs ? '&' + qs : '');
  }

  function loadAudit(page) {
    if (page) state.auditPage = page;
    state.view = 'audit';
    state.auditLoading = true;
    state.auditErr = '';
    api('/api/audit-logs?' + auditQuery())
      .then(function (d) {
        state.audit = d.list || [];
        state.auditTotal = d.total || 0;
        state.auditPage = d.page || 1;
        state.auditPages = d.pages || 1;
        state.auditLoading = false;
        render();
      })
      .catch(function (e) {
        state.auditLoading = false;
        state.auditErr = e.message;
        render();
      });
  }

  function auditView() {
    var f = state.auditFilters;
    var qEl = h('input', {
      value: f.q || '', placeholder: '用户 / 资源 / 操作 / ID',
      oninput: function () { f.q = qEl.value; }
    }, []);
    var userEl = h('input', {
      value: f.username || '', placeholder: '精确用户名',
      oninput: function () { f.username = userEl.value; }
    }, []);
    var actionEl = h('input', {
      value: f.action || '', placeholder: '如 task.start',
      oninput: function () { f.action = actionEl.value; }
    }, []);
    var statusEl = h('select', {
      value: f.status || '',
      onchange: function () { f.status = statusEl.value; }
    }, [
      h('option', { value: '' }, ['全部结果']),
      h('option', { value: 'success' }, ['成功']),
      h('option', { value: 'failed' }, ['失败'])
    ]);
    var fromEl = h('input', {
      type: 'date', value: f.from || '',
      onchange: function () { f.from = fromEl.value; }
    }, []);
    var toEl = h('input', {
      type: 'date', value: f.to || '',
      onchange: function () { f.to = toEl.value; }
    }, []);

    var search = function () { state.auditPage = 1; loadAudit(); };
    var onEnter = function (e) { if (e.key === 'Enter') search(); };
    [qEl, userEl, actionEl].forEach(function (el) { el.onkeydown = onEnter; });

    var exportURL = '/api/audit-logs/export' + (auditFilterQS() ? '?' + auditFilterQS() : '');

    var rows = (state.audit || []).map(function (x) {
      var details = '';
      try { details = JSON.stringify(x.details || {}); } catch (e) { details = ''; }
      if (details === '{}') details = '';
      var shortDetails = details.length > 120 ? details.slice(0, 120) + '…' : details;
      return [
        fmtTime(x.created_at),
        x.username || '-',
        x.action || '-',
        (x.resource_type || '-') + (x.resource_id ? ' #' + x.resource_id : ''),
        x.resource_name || '-',
        h('span', {
          class: 'tag ' + (x.status === 'success' ? 'status-success' : 'status-failed')
        }, [x.status === 'success' ? '成功' : '失败']),
        x.ip_address || '-',
        x.error_message || '-',
        shortDetails
          ? h('span', { class: 'muted', title: details }, [shortDetails])
          : '-'
      ];
    });

    var pager = state.auditPages > 1 ? h('div', { class: 'pager' }, [
      h('span', { class: 'muted' }, ['第 ' + state.auditPage + ' / ' + state.auditPages +
        ' 页 · 共 ' + state.auditTotal + ' 条']),
      h('button', {
        class: 'btn ghost small', disabled: state.auditPage <= 1 ? '' : null,
        onclick: function () { loadAudit(state.auditPage - 1); }
      }, ['上一页']),
      h('button', {
        class: 'btn ghost small', disabled: state.auditPage >= state.auditPages ? '' : null,
        onclick: function () { loadAudit(state.auditPage + 1); }
      }, ['下一页'])
    ]) : null;

    return h('div', { class: 'card' }, [
      h('h3', {}, ['审计日志']),
      hint('记录登录、任务、组件、组织与设置等敏感操作。仅管理员可见；普通管理员按租户隔离，' +
        'superadmin 可查看全部。保留天数在「设置 → 服务器」调整。'),
      state.auditErr ? h('div', { class: 'err field', role: 'alert' }, [state.auditErr]) : null,
      h('div', { class: 'grid2' }, [
        field('搜索', qEl, '匹配用户、资源名/ID、操作'),
        field('用户名', userEl, '精确匹配')
      ]),
      h('div', { class: 'grid2' }, [
        field('操作', actionEl, '如 task.start / component.update'),
        field('结果', statusEl, '')
      ]),
      h('div', { class: 'grid2' }, [
        field('开始日期', fromEl, '含当天'),
        field('结束日期', toEl, '含当天')
      ]),
      h('div', { class: 'toolbar' }, [
        h('button', { class: 'btn', onclick: search }, ['查询']),
        h('button', {
          class: 'btn ghost', onclick: function () {
            state.auditFilters = { q: '', username: '', action: '', status: '', from: '', to: '' };
            state.auditPage = 1;
            loadAudit();
          }
        }, ['重置']),
        h('a', { class: 'btn ghost', href: exportURL, download: '' }, ['导出 CSV']),
        h('span', { class: 'muted' }, [state.auditLoading ? '加载中…'
          : ('共 ' + state.auditTotal + ' 条' + (state.auditTotal > 20000 ? '（导出最多 2 万条）' : ''))])
      ]),
      simpleTable(['时间', '用户', '操作', '资源', '名称', '结果', 'IP', '错误', '详情'],
        state.audit || [], function (x, i) { return rows[i]; },
        { key: 'audit' }),
      (state.audit || []).length === 0 && !state.auditLoading
        ? h('div', { class: 'muted empty' }, ['没有匹配的审计记录。'])
        : null,
      pager
    ]);
  }

  function settingsView() {
    // inputs 只收集「可提交」的项：readonly 项不入表，避免把只读值回写。
    var inputs = {};
    // restartKeys 记录需重启生效的项，保存后据此提示。
    var restartKeys = {};
    var groups = {};
    (state.settings || []).forEach(function (s) { (groups[s.group] = groups[s.group] || []).push(s); });
    // Redis 状态卡片单独置顶：保存 redis.* 后需重建连接，页面可直接自检。
    if (!state.cacheStatus && !state.cacheErr) loadCacheStatus();
    var sections = Object.keys(groups).map(function (g) {
      return h('div', { class: 'card' }, [h('h3', {}, [g])].concat(groups[g].map(function (s) {
        var inp;
        if (s.type === 'bool') {
          inp = h('select', {}, [h('option', { value: 'true' }, ['true']), h('option', { value: 'false' }, ['false'])]);
          inp.value = s.value || 'false';
        } else if (s.type === 'enum') {
          inp = h('select', {}, (s.options || []).map(function (o) {
            return h('option', { value: o }, [o]);
          }));
          inp.value = s.value || (s.options && s.options[0]) || '';
        } else if (s.type === 'text') {
          // 多行配置（如外部文件白名单）：每行一条 name=address
          inp = h('textarea', { rows: '8', spellcheck: 'false' }, []);
          inp.value = s.value || '';
        } else {
          inp = h('input', { value: s.value || '' }, []);
          if (s.secret) inp.type = 'password';
        }
        if (s.readonly) {
          // 只读项（如数据库连接）：禁用输入，仅作「当前生效值」展示。
          inp.disabled = true;
        } else {
          inputs[s.key] = inp;
          if (s.restart) restartKeys[s.key] = true;
        }

        // 标签后缀：只读/需重启各给一个醒目标记，避免用户改完以为已生效。
        var tags = [];
        if (s.readonly) tags.push(h('span', { class: 'tag' }, ['只读']));
        if (s.restart) tags.push(h('span', { class: 'tag status-failed' }, ['需重启']));

        var sub = [];
        if (s.note) sub.push(h('div', { class: 'muted' }, [s.note]));

        return h('div', { class: 'field' }, [
          h('label', {}, [s.label + '  (' + s.key + ')'].concat(tags)),
          inp
        ].concat(sub));
      })));
    });
    var saveBtn = h('button', {
      class: 'btn', onclick: function () {
        var kv = {};
        var changedRestart = [];
        Object.keys(inputs).forEach(function (k) {
          kv[k] = inputs[k].value;
          if (restartKeys[k]) {
            // 与服务端当前值比对，只提示「真的改了」的需重启项。
            var cur = (state.settings || []).filter(function (s) { return s.key === k; })[0];
            if (cur && String(cur.value) !== String(inputs[k].value)) changedRestart.push(k);
          }
        });
        api('/api/settings', { method: 'PUT', body: JSON.stringify(kv) })
          .then(function () {
            var msg = '已保存';
            if (changedRestart.length) {
              msg += '\n\n⚠️ 以下配置需重启服务后生效：\n' + changedRestart.join('\n');
            }
            alert(msg);
            // redis.* 变更会重建连接：清掉状态缓存，下次渲染重新拉取。
            state.cacheStatus = null;
            state.cacheErr = '';
            loadSettings();
          })
          .catch(function (e) { alert('保存失败: ' + e.message); });
      }
    }, ['保存设置']);
    return h('div', {}, [cacheCard()].concat(sections).concat([h('div', { class: 'card' }, [saveBtn])]));
  }

  // loadCacheStatus 拉取 Redis 运行状态（失败只影响卡片，不影响设置页）。
  function loadCacheStatus() {
    api('/api/cache/status')
      .then(function (d) { state.cacheStatus = d || {}; state.cacheErr = ''; render(); })
      .catch(function (e) { state.cacheErr = e.message; render(); });
  }

  // cacheCard Redis 状态与自检。
  function cacheCard() {
    var st = state.cacheStatus;
    if (!st) {
      return h('div', { class: 'card' }, [
        h('h3', {}, ['Redis 缓存状态']),
        h('div', { class: 'muted' }, [state.cacheErr ? ('状态读取失败：' + state.cacheErr) : '读取中…'])
      ]);
    }
    var modeText = { single: '单点', cluster: '集群', sentinel: '哨兵' }[st.mode] || st.mode || '-';
    var rows = [
      ['状态', st.enabled ? h('span', { class: 'tag status-success' }, ['已连接']) : h('span', { class: 'tag status-failed' }, ['未启用/不可用'])],
      ['模式', modeText],
      ['地址', (st.addrs || []).join(', ') || '-'],
      ['主节点名', st.master_name || '-'],
      ['DB', String(st.db) + (st.db_fallback ? ('（配置 ' + st.requested_db + ' 不可用，已回退）') : '')],
      ['默认 TTL', (st.ttl_seconds || 0) + ' 秒'],
      ['主机列表 TTL', (st.hosts_ttl_seconds || 0) > 0
        ? (st.hosts_ttl_seconds + ' 秒')
        : ((st.ttl_seconds || 0) + ' 秒（未单独配置，沿用默认）')],
      ['键前缀', st.prefix || '-'],
      ['最近错误', st.last_error || '-']
    ];
    return h('div', { class: 'card' }, [
      h('h3', {}, ['Redis 缓存状态']),
      hint('缓存只读 API 响应，降低 PostgreSQL 压力；Redis 不可用时自动降级为直连数据库。' +
        '支持单点 / 集群 / 哨兵；修改下方 redis.* 设置并保存后会立即重建连接。'),
      simpleTable(['项目', '值'], rows, function (r) { return [r[0], r[1]]; }),
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn ghost', onclick: function () {
            api('/api/cache/test', { method: 'POST', body: '{}' })
              .then(function (r) { alert('Redis 读写正常，延迟 ' + r.latency_ms + ' ms（mode=' + r.mode + ', db=' + r.db + '）'); })
              .catch(function (e) { alert('自检失败: ' + e.message); });
          }
        }, ['测试连接']),
        h('button', {
          class: 'btn ghost', onclick: function () {
            if (!confirm('清空本平台 HTTP 缓存？（不会影响 Redis 中其它前缀的数据）')) return;
            api('/api/cache/flush', { method: 'POST', body: '{}' })
              .then(function (r) { alert('已清理 ' + r.deleted + ' 个缓存键'); loadCacheStatus(); })
              .catch(function (e) { alert('清理失败: ' + e.message); });
          }
        }, ['清空缓存']),
        h('button', { class: 'btn ghost', onclick: function () { state.cacheStatus = null; loadCacheStatus(); } }, ['刷新状态'])
      ])
    ]);
  }

  // ---------- 轻量 i18n ----------
  //
  // 英文模式通过「精确文本节点替换」实现，避免把上千条中文文案改成 t() 调用。
  // 覆盖范围：导航、按钮、表头、状态、常用表单标签；长段帮助说明保留中文。
  var I18N_EN = {
    '主机': 'Hosts', '组件': 'Components', '制品': 'Artifacts', '任务': 'Tasks',
    '人员组织': 'Organization', '外部文件': 'External binaries', '审计日志': 'Audit logs',
    '设置': 'Settings', '修改密码': 'Change password', '退出': 'Sign out',
    '运维': 'Operations', '帮助': 'Help', '退出登录': 'Sign out',
    '📘 快速上手': '📘 Quick start', '🖥️ 接入主机示例': '🖥️ Enroll host example',
    '接入主机示例': 'Enroll host example',
    '接入主机（安装 Agent）': 'Enroll host (install agent)',
    '生成接入命令': 'Generate enroll command',
    '一次性令牌': 'One-time token', '自动化令牌': 'Automation token',
    '令牌名称': 'Token name', '有效期': 'Expiry',
    '永久有效': 'Never expires', '自定义时间': 'Custom time',
    '自定义到期时间': 'Custom expiry',
    '最大使用次数（0=不限）': 'Max uses (0 = unlimited)',
    'IP/CIDR 白名单（可选）': 'IP/CIDR allowlist (optional)',
    '安装命令目标系统': 'Install command target OS',
    '安装命令架构': 'Install command architecture',
    '生成自动化令牌': 'Generate automation token', '刷新列表': 'Refresh list',
    '自动化令牌列表': 'Automation tokens', '暂无自动化令牌。': 'No automation tokens.',
    '到期': 'Expires', '已用 / 上限': 'Used / limit', '最后使用': 'Last used',
    '已注销': 'Revoked', '已达上限': 'Limit reached', '注销': 'Revoke', '轮换': 'Rotate',
    '目标操作系统': 'Target OS', '目标机器架构': 'Target architecture',
    '强制重新注册': 'Force re-enrollment',
    '外部文件代缓存': 'External binary cache', '系统设置': 'System settings',
    '用户管理': 'Users', '用户字段': 'User fields', '主机字段': 'Host fields',
    '主机字段定义': 'Host field definitions', '用户字段定义': 'User field definitions',
    '主机字段映射': 'Host field mappings', '扫描来源键': 'Scan source keys',
    '新增映射': 'Add mapping', '编辑映射': 'Edit mapping', '导入所选': 'Import selected',
    '重新扫描': 'Rescan', '同步 consul_mgr…': 'Sync consul_mgr…',
    '从 consul_mgr 同步主机': 'Sync hosts from consul_mgr',
    '全部服务组': 'All service groups', '开始同步': 'Start sync', '同步中…': 'Syncing…',
    '来源': 'Source', '来源键': 'Source key', '目标字段': 'Target field',
    '写入方式': 'Write mode', '仅补空': 'Fill blanks', '覆盖': 'Overwrite',
    '启用': 'Enabled', '停用': 'Disabled', '主机数': 'Hosts', '样例值': 'Samples',
    '现有映射': 'Existing mapping', '已映射': 'Mapped',
    '团队管理': 'Team management', '角色管理': 'Role management',
    '服务组': 'Service groups', '服务组管理': 'Service group management', '组织架构': 'Org structure', 'FlyIAM 人员': 'FlyIAM users',
    '登录': 'Sign in', '用户名': 'Username', '密码': 'Password', '正在登录…': 'Signing in…',
    '创建任务': 'Create task', '上传制品': 'Upload artifact', '新建组件': 'New component',
    '新增团队': 'Add team', '新增角色': 'Add role', '新增服务组': 'Add service group',
    '编辑角色': 'Edit role', '编辑服务组': 'Edit service group', '被引用': 'Referenced by',
    '权限': 'Permissions', '只读': 'Read', '写入': 'Write', '部署': 'Deploy',
    '新增字段': 'Add field', '保存': 'Save', '取消': 'Cancel', '删除': 'Delete',
    '编辑': 'Edit', '关闭': 'Close', '刷新': 'Refresh', '上传': 'Upload',
    '查询': 'Search', '重置': 'Reset', '上一页': 'Previous', '下一页': 'Next',
    '清除筛选': 'Clear filters', '导出 CSV': 'Export CSV', '详情日志': 'Details',
    '启动': 'Start', '暂停': 'Pause', '推进': 'Promote', '重试': 'Retry',
    '升级 Agent': 'Upgrade Agent', '测试连接': 'Test connection',
    '保存设置': 'Save settings', '保存策略': 'Save policy', '添加所选成员': 'Add selected',
    '从 FlyIAM 同步组织': 'Sync org from FlyIAM', '从 FlyIAM 同步字段': 'Sync fields from FlyIAM',
    '创建本地账号': 'Create local account', '加载 FlyIAM 用户': 'Load FlyIAM users',
    '安装组件': 'Install component', '更新组件': 'Update component', '卸载组件': 'Uninstall component',
    '安装脚本': 'Install script', '更新脚本': 'Update script', '卸载脚本': 'Uninstall script',
    '预览': 'Preview', '复制': 'Copy', '批量刷新': 'Refresh all', '新增一行': 'Add row',
    '安全设置': 'Security settings', '设置授权': 'Set permission', '团队详情': 'Team details',
    '组织架构': 'Org structure', '角色管理': 'Role management', '团队管理': 'Team management', '服务组管理': 'Service group management',
    '用户字段': 'User fields', '人员信息': 'People', 'FlyIAM 人员': 'FlyIAM users',
    'Agent 版本分布': 'Agent version distribution', '编辑主机': 'Edit host',
    '任务名': 'Task name', '任务类型': 'Task type', 'IP 段': 'IP range',
    '目标版本': 'Target version', '命令脚本': 'Command script', '解释器': 'Interpreter',
    '脚本参数': 'Script arguments', '标准输入 (stdin)': 'Stdin', '超时': 'Timeout',
    '灰度策略': 'Rollout strategy', '每批台数 / 百分比': 'Batch size / percent',
    '金丝雀首批台数': 'Canary first batch', '自动推进': 'Auto promote',
    '创建者': 'Creator', '状态': 'Status', '类型': 'Type', '版本': 'Version',
    '进度': 'Progress', '总数': 'Total', '成功': 'Success', '失败': 'Failed',
    '时间': 'Time', '用户': 'User', '操作': 'Actions', '资源': 'Resource',
    '名称': 'Name', '显示名称': 'Display name', '模式': 'Mode', '安装路径': 'Install path',
    '服务': 'Service', '主机名': 'Hostname', '业务组': 'Business group', '来源': 'Source',
    '组件/命令': 'Component/Command', '创建者': 'Creator', '错误': 'Error', '详情': 'Details',
    '部门': 'Department', '层级': 'Level', '人数': 'Users', '完整路径': 'Full path',
    '部门 ID': 'Dept ID', '任务名 / 组件名': 'Task / component name',
    '待启动': 'Draft', '运行中': 'Running', '已暂停': 'Paused', '成功': 'Success',
    '失败': 'Failed', '已取消': 'Canceled', '已回滚': 'Rolled back',
    '在线': 'Online', '离线': 'Offline', '未知': 'Unknown', '待下发': 'Pending',
    '执行中': 'Running', '已下发': 'Dispatched', '已接收': 'Accepted', '超时': 'Timeout',
    '已过期': 'Expired', '已跳过': 'Skipped', '待执行': 'Pending',
    '全部状态': 'All statuses', '全部类型': 'All types', '全部组件': 'All components',
    '全部创建者': 'All creators', '全部结果': 'All results', '全部组件': 'All components',
    '按组件筛选：': 'Component: ', '类型：': 'Type: ', '状态：': 'Status: ',
    '创建者：': 'Creator: ', '搜索：': 'Search: ', '按组件：': 'Component: ',
    '共': 'Total', '个组件': 'components', '个任务': 'tasks', '个制品': 'artifacts',
    '个部门': 'departments', '条': 'entries', '台': 'hosts'
  };
  var I18N_RULES = [
    [/^共 (\d+) 个任务.*$/, 'Total $1 task(s)'],
    [/^筛选结果 (\d+) 个任务.*$/, 'Filtered: $1 task(s)'],
    [/^第 (\d+) \/ (\d+) 页.*$/, 'Page $1 / $2'],
    [/^共 (\d+) 个组件.*$/, 'Total $1 component(s)'],
    [/^共 (\d+) 个制品.*$/, 'Total $1 artifact(s)'],
    [/^共 (\d+) 个部门.*$/, 'Total $1 department(s)'],
    [/^共 (\d+) 条.*$/, 'Total $1 entry/entries'],
    [/^目标版本：(.*)$/, 'Target version: $1'],
    [/^上次同步 (.*)$/, 'Last sync: $1'],
    [/^加载失败[:：]\s*(.*)$/, 'Load failed: $1'],
    [/^保存失败[:：]\s*(.*)$/, 'Save failed: $1'],
    [/^删除失败[:：]\s*(.*)$/, 'Delete failed: $1']
  ];

  function toggleLang() {
    state.lang = state.lang === 'en' ? 'zh' : 'en';
    try { localStorage.setItem('lang', state.lang); } catch (e) {}
    document.documentElement.lang = state.lang === 'en' ? 'en' : 'zh-CN';
    render();
  }

  function translateText(t) {
    if (!t) return null;
    if (I18N_EN[t]) return I18N_EN[t];
    for (var i = 0; i < I18N_RULES.length; i++) {
      if (I18N_RULES[i][0].test(t)) return t.replace(I18N_RULES[i][0], I18N_RULES[i][1]);
    }
    return null;
  }

  function applyI18n(root) {
    if (state.lang !== 'en') return;
    document.documentElement.lang = 'en';
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null, false);
    var nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    nodes.forEach(function (n) {
      var raw = n.nodeValue || '';
      var t = raw.trim();
      if (!t) return;
      var tr = translateText(t);
      if (tr) n.nodeValue = raw.replace(t, tr);
    });
    var attrs = ['placeholder', 'title'];
    attrs.forEach(function (attr) {
      var els = root.querySelectorAll('[' + attr + ']');
      for (var i = 0; i < els.length; i++) {
        var v = els[i].getAttribute(attr);
        var tr = translateText((v || '').trim());
        if (tr) els[i].setAttribute(attr, tr);
      }
    });
  }

  function simpleTable(cols, rows, cells, opts) {
    if (!Array.isArray(rows)) rows = [];
    var head = h('tr', {}, cols.map(function (t) { return h('th', { scope: 'col' }, [t]); }));
    var body = rows.map(function (x, i) {
      // 注意：必须把行号传给 cells；很多页面使用 `(x, i) => rows[i]` 预渲染行。
      return h('tr', {}, (cells(x, i) || []).map(function (c) {
        // 支持 DOM 节点或字符串
        return h('td', {}, [typeof c === 'string' ? c : c]);
      }));
    });
    var table = h('table', {}, [h('thead', {}, [head]), h('tbody', {}, body)]);
    var wrap = h('div', { class: 'table-wrap' }, [table]);
    if (!opts || !opts.key) return wrap;
    var block = h('div', { class: 'table-block' }, [wrap]);
    attachTableTools(table, opts.key, !!opts.sortable, block);
    return block;
  }

  // tagResponsiveTables 为所有表格的单元格补 data-label（取表头文字）。
  //
  // 窄屏下 CSS 会把表格渲染成卡片：td::before 用 data-label 显示字段名。
  // 放在 applyI18n 之后调用，因此英文模式下标签也会同步为英文。
  // 不修改列索引/排序逻辑：排序、列显隐仍按原 td 顺序工作。
  function tagResponsiveTables(root) {
    if (!root || !root.querySelectorAll) return;
    var tables = root.querySelectorAll('.table-wrap table');
    for (var t = 0; t < tables.length; t++) {
      var table = tables[t];
      var ths = table.querySelectorAll('thead th');
      if (!ths.length) continue;
      var labels = [];
      for (var i = 0; i < ths.length; i++) labels.push((ths[i].textContent || '').trim());
      table.classList.add('mobile-cards');
      var rows = table.querySelectorAll('tbody tr');
      for (var r = 0; r < rows.length; r++) {
        var cells = rows[r].children;
        for (var c = 0; c < cells.length; c++) {
          var cell = cells[c];
          if (cell.tagName !== 'TD' || cell.hasAttribute('data-label')) continue;
          cell.setAttribute('data-label', labels[c] || '');
        }
      }
    }
  }

  // attachTableTools 为表格增加「列显隐 / 排序 / 列宽记忆」。
  //
  // - 列显隐与列宽按表 key 存在 localStorage；
  // - sortable 仅用于客户端已全量加载的表（组件/制品/审计），
  //   分页表（主机/任务）不排序，避免只排当前页造成误解；
  // - 列宽拖拽后固定 table-layout，避免浏览器重算导致宽度跳变。
  function attachTableTools(table, key, sortable, container) {
    if (!table || !key) return;
    var ths = Array.prototype.slice.call(table.querySelectorAll('thead th'));
    if (!ths.length) return;
    var titles = ths.map(function (th) { return (th.textContent || '').trim(); });
    var hidden = {}, widths = {}, sortState = null;
    try { hidden = JSON.parse(localStorage.getItem('tblcols:' + key) || '{}') || {}; } catch (e) { hidden = {}; }
    try { widths = JSON.parse(localStorage.getItem('tblw:' + key) || '{}') || {}; } catch (e) { widths = {}; }
    try { sortState = JSON.parse(localStorage.getItem('tblsort:' + key) || 'null'); } catch (e) { sortState = null; }

    function save() {
      try {
        localStorage.setItem('tblcols:' + key, JSON.stringify(hidden));
        localStorage.setItem('tblw:' + key, JSON.stringify(widths));
        localStorage.setItem('tblsort:' + key, JSON.stringify(sortState));
      } catch (e) {}
    }

    function applyCols() {
      ths.forEach(function (th, i) {
        var hide = !!hidden[titles[i]];
        th.style.display = hide ? 'none' : '';
        var rows = table.querySelectorAll('tbody tr');
        for (var r = 0; r < rows.length; r++) {
          if (rows[r].cells[i]) rows[r].cells[i].style.display = hide ? 'none' : '';
        }
      });
    }

    function applyWidths() {
      ths.forEach(function (th, i) {
        if (widths[titles[i]]) th.style.width = widths[titles[i]];
      });
    }

    function sortBy(idx, dir) {
      var tb = table.querySelector('tbody');
      if (!tb || !tb.rows.length || idx < 0) return;
      var rows = Array.prototype.slice.call(tb.rows);
      rows.sort(function (a, b) {
        var av = a.cells[idx] ? (a.cells[idx].textContent || '').trim() : '';
        var bv = b.cells[idx] ? (b.cells[idx].textContent || '').trim() : '';
        var na = parseFloat(av.replace(/[^0-9.\-]/g, ''));
        var nb = parseFloat(bv.replace(/[^0-9.\-]/g, ''));
        var numeric = av !== '' && bv !== '' && !isNaN(na) && !isNaN(nb);
        var cmp = numeric ? na - nb : av.localeCompare(bv, 'zh-CN');
        return dir === 'desc' ? -cmp : cmp;
      });
      rows.forEach(function (r) { tb.appendChild(r); });
    }

    function addResizer(th, title) {
      var grip = h('span', { class: 'col-resizer', 'aria-hidden': 'true' }, []);
      grip.onclick = function (e) { e.stopPropagation(); };
      grip.onmousedown = function (e) {
        e.preventDefault();
        e.stopPropagation();
        var startX = e.clientX, startW = th.offsetWidth;
        table.style.tableLayout = 'fixed';
        function move(ev) {
          th.style.width = Math.max(60, startW + ev.clientX - startX) + 'px';
        }
        function up() {
          document.removeEventListener('mousemove', move);
          document.removeEventListener('mouseup', up);
          widths[title] = th.style.width;
          save();
        }
        document.addEventListener('mousemove', move);
        document.addEventListener('mouseup', up);
      };
      th.appendChild(grip);
    }

    var checks = titles.map(function (t, i) {
      var cb = h('input', { type: 'checkbox', checked: !hidden[t] }, []);
      cb.onchange = function () {
        if (cb.checked) delete hidden[t]; else hidden[t] = true;
        save();
        applyCols();
      };
      return h('label', { class: 'check' }, [cb, ' ' + t]);
    });

    var sortHint = sortable
      ? h('span', { class: 'muted' }, ['（点击表头排序）'])
      : null;
    var tools = h('details', { class: 'table-tools' }, [
      h('summary', {}, ['列 / 排序']),
      h('div', { class: 'table-tools-menu' }, [
        h('div', { class: 'muted' }, ['勾选显示的列']),
        h('div', { class: 'table-tools-cols' }, checks),
        h('div', { class: 'toolbar' }, [
          h('button', {
            class: 'btn ghost small', onclick: function () {
              hidden = {}; widths = {}; sortState = null;
              save();
              ths.forEach(function (th) { th.style.display = ''; th.style.width = ''; });
              table.style.tableLayout = '';
              applyCols();
              applyWidths();
              ths.forEach(function (th) { th.classList.remove('sort-asc', 'sort-desc'); });
            }
          }, ['重置表格设置']),
          sortHint
        ])
      ])
    ]);

    // 排序：仅客户端全量表启用。
    if (sortable) {
      ths.forEach(function (th, idx) {
        th.classList.add('sortable');
        th.onclick = function () {
          if (hidden[titles[idx]]) return;
          if (sortState && sortState.title === titles[idx]) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState = { title: titles[idx], dir: 'asc' };
          }
          save();
          ths.forEach(function (x) { x.classList.remove('sort-asc', 'sort-desc'); });
          th.classList.add(sortState.dir === 'desc' ? 'sort-desc' : 'sort-asc');
          sortBy(idx, sortState.dir);
        };
      });
    }
    ths.forEach(function (th, i) { addResizer(th, titles[i]); });

    applyCols();
    applyWidths();
    if (sortable && sortState) {
      var idx = titles.indexOf(sortState.title);
      if (idx >= 0) {
        var th = ths[idx];
        th.classList.add(sortState.dir === 'desc' ? 'sort-desc' : 'sort-asc');
        sortBy(idx, sortState.dir);
      }
    }

    if (container) {
      container.insertBefore(tools, container.firstChild || null);
    } else if (table.parentNode) {
      table.parentNode.insertBefore(tools, table);
    }
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

    // 组件列表保持全量（任务/组织下拉依赖），页面内做搜索 + 客户端分页。
    var q = (state.compQ || '').trim().toLowerCase();
    var filteredComps = state.components.filter(function (x) {
      if (!q) return true;
      return (x.name || '').toLowerCase().indexOf(q) >= 0 ||
        (x.display_name || '').toLowerCase().indexOf(q) >= 0;
    });
    var compPages = Math.max(1, Math.ceil(filteredComps.length / state.compSize));
    if (state.compPage > compPages) state.compPage = compPages;
    var compItems = filteredComps.slice((state.compPage - 1) * state.compSize, state.compPage * state.compSize);
    var compQEl = h('input', {
      id: 'comp-q', value: state.compQ || '', placeholder: '按名称 / 显示名称搜索',
      oninput: function () {
        state.compQ = compQEl.value.trim();
        state.compPage = 1;
        state.focusId = 'comp-q';
        render();
      }
    }, []);

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
          field('OS 匹配 (os_match)', osEl, '填 linux 或 windows 指定任务目标系统，不匹配的主机会被跳过；留空时按 script_url 扩展名、exec_path、install_path 自动判断。'),
          field('架构匹配 (arch_match)', archEl, '仅记录，当前版本不参与匹配。')
        ]),
        field('高级配置 JSON（可选）', advEl, '支持 ' + ADV_KEYS.join('、') + '；命令是二维数组，例如 [["/bin/sh","-c","echo hi"]]。script 或 exec 模式可用 script_url 指定 HTTP(S) 脚本地址（设置后不再要求上传制品）；exec + script_url 时 exec_args 作为脚本参数传入，例如 ["install"]。post_install 脚本请填到下方单独文本框。'),
        field('post_install 脚本（可选）', postScriptEl, 'Agent 在 post_install 命令之后把脚本写入临时文件，并用 bash/sh 执行；失败会触发回滚。脚本不会写入高级配置 JSON。'),
        h('div', { class: 'toolbar' }, [
          h('button', { class: 'btn', onclick: saveComp }, ['保存']),
          h('button', { class: 'btn ghost', onclick: function () { state.editingComp = null; state.err = ''; render(); } }, ['取消']),
          !c.id ? h('span', { class: 'muted' }, ['Linux：']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec'); } }, ['只执行命令（安装）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_update'); } }, ['只执行命令（更新）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_uninstall'); } }, ['只执行命令（卸载）']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script'); } }, ['脚本安装']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_update'); } }, ['脚本更新']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_uninstall'); } }, ['脚本卸载']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_binary'); } }, ['二进制替换']) : null,
          !c.id ? h('span', { class: 'muted' }, ['Windows：']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script'); } }, ['脚本安装']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script_update'); } }, ['脚本更新']) : null,
          !c.id ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script_uninstall'); } }, ['脚本卸载']) : null
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
      isAdmin() ? h('span', { class: 'muted' }, ['Linux：']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec'); } }, ['只执行命令（安装）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_update'); } }, ['只执行命令（更新）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_exec_uninstall'); } }, ['只执行命令（卸载）']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script'); } }, ['脚本安装']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_update'); } }, ['脚本更新']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_script_uninstall'); } }, ['脚本卸载']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('node_exporter_binary'); } }, ['二进制替换']) : null,
      isAdmin() ? h('span', { class: 'muted' }, ['Windows：']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script'); } }, ['脚本安装']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script_update'); } }, ['脚本更新']) : null,
      isAdmin() ? h('button', { class: 'btn ghost', onclick: function () { fillTemplate('windows_exporter_script_uninstall'); } }, ['脚本卸载']) : null,
      h('label', { class: 'muted' }, ['搜索：', compQEl]),
      h('span', { class: 'muted' }, ['共 ' + filteredComps.length + ' 个组件' +
        (q ? '（全部 ' + state.components.length + '）' : '')])
    ]);

    var table = simpleTable(
      ['ID', '名称', '显示名称', '模式', '安装路径', '服务', '操作'],
      compItems,
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
      },
      { key: 'components', sortable: true }
    );

    var empty = filteredComps.length === 0
      ? h('div', { class: 'muted empty' }, [q
          ? '没有匹配的组件。'
          : '还没有组件。点击“新建组件”，或直接点“填入示例：只执行命令（安装 / 更新 / 卸载）、脚本（安装 / 更新 / 卸载）或二进制替换”。'])
      : null;
    var compPager = compPages > 1 ? h('div', { class: 'pager' }, [
      h('span', { class: 'muted' }, ['第 ' + state.compPage + ' / ' + compPages +
        ' 页 · 共 ' + filteredComps.length + ' 个组件']),
      h('button', {
        class: 'btn ghost small', disabled: state.compPage <= 1 ? '' : null,
        onclick: function () { state.compPage -= 1; render(); }
      }, ['上一页']),
      h('button', {
        class: 'btn ghost small', disabled: state.compPage >= compPages ? '' : null,
        onclick: function () { state.compPage += 1; render(); }
      }, ['下一页'])
    ]) : null;

    return h('div', {}, [form, h('div', { class: 'card' }, [toolbar, table, compPager, empty])]);
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
        h('option', { value: 'linux' }, ['linux（未识别发行版）']),
        h('option', { value: 'windows' }, ['windows（Windows Server 2008 R2 ~ Windows 11）'])
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
            state.artifactPage = 1;
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
          field('操作系统 (os_family)', osEl, '留空表示通用；填了以后只匹配同族主机。Windows 主机上报 os_family=windows，二进制制品请选 windows；与平台无关的脚本仍可留空。')
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

    var artQEl = h('input', {
      value: state.artifactQ || '', placeholder: '版本 / 文件名 / 备注',
      onchange: function () { state.artifactQ = artQEl.value.trim(); loadArtifacts(1); }
    }, []);
    var artCompEl = h('select', {
      value: state.artifactFilterComp || '',
      onchange: function () { state.artifactFilterComp = artCompEl.value; loadArtifacts(1); }
    }, [h('option', { value: '' }, ['全部组件'])].concat(
      state.components.map(function (c) { return h('option', { value: String(c.id) }, [c.name]); })
    ));
    var artPages = state.artifactPages || 1;
    var artPager = artPages > 1 ? h('div', { class: 'pager' }, [
      h('span', { class: 'muted' }, ['第 ' + state.artifactPage + ' / ' + artPages +
        ' 页 · 共 ' + state.artifactTotal + ' 个制品']),
      h('button', {
        class: 'btn ghost small', disabled: state.artifactPage <= 1 ? '' : null,
        onclick: function () { loadArtifacts(state.artifactPage - 1); }
      }, ['上一页']),
      h('button', {
        class: 'btn ghost small', disabled: state.artifactPage >= artPages ? '' : null,
        onclick: function () { loadArtifacts(state.artifactPage + 1); }
      }, ['下一页'])
    ]) : null;

    var table = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [
        h('span', { class: 'muted' }, ['共 ' + state.artifactTotal + ' 个制品（版本 × 架构，md5 校验）']),
        h('label', { class: 'muted' }, ['按组件：', artCompEl]),
        h('label', { class: 'muted' }, ['搜索：', artQEl]),
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
        },
        { key: 'artifacts' }),
      state.artifacts.length === 0 ? h('div', { class: 'muted empty' }, [
        (state.artifactQ || state.artifactFilterComp)
          ? '没有匹配的制品。'
          : '还没有制品。先在“组件”页创建 node_exporter，再上传下载好的二进制。'
      ]) : null,
      artPager
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
    var modal = h('div', { class: 'modal', role: 'dialog', 'aria-modal': 'true' }, []);
    mask.appendChild(modal);

    var title = '任务详情' + (d && d.id ? ' #' + d.id : (state.taskDetailId ? ' #' + state.taskDetailId : ''));
    if (d && d.name) title += ' · ' + d.name;
    var actions = (d && d.can_operate !== false) ? taskActions(d, function () {
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
      h('div', { class: 'detail-stat' }, ['类型: ', d.kind === 'command' ? '执行命令/脚本' : '组件']),
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

    // 非管理员：加载有效权限，仅展示有 deploy 权限的组件（服务端仍会复核）。
    if (!isAdmin() && state.myPerms === undefined) {
      state.myPerms = null;
      api('/api/my/permissions')
        .then(function (p) { state.myPerms = p || { components: {} }; render(); })
        .catch(function () { state.myPerms = { components: {} }; render(); });
    }
    // 加载主机字段选项（任务筛选用）
    if (state.hostFacets === null) {
      api('/api/hosts/facets')
        .then(function (d) { state.hostFacets = d || { fields: [], values: {} }; render(); })
        .catch(function () { state.hostFacets = { fields: [], values: {} }; });
    }
    var deployComps = isAdmin() ? state.components : state.components.filter(function (c) {
      var perms = (state.myPerms && state.myPerms.components) || {};
      var set = perms[c.name] || {};
      return !!set.deploy;
    });

    // 表单值放在 state.taskForm 里：预览会触发 render，靠它保留用户输入。
    var f = state.taskForm || (state.taskForm = {
      kind: 'upgrade', name: '', component_id: '', version: '', ip_expr: '',
      script: '', interpreter: '', args: '', stdin: '', tty: false, timeout: '10m',
      mode: 'count', batch_size: '10', canary_count: '1', auto_promote: true,
      filters: {}
    });
    if (!f.kind) f.kind = 'upgrade';
    if (!isAdmin() && f.kind === 'command') f.kind = 'upgrade';
    if (!f.filters) f.filters = {};

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
      deployComps.map(function (c) {
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
      value: f.name, placeholder: '如 node_exporter 组件示例',
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
      h('option', { value: 'upgrade' }, ['组件（upgrade）'])
    ].concat(isAdmin() ? [h('option', { value: 'command' }, ['执行命令/脚本（command）'])] : []));
    var scriptEl = h('textarea', {
      rows: '8',
      placeholder: '#!/bin/bash\n# Linux 示例：\nset -e\nurl="https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh"\nf=$(mktemp /tmp/node_exporter-install.XXXXXX.sh)\ncurl -fsSL "$url" -o "$f"\nbash "$f" uninstall\nrm -f "$f"\n\n# Windows 示例（解释器填 powershell）：\n# Stop-Service node_exporter -ErrorAction SilentlyContinue\n# exit 0\n',
      value: f.script || '',
      oninput: function () { f.script = scriptEl.value; }
    }, []);
    var interpEl = h('input', {
      value: f.interpreter || '', placeholder: '留空自动 bash/sh；Windows 主机填 powershell 或 cmd',
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
          : ('组件 ' + (compMap[f.component_id] ? compMap[f.component_id].name : '组件') + ' ' + f.version)),
        selector: {
          ip_expr: f.ip_expr.trim(),
          filters: f.filters || {}
        },
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

    // 字段标签映射（与后端 hostfieldmap 保持一致）
    var fieldLabels = {
      app_code: 'App Code', biz_group: '业务组', os_family: '操作系统',
      os_type: '系统类型', account: '账号', department: '部门',
      vendor: '厂商', arch: '架构', host_type: '主机类型'
    };

    // 构建字段选择器（多个 tagPicker）
    var facets = state.hostFacets || { fields: [], values: {} };
    var fieldPickers = {};
    var fieldPickerEls = [];
    facets.fields.forEach(function (col) {
      var candidates = facets.values[col] || [];
      var picker = tagPicker(candidates, '选择或输入 ' + (fieldLabels[col] || col) + ' 后回车');
      fieldPickers[col] = picker;
      // 从 f.filters[col] 初始化已选值
      if (f.filters[col]) {
        f.filters[col].forEach(function (v) {
          picker.getValues(); // 先调用一次让 picker 内部同步输入框
          if (picker.getValues().indexOf(v) < 0) {
            // 手动添加到 picker 的 values 数组（tagPicker 内部状态）
            // 由于 tagPicker 没有暴露 add 方法，我们只能重新构建
          }
        });
      }
      fieldPickerEls.push(field(fieldLabels[col] || col, picker.el,
        '多选，留空表示不限。可从已有值中选择或手填。'));
    });

    // 预览前收集 filters
    function collectFilters() {
      var filters = {};
      facets.fields.forEach(function (col) {
        var vals = fieldPickers[col] ? fieldPickers[col].getValues() : [];
        if (vals.length > 0) filters[col] = vals;
      });
      f.filters = filters;
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
      collectFilters();
      if (!checkForm()) return;
      api('/api/tasks/preview', { method: 'POST', body: JSON.stringify(taskBody()) })
        .then(function (d) { state.preview = d; render(); })
        .catch(function (e) { state.preview = null; state.err = e.message; render(); });
    }

    function doCreate() {
      state.err = '';
      collectFilters();
      if (!checkForm()) return;
      api('/api/tasks', { method: 'POST', body: JSON.stringify(taskBody()) })
        .then(function (d) {
          state.preview = null;
          state.taskPage = 1;
          alert('任务已创建 #' + d.id + '，请在列表中点“启动”');
          loadTasks();
        })
        .catch(function (e) { state.err = e.message; render(); });
    }

    // 任务页示例与组件示例对齐：安装 / 更新 / 卸载分别对应组件页的三个 exec 示例。
    // 只填任务名与组件，动作由组件自身的 script_url + exec_args 决定。
    // 按操作系统区分示例：Linux 用 node_exporter_*，Windows 用 windows_exporter_*。
    var UPGRADE_SAMPLES = {
      linux: {
        // 依次尝试新示例组件名，最后回退到旧版模板使用的 node_exporter / *_cmd。
        install: { comps: ['node_exporter_cmd', 'node_exporter_script', 'node_exporter'], name: '安装 node_exporter（Linux 示例）' },
        update: { comps: ['node_exporter_update', 'node_exporter_script_update'], name: '更新 node_exporter（Linux 示例）' },
        uninstall: { comps: ['node_exporter_uninstall', 'node_exporter_script_uninstall'], name: '卸载 node_exporter（Linux 示例）' }
      },
      windows: {
        install: { comps: ['windows_exporter_script'], name: '安装 windows_exporter（Windows 示例）' },
        update: { comps: ['windows_exporter_script_update'], name: '更新 windows_exporter（Windows 示例）' },
        uninstall: { comps: ['windows_exporter_script_uninstall'], name: '卸载 windows_exporter（Windows 示例）' }
      }
    };

    function fillUpgradeSample(action, osName) {
      var osKey = osName === 'windows' ? 'windows' : 'linux';
      var s = UPGRADE_SAMPLES[osKey][action];
      var comp = null;
      for (var i = 0; i < s.comps.length; i++) {
        comp = state.components.filter(function (x) { return x.name === s.comps[i]; })[0];
        if (comp) break;
      }
      if (!comp) {
        alert(osKey === 'windows'
          ? '请先到“组件”页点 Windows 的“脚本安装 / 脚本更新 / 脚本卸载”示例并保存（组件名 windows_exporter_script*）'
          : '请先到“组件”页点 Linux 的“只执行命令”或“脚本安装/更新/卸载”示例并保存（组件名 node_exporter_*）');
        return;
      }
      f.kind = 'upgrade';
      var vers = versionsFor(comp.id);
      f.component_id = String(comp.id);
      f.version = comp.script_url ? 'latest' : (vers[0] || 'latest');
      f.name = s.name;
      // Windows 示例不预填 Linux 风格的 IP 段，避免误选主机网段。
      f.ip_expr = osKey === 'windows' ? '' : '10.1.255.24-26';
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
      f.name = '安装/更新 node_exporter（Linux 示例）';
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
      f.name = '更新 node_exporter（Linux 示例）';
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

    // fillCommandWindowsSample 预填 Windows 安装/更新/卸载示例：
    // 脚本会下载 examples/windows_exporter/install.ps1 并以参数指定动作。
    function fillCommandWindowsSample(action) {
      f.kind = 'command';
      f.name = (action === 'uninstall' ? '卸载' : action === 'update' ? '更新' : '安装') +
        ' windows_exporter（Windows 示例）';
      f.ip_expr = '';
      f.script = windowsInstallScript(action);
      f.interpreter = 'powershell.exe';
      f.stdin = '';
      f.tty = false;
      f.timeout = '30m';
      f.mode = 'count';
      f.batch_size = '10';
      f.auto_promote = action === 'uninstall';
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
      f.name = '卸载 node_exporter（Linux 示例）';
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
        h('div', { class: 'hint' }, [
          '任务系统：' + (p.target_os === 'windows' ? 'Windows' : p.target_os === 'linux' ? 'Linux' : '未识别（不按系统过滤）') +
          (p.skipped_os ? '；已跳过 ' + p.skipped_os + ' 台系统不匹配的主机' : '')
        ]),
        h('div', { class: 'hint' }, ['每批台数：' + (batches.length ? batches.join(' / ') : '-')]),
        h('div', { class: 'hint' }, ['示例 IP：' + (sample.join(', ') || '-') + (sample.length >= 20 ? ' …' : '')])
      ]);
    }

    var form = h('div', { class: 'card' }, [
      h('h3', {}, ['创建任务']),
      state.err ? h('div', { class: 'err field', role: 'alert' }, [state.err]) : null,
      field('任务类型', kindEl, '组件任务走 script_url/制品与健康检查流程（安装/更新/卸载由组件的 exec_args 决定）；执行命令/脚本用于卸载、清理等一次性操作。'),
      h('div', { class: 'grid2' }, [
        field('任务名', nameEl, '留空会自动生成。'),
        f.kind === 'command'
          ? field('IP 段', ipEl, '支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；留空表示所有主机。同时匹配主机注册地址与 Agent 上报的出口 IP（多网卡主机按实际出口网段选）。')
          : field('组件', compEl, state.components.length ? '' : '还没有组件，请先到“组件”页创建。')
      ]),
      f.kind === 'command' ? null : h('div', { class: 'grid2' }, [
        field('目标版本', verEl, '使用制品时从已上传制品中选择；使用 script_url 时可填 latest（仅作记录）。'),        field('IP 段', ipEl, '支持 10.1.255.24-26、10.1.255.0/24，多个用逗号分隔；留空表示所有主机。')
      ]),
      f.kind === 'command' ? null : datalist,
      // 主机字段筛选（如果配置了可选字段）
      fieldPickerEls.length > 0 ? h('div', { class: 'field-group' }, [
        h('h4', {}, ['主机筛选（可选）']),
        hint('按主机字段多选过滤；留空表示不限。与 IP 段取交集。')
      ].concat(fieldPickerEls)) : null,
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
        h('span', { class: 'muted' }, ['Linux：']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandInstallSample }, ['安装脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('install', 'linux'); } }, ['安装组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandUpdateSample }, ['更新脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('update', 'linux'); } }, ['更新组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: fillCommandSample }, ['卸载脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('uninstall', 'linux'); } }, ['卸载组件']),
        h('span', { class: 'muted' }, ['Windows：']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: function () { fillCommandWindowsSample('install'); } }, ['安装脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('install', 'windows'); } }, ['安装组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: function () { fillCommandWindowsSample('update'); } }, ['更新脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('update', 'windows'); } }, ['更新组件']),
        f.kind === 'command'
          ? h('button', { class: 'btn ghost', onclick: function () { fillCommandWindowsSample('uninstall'); } }, ['卸载脚本'])
          : h('button', { class: 'btn ghost', onclick: function () { fillUpgradeSample('uninstall', 'windows'); } }, ['卸载组件'])
      ]),
      hint('创建后任务处于 draft 状态，需要在下方列表点“启动”才会开始下发。')
    ]);

    var filterCompEl = h('select', {
      value: state.taskFilterComp,
      onchange: function () {
        state.taskFilterComp = filterCompEl.value;
        state.taskFilterKind = '';
        state.taskPage = 1;
        loadTasks();
      }
    }, [h('option', { value: '' }, ['全部组件'])].concat(
      deployComps.map(function (c) {
        return h('option', { value: String(c.id) }, [c.name + (c.display_name ? '（' + c.display_name + '）' : '')]);
      })
    ));
    var filterKindEl = h('select', {
      value: state.taskFilterKind,
      onchange: function () {
        state.taskFilterKind = filterKindEl.value;
        state.taskPage = 1;
        loadTasks();
      }
    }, [
      h('option', { value: '' }, ['全部类型']),
      h('option', { value: 'upgrade' }, ['组件']),
      h('option', { value: 'command' }, ['执行命令'])
    ]);
    var filterStatusEl = h('select', {
      value: state.taskFilterStatus,
      onchange: function () {
        state.taskFilterStatus = filterStatusEl.value;
        state.taskPage = 1;
        loadTasks();
      }
    }, [h('option', { value: '' }, ['全部状态'])].concat(
      Object.keys(TASK_STATUS).map(function (k) {
        return h('option', { value: k }, [TASK_STATUS[k]]);
      })
    ));
    var filterCreatorEl = h('select', {
      value: state.taskFilterCreator,
      onchange: function () {
        state.taskFilterCreator = filterCreatorEl.value;
        state.taskPage = 1;
        loadTasks();
      }
    }, [h('option', { value: '' }, ['全部创建者'])].concat(
      (state.taskCreators || []).map(function (u) {
        return h('option', { value: u }, [
          u + (state.user && u === state.user.Username ? '（我）' : '')
        ]);
      })
    ));

    var taskQEl = h('input', {
      value: state.taskQ || '', placeholder: '按任务名搜索',
      onchange: function () {
        state.taskQ = taskQEl.value.trim();
        state.taskPage = 1;
        loadTasks();
      }
    }, []);

    var taskPager = state.taskPages > 1 ? h('div', { class: 'pager' }, [
      h('span', { class: 'muted' }, ['第 ' + state.taskPage + ' / ' + state.taskPages +
        ' 页 · 共 ' + state.taskTotal + ' 个任务']),
      h('button', {
        class: 'btn ghost small', disabled: state.taskPage <= 1 ? '' : null,
        onclick: function () { state.taskPage -= 1; loadTasks(); }
      }, ['上一页']),
      h('button', {
        class: 'btn ghost small', disabled: state.taskPage >= state.taskPages ? '' : null,
        onclick: function () { state.taskPage += 1; loadTasks(); }
      }, ['下一页'])
    ]) : null;

    var list = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [
        h('span', { class: 'muted' }, [
          (state.taskFilterComp || state.taskFilterKind || state.taskFilterStatus || state.taskFilterCreator || state.taskQ)
            ? ('筛选结果 ' + state.taskTotal + ' 个任务；点“详情日志”查看批次进度、逐机阶段和执行日志。')
            : ('共 ' + state.taskTotal + ' 个任务；点“详情日志”查看批次进度、逐机阶段和执行日志。')
        ]),
        h('label', { class: 'muted' }, ['按组件筛选：', filterCompEl]),
        h('label', { class: 'muted' }, ['类型：', filterKindEl]),
        h('label', { class: 'muted' }, ['状态：', filterStatusEl]),
        h('label', { class: 'muted' }, ['创建者：', filterCreatorEl]),
        h('label', { class: 'muted' }, ['搜索：', taskQEl]),
        state.taskFilterComp || state.taskFilterKind || state.taskFilterStatus ||
          state.taskFilterCreator || state.taskQ
          ? h('button', {
            class: 'btn ghost small', onclick: function () {
              state.taskFilterComp = '';
              state.taskFilterKind = '';
              state.taskFilterStatus = '';
              state.taskFilterCreator = '';
              state.taskQ = '';
              state.taskPage = 1;
              loadTasks();
            }
          }, ['清除筛选'])
          : null,
        h('span', { class: 'muted auto-refresh-tip' }, ['● 自动刷新中'])
      ]),
      simpleTable(['ID', '类型', '名称', '组件/命令', '版本', '状态', '进度', '总数', '成功', '失败', '创建者', '操作'], state.tasks,
        function (x) {
          var c = compMap[x.component_id];
          var canView = x.can_view !== false;
          var acts = canView ? [detailBtn(x)] : [];
          if (canView && x.can_operate !== false) acts = acts.concat(taskActions(x));
          if (!acts.length) acts = [h('span', { class: 'muted' }, ['无权限'])];

          var pct = x.total ? Math.round(x.success * 100 / x.total) : 0;
          var isCmd = x.kind === 'command';
          return [x.id,
            isCmd ? '命令' : '组件',
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
            (x.created_by || '-') +
              (state.user && x.created_by === state.user.Username ? '（我）' : ''),
            h('div', { class: 'row-actions' }, acts)];
        },
        { key: 'tasks' }),
      taskPager,
      state.tasks.length === 0
        ? h('div', { class: 'muted empty' }, [
          (state.taskFilterComp || state.taskFilterKind)
            ? '当前筛选条件下没有任务，可点“清除筛选”查看全部。'
            : '还没有任务。先上传制品，再用上面的表单创建。'
        ])
        : null
    ]);

    return h('div', {}, [
      !isAdmin() ? hint('当前账号可为已授权（deploy）的组件创建任务；' +
        '命令任务与授权范围外的操作仍需管理员。任务操作按钮按 owner 策略显示。') : null,
      !isAdmin() && state.myPerms === null
        ? h('div', { class: 'card' }, [h('h3', {}, ['创建任务']), hint('正在加载组件权限…')])
        : (!isAdmin() && !deployComps.length
          ? h('div', { class: 'card' }, [
              h('h3', {}, ['暂无组件任务权限']),
              hint('你还没有任何组件的 deploy 权限。请联系管理员在「人员组织 → 团队」中，' +
                '通过服务组为你所在的团队授予 deploy 权限。命令任务仅管理员可创建。')
            ])
          : form),
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
    if (state.quickstart === undefined) {
      state.quickstart = null;
      api('/api/quickstart')
        .then(function (q) { state.quickstart = q || {}; state.quickstartErr = ''; render(); })
        .catch(function (e) { state.quickstart = {}; state.quickstartErr = e.message; render(); });
    }
    app.appendChild(topbar());
    app.appendChild(tabs());
    // 前端路由守卫：非管理员即使手工改 hash 也不进入管理页（服务端仍会 403）。
    if ((state.view === 'org' || state.view === 'audit') &&
      !(isAdmin() || isSuperAdmin())) {
      state.view = 'hosts';
    }
    if ((state.view === 'extbin' || state.view === 'settings') && !isSuperAdmin()) {
      state.view = 'hosts';
    }
    var body;
    switch (state.view) {
      case 'settings': body = settingsView(); break;
      case 'extbin': body = externalBinariesView(); break;
      case 'org': body = orgView(); break;
      case 'components': body = componentsView(); break;
      case 'artifacts': body = artifactsView(); break;
      case 'tasks': body = tasksView(); break;
      case 'audit': body = auditView(); break;
      case 'password': body = passwordView(); break;
      default: body = hostsView();
    }
    app.appendChild(h('div', { class: 'container' }, [body]));
    if (state.helpOpen) {
      app.appendChild(helpModal());
    }
    if (state.mgrSync) {
      app.appendChild(consulMgrSyncModal());
    }
    if (state.taskDetailId) {
      app.appendChild(taskDetailModal());
      // 自动刷新会重建 DOM，恢复用户原来的滚动位置。
      var mask = document.getElementById('task-detail-mask');
      if (mask) mask.scrollTop = state.taskDetailScroll || 0;
    }
    // i18n 必须放在弹窗之后：否则弹窗内按钮/标题不会被翻译。
    applyI18n(app);
    // 表格卡片化标签依赖翻译后的表头文字。
    tagResponsiveTables(app);
    syncHash();
    // 手工触发的局部刷新（如搜索框输入）会重建 DOM，按 id 还原焦点与光标。
    if (state.focusId) {
      var focusEl = document.getElementById(state.focusId);
      state.focusId = '';
      if (focusEl && focusEl.focus) {
        focusEl.focus();
        try { focusEl.setSelectionRange(focusEl.value.length, focusEl.value.length); } catch (e) {}
      }
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
    // 版本分布变化较慢：手动刷新/进入页面时立即拉取；自动刷新时默认 30 秒，
    // 有 Agent 正在自升级时缩短到 10 秒，便于及时看到剩余旧版本数量。
    var statsEvery = state.agentUpdateActive ? 10000 : 30000;
    if (!quiet || !state.agentStatsAt || Date.now() - state.agentStatsAt >= statsEvery) {
      loadAgentStats();
    }
    // 主机自定义字段定义：列表列与编辑表单共用；只加载一次，失败后置空数组。
    if (state.hostFieldDefs === undefined) {
      state.hostFieldDefs = null;
      api('/api/field-defs/host')
        .then(function (d) { state.hostFieldDefs = d || []; render(); })
        .catch(function () { state.hostFieldDefs = []; render(); });
    }
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
        // 自升级刚结束：立即刷新一次版本分布，不必等下一个 10/30 秒周期。
        var wasUpdating = state.agentUpdateActive;
        var nowUpdating = (d.list || []).some(function (x) {
          var s = x.agent_update_status;
          return s === 'pending' || s === 'dispatched' || s === 'accepted' || s === 'running';
        });
        state.agentUpdateActive = nowUpdating;
        if (wasUpdating && !nowUpdating) loadAgentStats();
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
  function artifactURL() {
    var qs = ['page=' + (state.artifactPage || 1), 'size=' + (state.artifactSize || 50)];
    if (state.artifactFilterComp) qs.push('component_id=' + encodeURIComponent(state.artifactFilterComp));
    if (state.artifactQ) qs.push('q=' + encodeURIComponent(state.artifactQ));
    return '/api/artifacts/paged?' + qs.join('&');
  }

  function loadArtifacts(page) {
    if (page) state.artifactPage = page;
    Promise.all([api(artifactURL()), api('/api/components')])
      .then(function (rs) {
        var d = rs[0] || {};
        state.artifacts = d.list || [];
        state.artifactTotal = d.total || 0;
        state.artifactPage = d.page || 1;
        state.artifactSize = d.size || state.artifactSize;
        state.artifactPages = d.pages || 1;
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
    if (state.taskFilterStatus) qs.push('status=' + encodeURIComponent(state.taskFilterStatus));
    if (state.taskFilterCreator) qs.push('created_by=' + encodeURIComponent(state.taskFilterCreator));
    if (state.taskQ) qs.push('q=' + encodeURIComponent(state.taskQ));
    qs.push('page=' + (state.taskPage || 1));
    qs.push('size=' + (state.taskSize || 50));
    return '/api/tasks' + '?' + qs.join('&');
  }
  function loadTasks(quiet) {
    if (!quiet) state.view = 'tasks';
    var seq = ++tasksReqSeq;
    var tasksReq = api(tasksURL());
    // 自动刷新只需要任务状态；组件/制品变化很少，沿用已有数据，
    // 避免每 4 秒重复拉取两个较大的列表。
    var reqs = quiet
      ? [tasksReq, Promise.resolve(state.components), Promise.resolve(state.artifacts),
         Promise.resolve(state.taskCreators)]
      : [tasksReq, api('/api/components'), api('/api/artifacts'), api('/api/tasks/creators')];
    return Promise.all(reqs)
      .then(function (rs) {
        if (seq !== tasksReqSeq) return;
        // 请求期间用户可能已切换页签，避免后台任务数据覆盖当前页面。
        if (state.view !== 'tasks') return;
        var pageData = rs[0] || {};
        var sig = JSON.stringify([pageData, rs[1] || [], rs[2] || []]);
        state.tasks = pageData.list || [];
        state.taskTotal = pageData.total || 0;
        state.taskPage = pageData.page || 1;
        state.taskSize = pageData.size || state.taskSize || 50;
        state.taskPages = pageData.pages || 1;
        state.components = rs[1] || [];
        state.artifacts = rs[2] || [];
        state.taskCreators = rs[3] || [];
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
            state.taskPage = 1;
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
        if (d.mappings === 0) {
          msg += '\n\n提示：当前没有启用的字段映射，只写入了主机 IP 与位置信息。' +
            '可在「人员组织 → 主机字段映射」配置要同步哪些字段。';
        }
        alert(msg);
        loadHosts();
      })
      .catch(function (e) { alert('同步失败: ' + e.message); });
  }

  // openConsulMgrSync 打开服务组选择弹窗，再由用户确认后同步。
  //
  // 服务组列表来自 consul_mgr（GET /api/consul-mgr/groups），不在配置文件里写死
  // group_id：哪些组可同步由 consul_mgr 侧的启用状态决定，页面只负责展示与选择。
  function openConsulMgrSync() {
    state.mgrSync = { loading: true, groups: null, err: '', groupID: 0, running: false };
    render();
    api('/api/consul-mgr/groups')
      .then(function (d) {
        if (!state.mgrSync) return;
        state.mgrSync.loading = false;
        state.mgrSync.groups = d.groups || [];
        state.mgrSync.endpoint = d.endpoint || '';
        render();
      })
      .catch(function (e) {
        if (!state.mgrSync) return;
        state.mgrSync.loading = false;
        state.mgrSync.err = e.message;
        render();
      });
  }

  // consulMgrSyncModal 选择服务组并触发同步。
  function consulMgrSyncModal() {
    var st = state.mgrSync;
    var mask = h('div', {
      class: 'modal-mask',
      onclick: function (e) {
        // 同步进行中不允许点遮罩关闭，避免用户以为已取消（请求仍在跑）。
        if (e.target === mask && !st.running) { state.mgrSync = null; render(); }
      }
    }, []);

    var body;
    if (st.loading) {
      body = h('div', { class: 'muted' }, ['正在读取 consul_mgr 的服务组…']);
    } else if (st.err) {
      body = h('div', {}, [
        h('div', { class: 'err', role: 'alert' }, [st.err]),
        hint('确认「设置 → consul_mgr 对接」已填写地址与服务令牌，且令牌与 consul_mgr ' +
          '「设置 → 服务间 API」一致。')
      ]);
    } else if (!st.groups.length) {
      body = h('div', {}, [
        h('div', { class: 'muted empty' }, ['consul_mgr 中没有可同步的服务组。']),
        hint('只有「启用且已配置 Consul 地址」的服务组才会出现在这里，请到 consul_mgr 中检查。')
      ]);
    } else {
      // 单选列表：全部 + 各服务组。用 radio 而非下拉，便于看清每组的编码与数据中心。
      var items = [{ id: 0, name: '全部服务组', code: '', datacenter: '' }].concat(st.groups);
      body = h('div', {}, [
        hint('选择要同步的服务组。「全部服务组」会遍历 consul_mgr 中所有启用的组（多套 Consul）。'),
        h('div', { class: 'check-group scroll' }, items.map(function (g) {
          var rb = h('input', {
            type: 'radio', name: 'mgr-group', checked: st.groupID === g.id,
            onchange: function () { st.groupID = g.id; }
          }, []);
          var sub = [];
          if (g.code) sub.push('编码 ' + g.code);
          if (g.datacenter) sub.push('dc ' + g.datacenter);
          if (g.id) sub.push('ID ' + g.id);
          return h('label', { class: 'check check-row' }, [rb, h('span', {}, [
            h('span', {}, [g.name]),
            sub.length ? h('span', { class: 'muted' }, [sub.join(' · ')]) : null
          ])]);
        }))
      ]);
    }

    var startBtn = h('button', {
      class: 'btn', disabled: st.running ? 'disabled' : null,
      onclick: function () {
        st.running = true;
        render();
        syncHostsFromMgr(st.groupID, function () {
          state.mgrSync = null;
          render();
        });
      }
    }, [st.running ? '同步中…' : '开始同步']);

    var modal = h('div', {
      class: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': '从 consul_mgr 同步主机'
    }, [
      h('div', { class: 'modal-head' }, [
        h('h3', {}, ['从 consul_mgr 同步主机']),
        h('button', {
          class: 'btn ghost small', disabled: st.running ? 'disabled' : null,
          onclick: function () { state.mgrSync = null; render(); }
        }, ['关闭'])
      ]),
      st.endpoint ? h('div', { class: 'muted' }, ['来源：' + st.endpoint]) : null,
      body,
      h('div', { class: 'toolbar' }, [
        st.loading || st.err || !(st.groups || []).length ? null : startBtn,
        h('button', {
          class: 'btn ghost', disabled: st.running ? 'disabled' : null,
          onclick: function () { state.mgrSync = null; render(); }
        }, ['取消'])
      ])
    ]);
    mask.appendChild(modal);
    return mask;
  }

  // syncHostsFromMgr 从 consul_mgr 同步主机（覆盖多个服务组/多套 Consul）。
  //
  // 与「同步 Consul」并列而非替代：两者来源不同，可按环境选用或同时使用。
  // groupID=0 表示全部启用的服务组；done 在请求结束（成功或失败）后调用。
  function syncHostsFromMgr(groupID, done) {
    var url = '/api/hosts/sync-consul-mgr';
    if (groupID) url += '?group_id=' + encodeURIComponent(groupID);
    api(url, { method: 'POST' })
      .then(function (d) {
        var msg = '同步完成: 主机 ' + d.upserted + ' 台（服务组 ' + d.groups + ' 个）';
        if (d.partial) {
          // 部分服务组失败时清单不完整，必须让用户知道，否则会误以为主机变少了。
          msg += '\n\n⚠️ 部分服务组查询失败，清单不完整：' +
            (d.failed_groups || []).join('、');
        }
        if (d.mappings === 0) {
          msg += '\n\n提示：consul_mgr 来源还没有启用的字段映射。' +
            '可在「人员组织 → 主机字段映射」里扫描来源键并配置。';
        }
        msg += '\n耗时: ' + d.duration;
        if (done) done();
        alert(msg);
        loadHosts();
      })
      .catch(function (e) {
        if (done) done();
        alert('同步失败: ' + e.message);
      });
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
    // 弹窗打开时暂停列表自动刷新：重建 DOM 会让弹窗/表单焦点抖动，
    // 也避免用户点击时元素被替换导致点击失效。
    if (!state.user || state.taskDetailId || state.editingHost || state.helpOpen || state.navOpen) {
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
        state.view !== 'artifacts' && state.view !== 'extbin' && state.view !== 'org' &&
        state.view !== 'password') {
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
      state.myPerms = undefined;
      state.quickstart = undefined;
      state.quickstartErr = '';
      state.taskForm = null;
      state.taskDetailId = null;
      state.taskDetail = null;
      state.hostsSig = '';
      state.tasksSig = '';
      state.taskDetailSig = '';
      render();
    });
  }

  // 启动：尝试拿当前用户
  window.addEventListener('hashchange', function () {
    if (!state.user) return;
    var route = parseHash();
    if (!route) return;
    applyHashToState(route);
    switchView(route.view);
  });

  api('/api/auth/userinfo').then(function (u) {
    state.user = u;
    var route = parseHash();
    if (route) {
      applyHashToState(route);
      switchView(route.view);
    } else {
      loadHosts().then(scheduleAutoRefresh, scheduleAutoRefresh);
    }
  }).catch(function () { render(); });
})();
