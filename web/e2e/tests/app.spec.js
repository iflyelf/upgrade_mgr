const { test, expect } = require('@playwright/test');

const ADMIN = process.env.E2E_ADMIN || 'admin';
const PASSWORD = process.env.E2E_PASSWORD || 'admin123';

async function login(page, user = ADMIN, pass = PASSWORD) {
  await page.goto('/');
  await page.locator('input[autocomplete="username"]').fill(user);
  await page.locator('input[autocomplete="current-password"]').fill(pass);
  await page.getByRole('button', { name: '登录', exact: true }).click();
}

test('登录失败提示', async ({ page }) => {
  await login(page, ADMIN, 'wrong-password');
  await expect(page.locator('.err')).toContainText('用户名或密码错误');
});

test('登录后导航与 URL 刷新保持页面', async ({ page }) => {
  await login(page);
  await expect(page.locator('.tabs')).toBeVisible();
  await page.getByRole('tab', { name: '任务' }).click();
  await expect(page).toHaveURL(/#\/tasks/);
  await expect(page.getByRole('heading', { name: '创建任务' })).toBeVisible();

  await page.reload();
  await expect(page).toHaveURL(/#\/tasks/);
  await expect(page.getByRole('heading', { name: '创建任务' })).toBeVisible();
});

test('创建组件并出现在列表', async ({ page }) => {
  await login(page);
  await page.getByRole('tab', { name: '组件' }).click();
  await page.getByRole('button', { name: '新建组件' }).click();

  const name = 'e2e_comp_' + Date.now();
  await page.getByRole('textbox', { name: '名称 (name)' }).fill(name);
  await page.getByRole('textbox', { name: '显示名称 (display_name)' }).fill(name);
  await page.getByRole('button', { name: '保存', exact: true }).click();

  // 组件可能被分页到后面，用页面搜索定位。
  const search = page.locator('input[placeholder="按名称 / 显示名称搜索"]');
  await search.fill(name);
  await search.press('Tab');
  await expect(page.locator('table')).toContainText(name);
});

test('审计日志与组织架构页面可打开', async ({ page }) => {
  await login(page);
  await page.getByRole('tab', { name: '审计日志' }).click();
  await expect(page.getByRole('heading', { name: '审计日志' })).toBeVisible();

  await page.getByRole('tab', { name: '人员组织' }).click();
  await page.getByRole('tab', { name: '组织架构' }).click();
  await expect(page.getByRole('button', { name: '从 FlyIAM 同步组织' })).toBeVisible();
});

test('英文模式切换（含快速上手与复制按钮）', async ({ page }) => {
  await login(page);
  await page.locator('button[title^="Switch language"]').click();
  await expect(page.getByRole('tab', { name: 'Hosts' })).toBeVisible();

  const guide = page.locator('details.help');
  if (!(await guide.evaluate(el => el.open))) {
    await guide.locator('summary').click();
  }
  await expect(page.locator('.guide-title', { hasText: 'Host: enroll Linux agent' })).toBeVisible();
  await expect(guide.getByRole('button', { name: 'Copy command' }).first()).toBeVisible();

  await page.locator('button[title^="Switch language"]').click();
  await expect(page.getByRole('tab', { name: '主机' })).toBeVisible();
  await expect(page.locator('.guide-title', { hasText: '主机 接入 Linux Agent' })).toBeVisible();
  await expect(page.locator('details.help').getByRole('button', { name: '复制命令' }).first()).toBeVisible();
});

test('快速上手支持 Linux / Windows 双示例', async ({ page }) => {
  await login(page);
  const guide = page.locator('details.help');
  await expect(guide).toBeVisible();
  if (!(await guide.evaluate(el => el.open))) {
    await guide.locator('summary').click();
  }
  const g = page.locator('details.help');
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
