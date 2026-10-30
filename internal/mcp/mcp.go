// Package mcp 实现 Model Context Protocol 服务端（JSON-RPC 2.0 over Streamable HTTP）。
//
// 只依赖标准库，不含业务逻辑：工具定义、鉴权与审计由调用方注入。
// 支持 initialize / ping / tools/list / tools/call 与 notifications/*；
// 不提供服务端主动推送（GET 返回 405，符合 Streamable HTTP 规范的可选项）。
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SupportedProtocolVersions 支持的 MCP 协议版本（新 → 旧）。客户端请求的版本受支持时原样回显，
// 否则返回最新版本，由客户端决定是否继续。
var SupportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 标准错误码。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Annotations 工具行为提示（供 AI 客户端决定是否需要用户确认）。
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// ToolInfo tools/list 返回的工具描述。
type ToolInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// Content 工具结果内容块（仅使用 text）。
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallResult tools/call 结果。IsError=true 表示工具执行失败（模型可见、可自我纠正）。
type CallResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError"`
}

// TextResult 构造文本结果。
func TextResult(text string, isError bool) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}, IsError: isError}
}

// Info 服务端信息（每次 initialize 时读取，支持热更新）。
type Info struct {
	Name         string
	Version      string
	Instructions string
}

// Provider 由调用方实现：按当前请求的身份列出与执行工具。
type Provider interface {
	Info(ctx context.Context) Info
	ListTools(ctx context.Context) []ToolInfo
	// CallTool 返回的 error 只用于协议级失败（如未知工具）；业务失败应返回 IsError 结果。
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error)
}

// ErrUnknownTool 未知或当前身份不可用的工具。
var ErrUnknownTool = errors.New("unknown tool")

// Request JSON-RPC 请求/通知。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification 无 id 的消息为通知，不需要响应。
func (r *Request) IsNotification() bool { return len(r.ID) == 0 || string(r.ID) == "null" }

// Response JSON-RPC 响应。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func errResp(id json.RawMessage, code int, msg string) *Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// Server MCP 服务端。
type Server struct {
	Provider Provider
}

// Handle 处理单条消息；通知返回 nil。
func (s *Server) Handle(ctx context.Context, req *Request) *Response {
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.IsNotification() {
			return nil
		}
		return errResp(req.ID, CodeInvalidRequest, "invalid request")
	}
	if req.IsNotification() {
		// notifications/initialized、notifications/cancelled 等：无需响应。
		return nil
	}
	ok := func(v any) *Response { return &Response{JSONRPC: "2.0", ID: req.ID, Result: v} }
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := SupportedProtocolVersions[0]
		for _, v := range SupportedProtocolVersions {
			if v == p.ProtocolVersion {
				version = v
				break
			}
		}
		info := s.Provider.Info(ctx)
		res := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": info.Name, "version": info.Version},
		}
		if strings.TrimSpace(info.Instructions) != "" {
			res["instructions"] = info.Instructions
		}
		return ok(res)
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		tools := s.Provider.ListTools(ctx)
		if tools == nil {
			tools = []ToolInfo{}
		}
		return ok(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return errResp(req.ID, CodeInvalidParams, "invalid params: name is required")
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		res, err := s.Provider.CallTool(ctx, p.Name, p.Arguments)
		if errors.Is(err, ErrUnknownTool) {
			return errResp(req.ID, CodeInvalidParams, "unknown tool: "+p.Name)
		}
		if err != nil {
			return errResp(req.ID, CodeInternalError, "tool call failed")
		}
		return ok(res)
	case "resources/list":
		return ok(map[string]any{"resources": []any{}})
	case "prompts/list":
		return ok(map[string]any{"prompts": []any{}})
	default:
		return errResp(req.ID, CodeMethodNotFound, "method not found: "+req.Method)
	}
}

// ServeHTTP 处理 Streamable HTTP POST：body 为单条消息或批量数组。
//
// 调用方负责鉴权、限流与请求体大小限制（应在调用前设置 http.MaxBytesReader）。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errResp(nil, CodeInvalidRequest, "request body too large"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errResp(nil, CodeParseError, "read body failed"))
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errResp(nil, CodeParseError, "empty body"))
		return
	}
	if body[0] == '[' {
		var reqs []Request
		if err := json.Unmarshal(body, &reqs); err != nil {
			writeJSON(w, http.StatusBadRequest, errResp(nil, CodeParseError, "parse error"))
			return
		}
		if len(reqs) == 0 {
			writeJSON(w, http.StatusBadRequest, errResp(nil, CodeInvalidRequest, "empty batch"))
			return
		}
		out := make([]*Response, 0, len(reqs))
		for i := range reqs {
			if resp := s.Handle(r.Context(), &reqs[i]); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp(nil, CodeParseError, "parse error"))
		return
	}
	resp := s.Handle(r.Context(), &req)
	if resp == nil {
		// 通知 / 客户端发来的响应：202 无响应体。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// String 便于日志。
func (e *RPCError) String() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }
