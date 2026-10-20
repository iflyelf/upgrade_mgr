// Agent 专用 modfile：Agent 仅依赖标准库，可用 Go 1.23 编译以兼容 CentOS 6（内核 2.6.32）。
// 用法见 Makefile 的 agent-legacy / test-agent-legacy 目标。请勿在此添加第三方依赖。
module github.com/iflyelf/upgrade_mgr

go 1.23
