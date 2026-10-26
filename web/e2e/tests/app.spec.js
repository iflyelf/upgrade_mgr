const { test, expect } = require('@playwright/test');

const ADMIN = process.env.E2E_ADMIN || 'admin';
const PASSWORD = process.env.E2E_PASSWORD || 'admin123';

async function login(page, user = ADMIN, pass = PASSWORD) {
  await page.goto('/');
  await page.locator('input[autocomplete="username"]').fill(user);
  await page.locator('input[autocomplete="current-password"]').fill(pass);
  await page.getByRole('button', { name: '登录', exact: true }).click();
}

// openNavMenu 打开顶栏下拉菜单（如「人员组织」「运维」「帮助」）。
async function openNavMenu(page, label) {
  await page.locator('.main-nav .nav-link', { hasText: label }).click();
}

test('登录失败提示', async ({ page }) => {
  await login(page, ADMIN, 'wrong-password');
  await expect(page.locator('.err')).toContainText('用户名或密码错误');
});

test('登录后导航与 URL 刷新保持页面', async ({ page }) => {
  await login(page);
  const nav = page.locator('.main-nav');
  await expect(nav).toBeVisible();
  await nav.getByRole('button', { name: '任务', exact: true }).click();
  await expect(page).toHaveURL(/#\/tasks/);
  await expect(page.getByRole('heading', { name: '创建任务' })).toBeVisible();

  await page.reload();
  await expect(page).toHaveURL(/#\/tasks/);
  await expect(page.getByRole('heading', { name: '创建任务' })).toBeVisible();
});

test('帮助菜单包含快速上手与接入主机示例', async ({ page }) => {
  await login(page);
  await openNavMenu(page, '帮助');
  await page.getByRole('menuitem', { name: '接入主机示例' }).click();

  const modal = page.locator('.help-modal');
  await expect(modal).toBeVisible();
  await expect(modal.getByRole('heading', { name: '接入主机示例' })).toBeVisible();
  await expect(modal.getByRole('button', { name: '生成接入命令' })).toBeVisible();

  // 主机页顶部同时内嵌完整接入卡片；帮助菜单入口仍然保留。
  await modal.getByRole('button', { name: '关闭' }).click();
  await expect(page.locator('.help-modal')).toHaveCount(0);
});

test('主机页顶部直接展示接入主机示例', async ({ page }) => {
  await login(page);
  // 主机页默认视图即可看到完整的 OS/架构选择与生成命令按钮。
  await expect(page.getByRole('heading', { name: '接入主机（安装 Agent）' })).toBeVisible();
  await expect(page.getByRole('button', { name: '生成接入命令' })).toBeVisible();

  // 帮助菜单仍保留同一入口，关闭弹窗后主机页卡片不消失。
  await openNavMenu(page, '帮助');
  await page.getByRole('menuitem', { name: '接入主机示例' }).click();
  const modal = page.locator('.help-modal');
  await expect(modal.getByRole('heading', { name: '接入主机示例' })).toBeVisible();
  await modal.getByRole('button', { name: '关闭' }).click();
  await expect(page.getByRole('heading', { name: '接入主机（安装 Agent）' })).toBeVisible();
});

test('自动化令牌可创建并注销', async ({ page }) => {
  await login(page);
  await page.getByRole('tab', { name: '自动化令牌' }).click();
  await expect(page.getByRole('button', { name: '生成自动化令牌' })).toBeVisible();

  const name = 'e2e_auto_' + Date.now();
  await page.getByPlaceholder('prod-ops-automation').fill(name);
  await page.getByRole('button', { name: '生成自动化令牌' }).click();
  await expect(page.getByText('自动化令牌已生成（仅显示一次，请立即复制到自动化平台）：')).toBeVisible();

  const row = page.locator('table tr', { hasText: name });
  await expect(row).toBeVisible();
  await expect(row).toContainText('永久');

  page.once('dialog', (dialog) => dialog.accept());
  await row.getByRole('button', { name: '注销' }).click();
  await expect(row).toContainText('已注销');
});

test('创建组件并出现在列表', async ({ page }) => {
  await login(page);
  await page.locator('.main-nav').getByRole('button', { name: '组件', exact: true }).click();
  await page.getByRole('button', { name: '新建组件' }).click();

  const name = 'e2e_comp_' + Date.now();
  await page.getByRole('textbox', { name: '名称 (name)' }).fill(name);
  await page.getByRole('textbox', { name: '显示名称 (display_name)' }).fill(name);

  // 保存后会重新拉取列表；先等这次 GET 完成，避免与搜索输入竞争。
  const reloaded = page.waitForResponse(
    (r) => r.url().includes('/api/components') && r.request().method() === 'GET'
  );
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await reloaded;

  // 组件可能被分页到后面，用页面搜索定位。
  const search = page.locator('input[placeholder="按名称 / 显示名称搜索"]');
  await search.fill(name);
  await expect(search).toHaveValue(name);
  await expect(page.locator('table')).toContainText(name);
});

test('审计日志与人员组织（下拉菜单）可打开', async ({ page }) => {
  await login(page);
  await openNavMenu(page, '运维');
  await page.getByRole('menuitem', { name: '审计日志' }).click();
  await expect(page.getByRole('heading', { name: '审计日志' })).toBeVisible();

  await openNavMenu(page, '人员组织');
  await page.getByRole('menuitem', { name: '组织架构' }).click();
  await expect(page.getByRole('button', { name: '从 FlyIAM 同步组织' })).toBeVisible();

  // 用户管理：与 consul_mgr 对齐的新增/重置密码入口。
  await openNavMenu(page, '人员组织');
  await page.getByRole('menuitem', { name: '用户管理' }).click();
  await expect(page.getByRole('button', { name: '新增用户' })).toBeVisible();

  // 角色/服务组：与 consul_mgr 对齐的内联表单（非 prompt）。
  await openNavMenu(page, '人员组织');
  await page.getByRole('menuitem', { name: '角色' }).click();
  await page.getByRole('button', { name: '新增角色' }).click();
  await expect(page.getByRole('textbox', { name: '角色名称' })).toBeVisible();
  await expect(page.getByRole('checkbox', { name: /只读（read）/ })).toBeChecked();

  await openNavMenu(page, '人员组织');
  await page.getByRole('menuitem', { name: '服务组' }).click();
  await page.getByRole('button', { name: '新增服务组' }).click();
  await expect(page.getByRole('textbox', { name: '服务组名称' })).toBeVisible();
  await expect(page.getByPlaceholder('搜索组件名称后勾选')).toBeVisible();
});

test('英文模式切换（含帮助弹窗与复制按钮）', async ({ page }) => {
  await login(page);
  await page.locator('button[title^="Switch language"]').click();
  await expect(page.locator('.main-nav').getByRole('button', { name: 'Hosts', exact: true })).toBeVisible();

  await openNavMenu(page, 'Help');
  await page.getByRole('menuitem', { name: 'Quick start' }).click();
  const modal = page.locator('.help-modal');
  await expect(modal.locator('.guide-title', { hasText: 'Host: enroll Linux agent' })).toBeVisible();

  // 弹窗是模态的，先关闭再切换语言（也验证弹窗不会阻塞顶栏交互）。
  await modal.getByRole('button', { name: 'Close' }).click();
  await expect(page.locator('.help-modal')).toHaveCount(0);
  await page.locator('button[title^="Switch language"]').click();
  await expect(page.locator('.main-nav').getByRole('button', { name: '主机', exact: true })).toBeVisible();
});

test('系统设置展示 Redis 缓存状态与操作入口', async ({ page }) => {
  await login(page);
  await openNavMenu(page, '运维');
  await page.getByRole('menuitem', { name: '系统设置' }).click();
  await expect(page.getByRole('heading', { name: 'Redis 缓存状态' })).toBeVisible();
  await expect(page.getByRole('button', { name: '测试连接' })).toBeVisible();
  await expect(page.getByRole('button', { name: '清空缓存' })).toBeVisible();
  await expect(page.getByRole('button', { name: '刷新状态' })).toBeVisible();
  // 制品上传的新配置项必须在页面可见，且明确不要求重启（保存后热更新）。
  const uploadLabel = page.locator('label', { hasText: 'artifact.max_concurrent_uploads' });
  const lockWaitLabel = page.locator('label', { hasText: 'artifact.lock_wait' });
  await expect(uploadLabel).toBeVisible();
  await expect(lockWaitLabel).toBeVisible();
  await expect(uploadLabel).not.toContainText('需重启');
  await expect(lockWaitLabel).not.toContainText('需重启');
  // 外部文件加速代理必须在页面可配置。
  await expect(page.locator('label', { hasText: '(server.external_binary_proxy)' })).toBeVisible();
  await expect(page.locator('label', { hasText: '(server.external_binary_proxy_hosts)' })).toBeVisible();
});

test('快速上手支持 Linux / Windows 双示例', async ({ page }) => {
  await login(page);
  await openNavMenu(page, '帮助');
  await page.getByRole('menuitem', { name: '快速上手' }).click();
  const g = page.locator('.help-modal');
  await expect(g).toBeVisible();
  await g.getByRole('button', { name: 'Windows', exact: true }).click();
  await expect(g.locator('.guide-title', { hasText: '主机 接入 Windows Agent' })).toBeVisible();
  await expect(g.locator('.guide-title', { hasText: '组件 选择 Windows 示例' })).toBeVisible();
  await expect(g.getByText('windows-amd64', { exact: false }).first()).toBeVisible();

  await g.getByRole('button', { name: 'Linux', exact: true }).click();
  await expect(g.locator('.guide-title', { hasText: '主机 接入 Linux Agent' })).toBeVisible();
  await expect(g.locator('.guide-title', { hasText: '组件 选择 Linux 示例' })).toBeVisible();
  await expect(g.getByText('linux-amd64', { exact: false }).first()).toBeVisible();
  const copyBtn = g.getByRole('button', { name: '复制命令' }).first();
  await expect(copyBtn).toBeVisible();
  await copyBtn.click();
  await expect(copyBtn).toHaveText('已复制');
});

// 回归：顶栏下拉曾被 .main-nav 的 overflow-x:auto 裁剪（overflow-y 被强制为 auto），
// 点击后菜单实际展开但不可见。Playwright 的 click 会自动滚动被裁剪元素，
// 因此必须显式断言菜单能被真实命中，而不是只断言 click 成功。
test('顶栏下拉菜单展开后不被父容器裁剪', async ({ page }) => {
  await login(page);

  for (const label of ['人员组织', '运维', '帮助']) {
    await openNavMenu(page, label);
    const menu = page.locator('.nav-dropdown.open > .nav-menu');
    await expect(menu).toBeVisible();
    await expect(menu).toBeInViewport();

    // 菜单中心点必须命中菜单自身；被 overflow 裁剪时会命中页面其他元素。
    const hitInsideMenu = await menu.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!(hit && el.contains(hit));
    });
    expect(hitInsideMenu, `${label} 菜单被裁剪`).toBe(true);

    // 点击页面空白处关闭，保证下一轮从未展开状态开始。
    await page.locator('body').click({ position: { x: 5, y: 300 } });
    await expect(page.locator('.nav-dropdown.open')).toHaveCount(0);
  }
});
