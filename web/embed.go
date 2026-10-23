package web

import "embed"

// DistFS 内嵌前端构建产物（dist）。
//
// P0 内嵌最小可用页面（登录 + 主机列表 + 设置 + 主题切换）；
// 后续接入 Vue3 构建时，用 web/src 构建输出覆盖 web/dist 即可，无需改代码。
//
//go:embed all:dist
var DistFS embed.FS

// QuickstartJSON 快速上手结构化文案（由 web/quickstart.yaml 通过 make docs-quickstart 生成）。
//
//go:embed quickstart.json
var QuickstartJSON []byte
