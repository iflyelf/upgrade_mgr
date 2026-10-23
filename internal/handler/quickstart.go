package handler

import (
	"encoding/json"
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/web"
)

// Quickstart 返回快速上手结构化文案（中英双语，页面按当前语言渲染）。
//
// 数据源：web/quickstart.yaml → web/quickstart.json（make docs-quickstart）。
func Quickstart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, json.RawMessage(web.QuickstartJSON))
	}
}
