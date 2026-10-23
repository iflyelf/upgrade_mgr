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

test('英文模式切换', async ({ page }) => {
  await login(page);
  await page.locator('button[title^="Switch language"]').click();
  await expect(page.getByRole('tab', { name: 'Hosts' })).toBeVisible();
  await page.locator('button[title^="Switch language"]').click();
  await expect(page.getByRole('tab', { name: '主机' })).toBeVisible();
});
