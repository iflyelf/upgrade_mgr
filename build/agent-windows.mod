// Agent Windows 专用 modfile：Go 1.20 是最后一个支持 Windows Server 2008 R2 的版本。
// 单套 windows/amd64 与 windows/arm64 构建同时覆盖 Server 2008 R2 ~ Windows 11。
// 唯一第三方依赖是 golang.org/x/sys（SCM 服务宿主与服务管理），请勿添加其他依赖。
// 用法见 Makefile 的 agent-windows 目标。
module github.com/iflyelf/upgrade_mgr

go 1.20

require golang.org/x/sys v0.18.0
