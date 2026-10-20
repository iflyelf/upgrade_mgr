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

  var state = { user: null, view: 'hosts', hosts: [], total: 0, q: '', settings: [], components: [], artifacts: [], tasks: [], err: '' };

  function h(tag, attrs, children) {
    var el = document.createElement(tag);
    attrs = attrs || {};
    for (var k in attrs) {
      if (k === 'onclick') el.onclick = attrs[k];
      else if (k === 'value') el.value = attrs[k];
      else el.setAttribute(k, attrs[k]);
    }
    (children || []).forEach(function (c) {
      if (c == null) return;
      el.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    });
    return el;
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

  function switchView(v) {
    state.view = v;
    if (v === 'settings') loadSettings();
    else if (v === 'components') loadComponents();
    else if (v === 'artifacts') loadArtifacts();
    else if (v === 'tasks') loadTasks();
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
    return h('div', { class: 'card' }, [toolbar, h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, rows)])])]);
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
      return h('tr', {}, cells(x).map(function (c) { return h('td', {}, [String(c == null ? '-' : c)]); }));
    });
    return h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, body)])]);
  }

  function componentsView() {
    return h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [h('span', { class: 'muted' }, ['组件定义（升级对象）'])]),
      simpleTable(['ID', '名称', '模式', '安装路径', '服务'], state.components,
        function (x) { return [x.id, x.name, x.deploy_mode, x.install_path, x.service_name]; })
    ]);
  }

  function artifactsView() {
    return h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [h('span', { class: 'muted' }, ['制品（版本×架构，md5 校验）'])]),
      simpleTable(['ID', '组件', '版本', 'ARCH', 'OS', '大小', 'MD5'], state.artifacts,
        function (x) { return [x.id, x.component_id, x.version, x.arch, x.os_family, x.size, (x.md5 || '').slice(0, 12)]; })
    ]);
  }

  function tasksView() {
    var nameEl, cidEl, verEl, ipEl;
    var form = h('div', { class: 'card' }, [
      h('h3', {}, ['创建升级任务']),
      h('div', { class: 'field' }, [h('label', {}, ['任务名']), (nameEl = h('input', {}, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['组件 ID']), (cidEl = h('input', {}, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['目标版本']), (verEl = h('input', {}, []))]),
      h('div', { class: 'field' }, [h('label', {}, ['IP 段（如 10.1.255.24-26,10.1.255.0/24）']), (ipEl = h('input', {}, []))]),
      h('button', { class: 'btn', onclick: function () {
        var body = { name: nameEl.value, component_id: parseInt(cidEl.value, 10), version: verEl.value,
          selector: { ip_expr: ipEl.value }, strategy: { mode: 'count', batch_size: 10, auto_promote: true } };
        api('/api/tasks', { method: 'POST', body: JSON.stringify(body) })
          .then(function (d) { alert('任务已创建 #' + d.id + '，可在列表启动'); loadTasks(); })
          .catch(function (e) { alert('创建失败: ' + e.message); });
      } }, ['创建'])
    ]);
    var list = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [h('span', { class: 'muted' }, ['任务列表'])]),
      simpleTable(['ID', '名称', '版本', '状态', '总数', '成功', '失败', '操作'], state.tasks,
        function (x) { return [x.id, x.name, x.version, x.status, x.total, x.success, x.failed,
          '']; }),
      h('div', { class: 'toolbar' }, state.tasks.map(function (x) {
        return h('button', { class: 'btn', onclick: function () {
          api('/api/tasks/' + x.id + '/start', { method: 'POST' })
            .then(function () { alert('任务 #' + x.id + ' 已启动'); loadTasks(); })
            .catch(function (e) { alert(e.message); });
        } }, ['启动 #' + x.id]);
      }))
    ]);
    return h('div', {}, [form, list]);
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
      default: body = hostsView();
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
    api('/api/components').then(function (d) { state.components = d || []; render(); }).catch(function (e) { state.components = []; render(); });
  }
  function loadArtifacts() {
    api('/api/artifacts').then(function (d) { state.artifacts = d || []; render(); }).catch(function (e) { state.artifacts = []; render(); });
  }
  function loadTasks() {
    api('/api/tasks').then(function (d) { state.tasks = d || []; render(); }).catch(function (e) { state.tasks = []; render(); });
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
