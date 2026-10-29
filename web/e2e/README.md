# 前端 E2E（Playwright）

覆盖：登录失败/成功、页签导航、URL 刷新恢复、创建组件、审计/组织页面、英文切换，
以及主机「按筛选条件全选 → 后台删除进度 → 刷新恢复」与组件多选批量删除。

主机批量删除用例需要直接写库造数：设置 `E2E_DATABASE_URL`（并安装 `psql`），未设置时自动跳过。

## 本地运行（国内网络）

```bash
# 1. 启动控制台（示例）
go build -o /tmp/upgrade-mgr ./cmd/server
/tmp/upgrade-mgr -c /tmp/config.yaml   # 需 PostgreSQL，admin 密码与下方一致

# 2. 安装依赖与浏览器（.npmrc 已指向 npmmirror）
cd web/e2e
npm ci
PLAYWRIGHT_DOWNLOAD_HOST=https://npmmirror.com/mirrors/playwright \
  npx playwright install --with-deps chromium

# 3. 运行
BASE_URL=http://127.0.0.1:18090 npx playwright test
```

镜像说明：

- npm：`web/e2e/.npmrc` 默认 `registry=https://registry.npmmirror.com`；
- Playwright 浏览器：设置 `PLAYWRIGHT_DOWNLOAD_HOST=https://npmmirror.com/mirrors/playwright`
  （CI 工作流已默认配置）；
- 内网离线：在 CI/本机预装浏览器后设置 `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`，
  或用 `PLAYWRIGHT_BROWSERS_PATH` 指向共享缓存目录；
- 官方源：`npm_config_registry=https://registry.npmjs.org PLAYWRIGHT_DOWNLOAD_HOST= npx playwright install chromium`。

## CI

`.github/workflows/web-e2e.yml` 会自动启动 PostgreSQL 与控制台；工作流已配置
npmmirror 与 `GOPROXY=https://goproxy.cn,direct`，适配国内 runner。
