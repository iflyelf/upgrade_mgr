const { test, expect } = require('@playwright/test');

const ADMIN = process.env.E2E_ADMIN || 'admin';
const PASSWORD = process.env.E2E_PASSWORD || 'admin123';

// 登录接口有限流（防暴力破解）：每个 worker 只走一次表单登录，之后复用会话 Cookie，
// 避免与其他 spec 同时运行时触发 429。
let sessionCookies = null;

async function login(page) {
  if (sessionCookies) {
    await page.context().addCookies(sessionCookies);
    await page.goto('/');
    await expect(page.locator('.main-nav')).toBeVisible();
    return;
  }
  await page.goto('/');
  await page.locator('input[autocomplete="username"]').fill(ADMIN);
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.locator('.main-nav')).toBeVisible();
  sessionCookies = await page.context().cookies();
}

async function openAI(page) {
  await login(page);
  await page.locator('.main-nav').getByRole('button', { name: 'AI 接入', exact: true }).click();
  await expect(page).toHaveURL(/#\/ai/);
  await expect(page.getByRole('heading', { name: 'AI 访问令牌', exact: true })).toBeVisible();
}

// 页面上所有 confirm() 弹窗一律确认。
function autoAccept(page) {
  page.on('dialog', (d) => d.accept());
}

test.describe('AI 接入页面', () => {
  test('状态卡片展示服务端配置（地址与服务名来自接口）', async ({ page, request }) => {
    await openAI(page);
    const integration = await page.evaluate(async () => (await (await fetch('/api/ai/integration')).json()).data);
    await expect(page.locator('code.ai-code', { hasText: integration.mcp_url })).toBeVisible();
    await expect(page.getByText('服务名 ' + integration.server_name, { exact: false })).toBeVisible();
    await expect(page.getByText('默认（' + integration.token_default_ttl + '）')).toBeAttached();
  });

  test('创建、复制、注销令牌', async ({ page }) => {
    autoAccept(page);
    await openAI(page);
    const name = 'e2e-' + Date.now();
    await page.locator('#aiTkName').fill(name);
    await page.locator('#aiTkExpiry').selectOption('custom');
    await page.locator('#aiTkCustom').fill('2h');
    await page.locator('#aiTkWrite').check();
    await page.getByRole('button', { name: '创建令牌', exact: true }).click();

    const notice = page.locator('.ai-notice.ok');
    await expect(notice).toContainText(name);
    await expect(notice).toContainText('允许写');
    const plain = await page.locator('#aiTkPlain').inputValue();
    expect(plain.length).toBeGreaterThan(40);

    const row = page.locator('table tr', { hasText: name });
    await expect(row).toContainText('有效');
    await expect(row).toContainText('允许');

    // 新令牌可直接访问 MCP
    const ping = await page.evaluate(async (t) => {
      const r = await fetch('/mcp', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + t },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ping' }) });
      return r.status;
    }, plain);
    expect(ping).toBe(200);

    await row.getByRole('button', { name: '注销' }).click();
    await expect(page.locator('table tr', { hasText: name })).toContainText('已注销');
    const after = await page.evaluate(async (t) => {
      const r = await fetch('/mcp', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + t },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ping' }) });
      return r.status;
    }, plain);
    expect(after).toBe(401);
  });

  test('名称为空时提示错误', async ({ page }) => {
    await openAI(page);
    await page.locator('#aiTkName').fill('');
    await page.getByRole('button', { name: '创建令牌', exact: true }).click();
    await expect(page.locator('.err[role="alert"]', { hasText: '请填写令牌名称' })).toBeVisible();
  });

  test('为每个客户端与系统生成接入命令', async ({ page }) => {
    await openAI(page);
    const clients = await page.locator('#aiGenClient option').evaluateAll((os) => os.map((o) => o.value));
    const oses = await page.locator('#aiGenOS option').evaluateAll((os) => os.map((o) => o.value));
    expect(clients.length).toBeGreaterThanOrEqual(5);
    expect(oses.length).toBeGreaterThanOrEqual(3);
    await page.locator('#aiGenTokenMode').selectOption('placeholder');
    for (const c of clients) {
      for (const o of oses) {
        await page.locator('#aiGenClient').selectOption(c);
        await page.locator('#aiGenOS').selectOption(o);
        await page.locator('#aiGenTokenMode').selectOption('placeholder');
        await page.locator('#aiGenBtn').click();
        const card = page.locator('#aiSnippets');
        await expect(card).toBeVisible();
        await expect(card.locator('.ai-step-title').first()).toContainText('设置令牌环境变量');
        await expect(card.locator('pre.code').first()).toContainText('<粘贴 AI 访问令牌>');
        expect(await card.locator('ol.ai-steps > li').count()).toBeGreaterThanOrEqual(2);
      }
    }
  });

  test('新建令牌并嵌入接入命令', async ({ page }) => {
    await openAI(page);
    await page.locator('#aiGenClient').selectOption('claude');
    await page.locator('#aiGenOS').selectOption('linux');
    await page.locator('#aiGenTokenMode').selectOption('new');
    await page.locator('#aiGenNewName').fill('e2e-embed-' + Date.now());
    await page.locator('#aiGenBtn').click();
    const card = page.locator('#aiSnippets');
    await expect(card.locator('.ai-notice.warn')).toContainText('已嵌入令牌明文');
    const plain = await page.locator('#aiTkPlain').inputValue();
    await expect(card.locator('pre.code').first()).toContainText(plain);
    await expect(card).toContainText('claude mcp add');
  });

  test('Skills 清单与下载', async ({ page }) => {
    await openAI(page);
    await expect(page.getByRole('heading', { name: 'Skills', exact: true })).toBeVisible();
    await expect(page.locator('code', { hasText: 'upgrade-mgr-ops' })).toBeVisible();
    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.getByRole('button', { name: '下载 .tar.gz' }).first().click()
    ]);
    expect(download.suggestedFilename()).toMatch(/upgrade-mgr-ops\.tar\.gz$/);
  });

  test('AI 助手：只读工具自动执行', async ({ page }) => {
    await openAI(page);
    const chat = page.getByRole('heading', { name: 'AI 助手', exact: true });
    await expect(chat).toBeVisible();
    test.skip(!(await page.locator('#aiChatInput').isVisible()), '助手未启用');

    await page.locator('#aiChatInput').fill('列出主机');
    await page.locator('#aiChatSend').click();
    await expect(page.locator('.ai-msg.bot .ai-msg-body').last()).toContainText('已查询完毕', { timeout: 15000 });
    await expect(page.locator('.ai-chat-steps summary').last()).toContainText('工具调用');
  });

  test('AI 助手：写操作生成待确认卡片并可取消', async ({ page }) => {
    await openAI(page);
    test.skip(!(await page.locator('#aiChatInput').isVisible()), '助手未启用');

    await page.locator('#aiChatInput').fill('暂停任务 1');
    await page.locator('#aiChatSend').click();
    // 等待按钮变回"发送"（即请求完成）
    await expect(page.locator('#aiChatSend')).toContainText('发送', { timeout: 15000 });
    // 待确认卡片应已显示
    const pending = page.locator('.ai-notice.warn').first();
    await expect(pending).toBeVisible();
    await pending.getByRole('button', { name: '取消' }).click();
    await expect(page.locator('.ai-msg.bot').last()).toContainText('已取消');
  });

  test('英文界面可切换', async ({ page }) => {
    await openAI(page);
    await page.getByRole('button', { name: 'EN', exact: true }).click();
    await expect(page.locator('.main-nav').getByRole('button', { name: 'AI Integration' })).toBeVisible();
    await page.getByRole('button', { name: '中文', exact: true }).click();
  });
});

test.describe('设置页：来源标注与恢复', () => {
  test('AI 设置可保存并恢复为配置文件值', async ({ page }) => {
    autoAccept(page);
    await login(page);
    await page.goto('/#/settings');
    const field = page.locator('[data-setting-key="ai.mcp_rate_limit"]');
    await expect(field).toBeVisible();
    const input = field.locator('input');
    const original = await input.inputValue();
    await input.fill('77');
    await page.getByRole('button', { name: '保存设置' }).click();
    await expect(field.locator('.setting-source')).toContainText('页面');
    await field.getByRole('button', { name: '恢复为配置文件值' }).click();
    await expect(field.locator('input')).toHaveValue(original);
    await expect(field.locator('.setting-source')).not.toContainText('页面');
  });
});
