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
      return h('tr', {}, cells(x).map(function (c) { 
        // 支持 DOM 节点或字符串
        return h('td', {}, [typeof c === 'string' ? c : c]); 
      }));
    });
    return h('div', { class: 'table-wrap' }, [h('table', {}, [h('thead', {}, [head]), h('tbody', {}, body)])]);
  }

  function componentsView() {
    var editingComp = state.editingComp || null;
    var nameEl, dispEl, modeEl, binEl, pathEl, serviceEl, svcMgrEl, osEl, archEl, enabledEl, advEl;
    
    var form = editingComp !== null ? h('div', { class: 'card' }, [
      h('h3', {}, [editingComp.id ? '编辑组件' : '新建组件']),
      state.err ? h('div', { class: 'err field' }, [state.err]) : null,
      h('div', { class: 'field' }, [
        h('label', {}, ['名称 (name)']),
        (nameEl = h('input', { value: editingComp.name || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['显示名称 (display_name)']),
        (dispEl = h('input', { value: editingComp.display_name || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['部署模式 (deploy_mode)']),
        (modeEl = h('select', {}, [
          h('option', { value: 'replace' }, ['replace']),
          h('option', { value: 'exec' }, ['exec']),
          h('option', { value: 'replace_exec' }, ['replace_exec'])
        ]))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['二进制名称 (binary_name)']),
        (binEl = h('input', { value: editingComp.binary_name || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['安装路径 (install_path)']),
        (pathEl = h('input', { value: editingComp.install_path || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['服务名称 (service_name)']),
        (serviceEl = h('input', { value: editingComp.service_name || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['服务管理器 (service_mgr)']),
        (svcMgrEl = h('select', {}, [
          h('option', { value: 'auto' }, ['auto']),
          h('option', { value: 'systemd' }, ['systemd']),
          h('option', { value: 'sysv' }, ['sysv'])
        ]))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['OS 匹配 (os_match)']),
        (osEl = h('input', { value: editingComp.os_match || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['架构匹配 (arch_match)']),
        (archEl = h('input', { value: editingComp.arch_match || '' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, [
          (enabledEl = h('input', { type: 'checkbox' }, [])),
          ' 启用 (enabled)'
        ])
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['高级配置 JSON (可选，exec_path/exec_args/pre_install 等)']),
        (advEl = h('textarea', { rows: '8', style: 'width:100%; font-family:monospace; font-size:13px;' }, []))
      ]),
      h('div', { class: 'toolbar' }, [
        h('button', {
          class: 'btn', onclick: function () {
            var base = editingComp.id ? Object.assign({}, editingComp) : {
              backup_dir: '/var/lib/upgrade-agent/backup',
              service_mgr: 'auto',
              deploy_mode: 'replace',
              enabled: true
            };
            base.name = nameEl.value;
            base.display_name = dispEl.value;
            base.deploy_mode = modeEl.value;
            base.binary_name = binEl.value;
            base.install_path = pathEl.value;
            base.service_name = serviceEl.value;
            base.service_mgr = svcMgrEl.value;
            base.os_match = osEl.value;
            base.arch_match = archEl.value;
            base.enabled = enabledEl.checked;
            
            if (advEl.value.trim()) {
              try {
                var adv = JSON.parse(advEl.value);
                Object.assign(base, adv);
              } catch (e) {
                state.err = '高级配置 JSON 格式错误: ' + e.message;
                render();
                return;
              }
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
          }
        }, ['保存']),
        h('button', {
          class: 'btn', onclick: function () { state.editingComp = null; render(); }
        }, ['取消'])
      ])
    ]) : null;
    
    // 设置选择框和复选框的初始值
    if (editingComp && form) {
      setTimeout(function() {
        if (modeEl) modeEl.value = editingComp.deploy_mode || 'replace';
        if (svcMgrEl) svcMgrEl.value = editingComp.service_mgr || 'auto';
        if (enabledEl) enabledEl.checked = editingComp.enabled !== false;
        if (nameEl && editingComp.id) nameEl.readOnly = true;
        if (advEl && editingComp.id) {
          var adv = {};
          ['exec_path', 'exec_args', 'exec_as_user', 'exec_timeout', 'exec_env', 'work_dir',
           'ignore_error', 'pre_install', 'post_install', 'stop_cmd', 'start_cmd', 'restart_cmd',
           'version_cmd', 'version_regex', 'health_cmd', 'consul_maint', 'backup_dir'].forEach(function(k) {
            if (editingComp[k] !== undefined && editingComp[k] !== null && editingComp[k] !== '') {
              adv[k] = editingComp[k];
            }
          });
          if (Object.keys(adv).length > 0) {
            advEl.value = JSON.stringify(adv, null, 2);
          }
        }
      }, 0);
    }
    
    var toolbar = h('div', { class: 'toolbar' }, [
      state.user && state.user.IsAdmin ? h('button', {
        class: 'btn', onclick: function () {
          state.editingComp = { enabled: true };
          state.err = '';
          render();
        }
      }, ['新建组件']) : null,
      h('span', { class: 'muted' }, ['组件定义（升级对象）'])
    ]);
    
    var table = simpleTable(
      ['ID', '名称', '显示名称', '模式', '安装路径', '服务', '操作'],
      state.components,
      function (x) {
        var actions = state.user && state.user.IsAdmin ? h('div', { style: 'display:flex; gap:4px;' }, [
          h('button', {
            class: 'btn', style: 'padding:4px 8px; font-size:12px;',
            onclick: function () {
              state.editingComp = Object.assign({}, x);
              state.err = '';
              render();
            }
          }, ['编辑']),
          h('button', {
            class: 'btn', style: 'padding:4px 8px; font-size:12px; background:#d9776c;',
            onclick: function () {
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
    
    return h('div', {}, [form, h('div', { class: 'card' }, [toolbar, table])]);
  }

  function artifactsView() {
    var compEl, verEl, archEl, osEl, innerEl, notesEl, fileEl;
    
    var uploadForm = state.user && state.user.IsAdmin ? h('div', { class: 'card' }, [
      h('h3', {}, ['上传制品']),
      state.err ? h('div', { class: 'err field' }, [state.err]) : null,
      h('div', { class: 'field' }, [
        h('label', {}, ['组件']),
        (compEl = h('select', {}, [h('option', { value: '' }, ['请选择'])].concat(
          state.components.map(function (c) {
            return h('option', { value: c.id }, [c.name + ' (' + c.display_name + ')']);
          })
        )))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['版本']),
        (verEl = h('input', { placeholder: '如 1.2.3' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['架构 (arch)']),
        (archEl = h('select', {}, [
          h('option', { value: 'amd64' }, ['amd64']),
          h('option', { value: 'arm64' }, ['arm64']),
          h('option', { value: 'arm' }, ['arm']),
          h('option', { value: '386' }, ['386'])
        ]))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['操作系统 (os_family，可选)']),
        (osEl = h('input', { placeholder: '如 linux' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['内部路径 (inner_path，可选)']),
        (innerEl = h('input', { placeholder: '如 bin/app' }, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['备注 (notes，可选)']),
        (notesEl = h('input', {}, []))
      ]),
      h('div', { class: 'field' }, [
        h('label', {}, ['文件']),
        (fileEl = h('input', { type: 'file' }, []))
      ]),
      h('button', {
        class: 'btn', onclick: function () {
          if (!compEl.value || !verEl.value || !fileEl.files[0]) {
            state.err = '组件、版本和文件为必填项';
            render();
            return;
          }
          var fd = new FormData();
          fd.append('file', fileEl.files[0]);
          fd.append('component_id', compEl.value);
          fd.append('version', verEl.value);
          fd.append('arch', archEl.value);
          if (osEl.value) fd.append('os_family', osEl.value);
          if (innerEl.value) fd.append('inner_path', innerEl.value);
          if (notesEl.value) fd.append('notes', notesEl.value);
          
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
        }
      }, ['上传'])
    ]) : null;
    
    var compMap = {};
    state.components.forEach(function (c) { compMap[c.id] = c.name; });
    
    var table = h('div', { class: 'card' }, [
      h('div', { class: 'toolbar' }, [h('span', { class: 'muted' }, ['制品（版本×架构，md5 校验）'])]),
      simpleTable(['ID', '组件', '版本', 'ARCH', 'OS', '大小', 'MD5', '备注'], state.artifacts,
        function (x) {
          return [
            x.id,
            compMap[x.component_id] || x.component_id,
            x.version,
            x.arch,
            x.os_family || '-',
            x.size ? (x.size / 1024 / 1024).toFixed(2) + ' MB' : '-',
            (x.md5 || '').slice(0, 12),
            x.notes || '-'
          ];
        })
    ]);
    
    return h('div', {}, [uploadForm, table]);
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
