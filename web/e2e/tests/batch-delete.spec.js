const { test, expect } = require('@playwright/test');
const { execFileSync, spawn } = require('child_process');

const ADMIN = process.env.E2E_ADMIN || 'admin';
const PASSWORD = process.env.E2E_PASSWORD || 'admin123';
// 主机只能由 Agent 注册或同步产生，这里直接写库造数；未提供数据库地址时跳过主机用例。
const DB_URL = process.env.E2E_DATABASE_URL || '';

async function login(page) {
  await page.goto('/');
  await page.locator('input[autocomplete="username"]').fill(ADMIN);
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.locator('.main-nav')).toBeVisible();
}

function psql(sql) {
  return execFileSync('psql', [DB_URL, '-v', 'ON_ERROR_STOP=1', '-At', '-c', sql], { encoding: 'utf8' }).trim();
}

// collectDialogs 自动确认 confirm/alert，并记录文案供断言。
function collectDialogs(page) {
  const messages = [];
  page.on('dialog', (d) => { messages.push(d.message()); d.accept(); });
  return messages;
}

test('主机：按筛选条件全选 → 后台删除进度 → 刷新后恢复', async ({ page }) => {
  test.skip(!DB_URL, '未设置 E2E_DATABASE_URL，跳过主机批量删除用例');
  test.setTimeout(60 * 1000);

  // 150 台：作业每批 100 台，第一批提交后能看到中间进度。
  const tag = 'e2e-hdel-' + Date.now();
  psql(`INSERT INTO hosts (ip, hostname, tenant_id, source)
        SELECT '${tag}-' || g, '${tag}', 1, 'manual' FROM generate_series(1,150) g`);
  const lastID = psql(`SELECT max(id) FROM hosts WHERE hostname='${tag}'`);

  // 锁住最后一台主机：第二批会等锁，作业停在 100/150，便于验证刷新后恢复进度。
  const locker = spawn('psql', [DB_URL, '-c',
    `BEGIN; SELECT 1 FROM hosts WHERE id=${lastID} FOR UPDATE; SELECT pg_sleep(12); COMMIT;`],
    { stdio: 'ignore' });
  try {
    await expect.poll(() => psql(`SELECT count(*) FROM pg_stat_activity
      WHERE state='active' AND query LIKE '%pg_sleep(12)%' AND pid<>pg_backend_pid()`)).toBe('1');

    const dialogs = collectDialogs(page);
    await login(page);
    await page.locator('input[aria-label="搜索主机"]').fill(tag);
    await page.getByRole('button', { name: '搜索', exact: true }).click();
    await expect(page.getByText('共 150')).toBeVisible();

    await page.getByRole('button', { name: '按筛选条件全选' }).click();
    await expect(page.getByText('已按筛选条件选中 150 台可删除主机')).toBeVisible();
    await page.getByRole('button', { name: '批量删除（150）' }).click();
    expect(dialogs[0]).toContain('当前筛选结果中的 150 台主机');

    const progress = page.getByRole('status').filter({ hasText: '后台删除中' });
    await expect(progress).toContainText('100 / 150', { timeout: 15000 });

    // 刷新页面：作业在服务端继续，页面重新进入后恢复进度显示。
    await page.reload();
    await expect(page.getByRole('status').filter({ hasText: '后台删除中' })).toContainText('/ 150');

    // 释放锁后作业完成，弹出汇总提示，列表清空。
    await expect.poll(() => dialogs.some((m) => m.includes('已完成') && m.includes('已删除 150 台主机')),
      { timeout: 25000 }).toBe(true);
    expect(psql(`SELECT count(*) FROM hosts WHERE hostname='${tag}'`)).toBe('0');
  } finally {
    locker.kill();
    psql(`DELETE FROM hosts WHERE hostname='${tag}'`);
  }
});

test('主机：超长筛选标签仍可移除', async ({ page }) => {
  test.skip(!DB_URL, '未设置 E2E_DATABASE_URL，跳过超长标签用例');
  const tag = 'e2e-hlong-' + Date.now();
  const longApp = 'E2E_LONG_' + 'X'.repeat(80);
  psql(`INSERT INTO hosts (ip, hostname, app_code, tenant_id, source)
        VALUES ('${tag}-1', '${tag}', '${longApp}', 1, 'manual')`);
  try {
    await login(page);
    await page.locator('input[aria-label="搜索主机"]').fill(tag);
    await page.getByRole('button', { name: '搜索', exact: true }).click();
    await expect(page.getByText('共 1')).toBeVisible();

    const appBox = page.locator('.tag-filter[data-key="app_code"]');
    const input = appBox.getByLabel('按 app_code 筛选');
    await input.fill(longApp);
    await input.press('Enter');
    await expect(appBox.locator('.tag')).toHaveCount(1);

    // 文本被截断，但 × 必须始终可见且可点击。
    const x = appBox.locator('.tag-x');
    await expect(x).toBeVisible();
    const box = await x.boundingBox();
    expect(box && box.width > 0 && box.height > 0).toBeTruthy();
    await x.click();
    await expect(appBox.locator('.tag')).toHaveCount(0);
  } finally {
    psql(`DELETE FROM hosts WHERE hostname='${tag}'`);
  }
});

