# 修复任务清单

## 问题
1. ❌ 本地用户不能修改密码（无后端接口，无前端入口）
2. ❌ 组件/制品页面无法操作（后端API已有，前端仅显示只读表格）

## 计划

### 后端：修改密码
- [ ] `internal/auth/builtin.go`: 添加 `ChangePassword(ctx, userID, oldPw, newPw) (updatedAt, newToken, error)`
  - 验证旧密码、更新 password_hash、返回 updated_at 用于签发新会话
  - 删除 cache 条目使旧会话失效
- [ ] `internal/auth/builtin.go`: 添加 `InvalidateUser(userID)` 清理缓存
- [ ] `internal/auth/session.go`: 添加 `signAt(u *User, iat time.Time)` 支持指定 iat
- [ ] `internal/auth/auth.go`: 添加 `ValidatePassword(old, new string) error`（8-72字节，新旧不同）
- [ ] `internal/handler/auth.go`: 添加 `ChangePassword(s)` handler
  - 仅 builtin 支持，casdoor 返回 400
  - 调用 Builtin.ChangePassword，audit，SetSession
- [ ] `internal/pkg/audit/audit.go`: 添加 `ActionPasswordChange` 常量
- [ ] `cmd/server/main.go`: 注册 `PUT /api/auth/password` (RequireAuth + RateLimit)

### 后端：管理员重置密码（可选，暂不实现）
- [ ] `internal/rbac/rbac.go`: 添加 `ResetPassword(ctx, actor, username, newPw) (userID, error)`
  - 租户范围校验，非 superadmin 不能重置 superadmin
- [ ] `internal/handler/user.go`: 添加 `UserResetPassword(s)` handler
- [ ] `cmd/server/main.go`: 注册 `PUT /api/users/{username}/password` (RequireAuth + RequireAdmin)

### 前端：修改密码
- [ ] `web/dist/assets/app.js`: topbar 增加"修改密码"按钮
- [ ] 添加 `passwordView()` 视图（旧密码、新密码、确认密码）
- [ ] 提交到 `PUT /api/auth/password`，成功后 alert 并返回主页

### 前端：组件管理
- [ ] `web/dist/assets/app.js`: `componentsView()` 增加新建按钮和编辑表单
- [ ] 表单字段：name、display_name、deploy_mode、binary_name、install_path、service_name、service_mgr、os_match、arch_match、enabled
- [ ] 高级配置 JSON textarea（exec_path、exec_args、pre_install、post_install 等）
- [ ] 编辑时加载完整组件对象，合并表单修改后提交 PUT
- [ ] 表格增加编辑/删除操作列
- [ ] 扩展 `simpleTable()` 支持渲染 DOM 节点作为单元格内容

### 前端：制品管理
- [ ] `web/dist/assets/app.js`: `artifactsView()` 增加上传表单
- [ ] 表单字段：component_id（下拉选择）、version、arch、os_family、inner_path、notes、file
- [ ] 使用 FormData + fetch 上传（Content-Type 由浏览器设置 multipart）
- [ ] 成功后刷新列表

### CSS 增强
- [ ] `web/dist/assets/app.css`: 增加 textarea 样式
- [ ] 增加 `.btn-secondary`、`.btn-danger` 样式变体

### 测试
- [ ] 单元测试：`internal/auth/builtin_test.go` 测试 ChangePassword
- [ ] 集成测试：`cmd/server/main_test.go` 端到端测试修改密码流程
- [ ] 手动测试：组件增删改、制品上传

## 验证步骤
1. `make build` 编译通过
2. `make vet` 静态检查通过
3. `DATABASE_URL=... make test` 测试通过
4. 启动服务，登录后验证：
   - 修改密码成功，旧密码失效
   - 组件创建、编辑、删除
   - 制品上传、列表显示
