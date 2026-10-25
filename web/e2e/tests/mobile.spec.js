const { test, expect } = require('@playwright/test');

const ADMIN = process.env.E2E_ADMIN || 'admin';
const PASSWORD = process.env.E2E_PASSWORD || 'admin123';

async function login(page) {
  await page.goto('/');
  await page.locator('input[autocomplete="username"]').fill(ADMIN);
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.locator('.mobile-tabs')).toBeVisible();
}

// H5 回归：窄屏应使用移动页签、表格卡片化、无横向溢出，且主题色跟随切换。
test.describe('H5 移动端自适应', () => {
  test.use({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true
  });

  test('窄屏布局、表格卡片化与主题色', async ({ page }) => {
    await login(page);

    // 桌面导航隐藏，移动端页签显示。
    await expect(page.locator('.main-nav')).toBeHidden();
    await expect(page.locator('.mobile-tabs')).toBeVisible();

    // 人员组织页有表格，用它验证表格 -> 卡片布局。
    await page.locator('.mobile-tabs .tab', { hasText: '人员组织' }).click();
    const cards = page.locator('table.mobile-cards');
    await expect(cards.first()).toBeVisible();
    const cardStyle = await cards.first().evaluate((table) => {
      const tr = table.querySelector('tbody tr');
      const td = table.querySelector('tbody td');
      const thead = table.querySelector('thead');
      return {
        trDisplay: tr ? getComputedStyle(tr).display : '',
        tdDisplay: td ? getComputedStyle(td).display : '',
        theadDisplay: thead ? getComputedStyle(thead).display : '',
        label: td ? td.getAttribute('data-label') : ''
      };
    });
    expect(cardStyle.trDisplay).toBe('block');
    expect(cardStyle.tdDisplay).toBe('block');
    expect(cardStyle.theadDisplay).toBe('none');
    expect(cardStyle.label).toBeTruthy();

    // 输入框 >=16px，避免 iOS 聚焦自动放大。
    await page.locator('.mobile-tabs .tab', { hasText: '主机' }).click();
    await expect(page.locator('input[placeholder*="IP/出口"]')).toBeVisible();
    const fontSize = await page.evaluate(() => {
      const el = Array.prototype.slice.call(document.querySelectorAll('input'))
        .filter(function (x) { return x.type !== 'checkbox' && x.offsetParent !== null; })[0];
      return el ? parseFloat(getComputedStyle(el).fontSize) : 0;
    });
    expect(fontSize).toBeGreaterThanOrEqual(16);

    // 页面不能出现横向滚动条。用 visualViewport 作为设备宽度基准：
    // 若内容把布局视口撑宽，window.innerWidth 会同步变大，从而掩盖问题。
    const hasHorizontalOverflow = await page.evaluate(() => {
      const vw = (window.visualViewport && window.visualViewport.width) || window.innerWidth;
      return document.documentElement.scrollWidth > vw + 1;
    });
    expect(hasHorizontalOverflow).toBe(false);

    // 任务页工具栏包含带长选项的 select，单独回归一次，防止再次撑宽页面。
    await page.locator('.mobile-tabs .tab', { hasText: '任务' }).click();
    await expect(page.locator('input[placeholder="按任务名搜索"]')).toBeVisible();
    const taskOverflow = await page.evaluate(() => {
      const vw = (window.visualViewport && window.visualViewport.width) || window.innerWidth;
      return { overflow: document.documentElement.scrollWidth > vw + 1, vw };
    });
    expect(taskOverflow.overflow).toBe(false);
    expect(taskOverflow.vw).toBe(390);

    // 设置页含超长 URL/域名说明，回归卡片不能被撑宽。
    await page.locator('.mobile-tabs .tab', { hasText: '设置' }).click();
    await page.waitForTimeout(400);
    const settingsOverflow = await page.evaluate(() => {
      const vw = (window.visualViewport && window.visualViewport.width) || window.innerWidth;
      return { overflow: document.documentElement.scrollWidth > vw + 1, sw: document.documentElement.scrollWidth, vw };
    });
    expect(settingsOverflow.overflow).toBe(false);

    // 帮助弹窗在窄屏内可完整显示，宽度不超出视口。
    await page.locator('.mobile-tabs .tab', { hasText: '帮助' }).click();
    const modal = page.locator('.help-modal');
    await expect(modal).toBeVisible();
    const box = await modal.boundingBox();
    expect(box.width).toBeLessThanOrEqual(390);
    await modal.getByRole('button', { name: '关闭' }).click();

    // 主题色 meta 跟随主题，移动端状态栏颜色一致。
    await page.locator('.theme-btn[title="暗黑"]').click();
    await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#171a1f');
    await page.locator('.theme-btn[title="暖沙米"]').click();
    await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#f5efe6');
  });
});
