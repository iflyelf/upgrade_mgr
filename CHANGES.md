# 修复说明

## 问题
1. **本地用户不能修改密码**：后端无修改密码接口，前端无入口
2. **组件/制品页面无法操作**：后端 API 已有，前端仅显示只读表格

## 修复内容

### 后端：密码修改功能
- ✅ `internal/auth/auth.go`: 新增 `ValidatePassword()` 和 `ErrWrongPassword`
- ✅ `internal/auth/builtin.go`: 新增 `ChangePassword()` 方法
  - 验证旧密码、更新 password_hash
  - 删除缓存条目使旧会话失效
  - 返回新会话 token（iat 向上取整避免精度问题）
- ✅ `internal/auth/builtin.go`: 新增 `InvalidateUser()` 清理缓存
- ✅ `internal/auth/session.go`: 新增 `signAt()` 支持指定 iat
- ✅ `internal/handler/auth.go`: 新增 `ChangePassword()` handler
  - 仅 builtin 支持，casdoor 返回 400
  - 审计日志记录
- ✅ `internal/pkg/audit/audit.go`: 新增 `ActionPasswordChange` 常量
- ✅ `cmd/server/main.go`: 注册 `PUT /api/auth/password` (RequireAuth + RateLimit)

### 前端：密码修改
- ✅ topbar 增加"修改密码"按钮
- ✅ 新增 `passwordView()` 视图（旧密码、新密码、确认密码）
- ✅ 客户端验证：密码长度 ≥8、两次输入一致
- ✅ 提交到 `PUT /api/auth/password`，成功后 alert 并返回主页

### 前端：组件管理
- ✅ `componentsView()` 增加"新建组件"按钮
- ✅ 编辑表单支持：
  - 基础字段：name、display_name、deploy_mode、binary_name、install_path、service_name、service_mgr、os_match、arch_match、enabled
  - 高级配置：JSON textarea 覆盖 exec_path、exec_args、pre_install、post_install 等
- ✅ 编辑时加载完整组件对象，合并表单修改后提交 PUT
- ✅ 表格增加编辑/删除操作列（仅管理员可见）
- ✅ 删除前确认提示
- ✅ 扩展 `simpleTable()` 支持渲染 DOM 节点作为单元格内容

### 前端：制品管理
- ✅ `artifactsView()` 增加上传表单（仅管理员可见）
- ✅ 表单字段：component_id（下拉选择）、version、arch、os_family、inner_path、notes、file
- ✅ 使用 FormData + fetch 上传（Content-Type 由浏览器设置 multipart）
- ✅ 成功后刷新列表
- ✅ 表格显示组件名称（而非 ID）、文件大小格式化

### 测试
- ✅ 新增 `internal/auth/builtin_test.go`
  - `TestChangePassword`: 端到端测试密码修改流程
  - `TestValidatePassword`: 单元测试密码验证规则
- ✅ 所有测试通过

## 技术细节

### 密码修改安全性
1. **旧会话失效**：更新 `updated_at` 后，iat 早于该时间的会话均失效
2. **缓存清理**：主动删除缓存条目，确保其他副本也能检测到失效
3. **乐观锁**：更新时比较当前哈希，防止并发修改冲突
4. **密码验证**：8-72 字节（bcrypt 限制），新旧密码不能相同
5. **时间精度处理**：iat 向上取整到毫秒，避免与 DB 微秒精度的 updated_at 比较时产生误判
6. **限流保护**：复用 loginLimiter 防止暴力破解
7. **审计日志**：记录操作，不记录密码明文

### 前端设计
1. **组件编辑合并策略**：编辑时从完整对象开始，避免 PUT 覆盖丢失未编辑字段
2. **高级配置 JSON**：通过 textarea 暴露完整配置能力，避免表单过于复杂
3. **权限控制**：管理员操作按钮通过 `state.user.IsAdmin` 条件渲染
4. **错误提示**：统一通过 `state.err` 显示，操作成功后清空

## 代码统计
- 修改文件：7 个
- 新增文件：2 个（测试 + 文档）
- 新增代码：+463 行，-9 行

## 验证步骤
```bash
# 编译
make build

# 静态检查
go vet ./...

# 单元测试
go test ./...

# 集成测试（需设置 DATABASE_URL）
DATABASE_URL="postgresql://user:pass@host:port/db" go test -v ./internal/auth -run TestChangePassword

# 手动测试
./bin/upgrade-mgr
# 1. 登录后点击"修改密码"，验证旧密码错误时提示
# 2. 输入正确旧密码和新密码，提交成功后旧密码应无法登录
# 3. 点击"组件"标签，点击"新建组件"，填写表单提交
# 4. 点击"编辑"修改组件，点击"删除"确认删除
# 5. 点击"制品"标签，选择组件上传文件，查看列表
```

## 后续建议
1. **用户管理页面**：增加用户列表、创建、启用/禁用、管理员重置密码（已有后端 RBAC API）
2. **制品删除**：当前无后端 DELETE 端点，可根据需要添加
3. **制品上传进度**：考虑使用 XMLHttpRequest 替代 fetch，显示上传进度
4. **审计日志查询页面**：audit_logs 表已有数据，可添加前端查询界面
5. **全局错误处理**：401 响应统一跳转登录，避免静默失败
