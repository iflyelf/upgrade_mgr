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
    await page.locator('input[placeholder="IP/出口 IP/隧道地址/主机名/业务组"]').fill(tag);
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
