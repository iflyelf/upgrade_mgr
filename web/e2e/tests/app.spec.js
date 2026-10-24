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

  // 主机页不再内嵌接入卡片，只在帮助弹窗中出现。
  await modal.getByRole('button', { name: '关闭' }).click();
  await expect(page.locator('.help-modal')).toHaveCount(0);
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