test('组件：多选批量删除', async ({ page }) => {
  const dialogs = collectDialogs(page);
  await login(page);
  const prefix = 'e2e_cbd_' + Date.now();
  for (const n of ['a', 'b']) {
    const res = await page.request.post('/api/components', {
      data: { name: prefix + '_' + n, display_name: prefix + '_' + n, deploy_mode: 'replace', enabled: true }
    });
    expect(res.ok()).toBeTruthy();
  }

  await page.locator('.main-nav').getByRole('button', { name: '组件', exact: true }).click();
  await page.locator('input[placeholder="按名称 / 显示名称搜索"]').fill(prefix);
  await expect(page.locator('table tbody tr')).toHaveCount(2);

  await page.getByRole('checkbox', { name: '选择组件 ' + prefix + '_a' }).check();
  await page.getByRole('checkbox', { name: '选择组件 ' + prefix + '_b' }).check();
  await expect(page.getByText('已选中 2 个组件')).toBeVisible();

  const reloaded = page.waitForResponse(
    (r) => r.url().endsWith('/api/components') && r.request().method() === 'GET'
  );
  await page.getByRole('button', { name: '批量删除（2）' }).click();
  await reloaded;
  expect(dialogs.some((m) => m.includes('已删除 2 个组件'))).toBe(true);
  await expect(page.getByText('没有匹配的组件。')).toBeVisible();
});


test('主机：下拉筛选 + URL 恢复 + 按筛选全选只选中筛选结果', async ({ page }) => {
  test.skip(!DB_URL, '未设置 E2E_DATABASE_URL，跳过主机筛选用例');
  const tag = 'e2e-hflt-' + Date.now();
  const app = 'E2EAPP' + Date.now();
  // 3 台匹配 app_code，2 台不匹配；关键字 tag 隔离其他数据。
  psql(`INSERT INTO hosts (ip, hostname, app_code, tenant_id, source)
        SELECT '${tag}-' || g, '${tag}', CASE WHEN g <= 3 THEN '${app}' ELSE 'OTHER' END, 1, 'manual'
          FROM generate_series(1,5) g`);
  try {
    await login(page);
    await page.locator('input[aria-label="搜索主机"]').fill(tag);
    await page.getByRole('button', { name: '搜索', exact: true }).click();
    await expect(page.getByText('共 5')).toBeVisible();

    // 可搜索标签框：输入完整 app_code 后回车，即使不在初始 facets 里也能精确筛选。
    const appBox = page.locator('.tag-filter[data-key="app_code"]');
    await appBox.getByLabel('按 app_code 筛选').fill(app);
    await appBox.getByLabel('按 app_code 筛选').press('Enter');
    await expect(page.getByText('共 3')).toBeVisible();
    await expect(page).toHaveURL(new RegExp('app_code=' + app));

    // 刷新后筛选条件从 URL 恢复为标签。
    await page.reload();
    await expect(page.locator('.tag-filter[data-key="app_code"] .tag', { hasText: app })).toBeVisible();
    await expect(page.getByText('共 3')).toBeVisible();

    await page.getByRole('button', { name: '按筛选条件全选' }).click();
    await expect(page.getByText('已按筛选条件选中 3 台可删除主机')).toBeVisible();

    await page.getByRole('button', { name: '清除筛选' }).click();
    await expect(page.locator('.tag-filter[data-key="app_code"] .tag')).toHaveCount(0);
    await expect(page).not.toHaveURL(/app_code=/);
  } finally {
    psql(`DELETE FROM hosts WHERE hostname='${tag}'`);
  }
});


test('主机：筛选支持多选标签（选一个即为单选）', async ({ page }) => {
  test.skip(!DB_URL, '未设置 E2E_DATABASE_URL，跳过主机多选筛选用例');
  const tag = 'e2e-hmulti-' + Date.now();
  const app1 = 'E2EM1' + Date.now();
  const app2 = 'E2EM2' + Date.now();
  psql(`INSERT INTO hosts (ip, hostname, app_code, tenant_id, source)
        SELECT '${tag}-' || g, '${tag}',
               CASE WHEN g <= 2 THEN '${app1}' WHEN g <= 4 THEN '${app2}' ELSE 'OTHER' END,
               1, 'manual'
          FROM generate_series(1,5) g`);
  try {
    await login(page);
    await page.locator('input[aria-label="搜索主机"]').fill(tag);
    await page.getByRole('button', { name: '搜索', exact: true }).click();
    await expect(page.getByText('共 5')).toBeVisible();

    const appInput = page.locator('.tag-filter[data-key="app_code"]').getByLabel('按 app_code 筛选');
    await appInput.fill(app1);
    await appInput.press('Enter');
    await appInput.fill(app2);
    await appInput.press('Enter');
    await expect(page.getByText('共 4')).toBeVisible();
    await expect(page.locator('.tag-filter[data-key="app_code"] .tag')).toHaveCount(2);
    await expect(page).toHaveURL(new RegExp('app_code=' + app1));
    await expect(page).toHaveURL(new RegExp('app_code=' + app2));

    // 刷新后两个标签都从 URL 恢复。
    await page.reload();
    await expect(page.locator('.tag-filter[data-key="app_code"] .tag')).toHaveCount(2);
    await expect(page.getByText('共 4')).toBeVisible();

    // 点标签 × 移除一个值，筛选立即生效（只留一个即为单选）。
    await page.locator('.tag-filter[data-key="app_code"] .tag', { hasText: app2 })
      .locator('.tag-x').click();
    await expect(page.locator('.tag-filter[data-key="app_code"] .tag')).toHaveCount(1);
    await expect(page.getByText('共 2')).toBeVisible();
  } finally {
    psql(`DELETE FROM hosts WHERE hostname='${tag}'`);
  }
});
