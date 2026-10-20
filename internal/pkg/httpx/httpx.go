// Package httpx 提供统一的 JSON 响应与请求辅助。
package httpx

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/errutil"
)

// Resp 统一响应结构 {code,message,data}。
type Resp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// OK 写入成功响应。
func OK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Resp{Code: 0, Message: "ok", Data: data})
}

// Fail 写入失败响应（HTTP 状态码 + 业务 message）。
//
// 注意：msg 会原样返回客户端，调用方须确保不含内部细节。
// 若要返回 error，请用 FailErr（自动脱敏）。
func Fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Resp{Code: status, Message: msg})
}

// FailErr 写入失败响应：服务端记录完整错误，客户端只收到脱敏消息。
//
// 安全：避免把 pq/SQL/文件路径/连接串/token 等内部细节泄露给调用方。
// 业务可读错误（如「制品不存在」）由 errutil 原样保留。
func FailErr(w http.ResponseWriter, status int, err error) {
	if err == nil {
		Fail(w, status, "请求失败")
		return
	}
	log.Printf("❌ API 错误 [%d]: %v", status, err)
	writeJSON(w, status, Resp{Code: status, Message: errutil.Sanitize(err)})
}

// FailErrf 同 FailErr，但可附加面向用户的前缀说明。
func FailErrf(w http.ResponseWriter, status int, prefix string, err error) {
	if err == nil {
		Fail(w, status, prefix)
		return
	}
	log.Printf("❌ API 错误 [%d] %s: %v", status, prefix, err)
	writeJSON(w, status, Resp{Code: status, Message: prefix + ": " + errutil.Sanitize(err)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON 解析请求体到 v。
func DecodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
