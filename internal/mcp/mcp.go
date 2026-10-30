// Package mcp 实现 Model Context Protocol 服务端（JSON-RPC 2.0 over Streamable HTTP）。
//
// 只依赖标准库，不含业务逻辑：工具定义、鉴权与审计由调用方注入。
//
// 双代（dual-era）实现：
//   - 现代版本（2026-07-28 起）：无握手、无会话。每个请求在 params._meta 中携带
//     协议版本 / clientInfo / clientCapabilities，HTTP 头携带 MCP-Protocol-Version、
//     Mcp-Method、Mcp-Name，服务端逐请求校验；提供 server/discover；列表结果带缓存提示。
//   - 旧版本（2025-11-25 及更早）：保留 initialize 握手，供尚未升级的客户端使用。
//
// 不提供服务端主动推送（GET/DELETE 返回 405）；不签发 Mcp-Session-Id。
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SupportedProtocolVersions 支持的 MCP 协议版本（新 → 旧）。
var SupportedProtocolVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// ModernProtocolVersions 无握手的现代协议版本（逐请求携带 _meta）。
var ModernProtocolVersions = []string{"2026-07-28"}

// legacyHeaderlessVersion 未携带 MCP-Protocol-Version 头的旧客户端按规范视为该版本。
const legacyHeaderlessVersion = "2025-03-26"

// _meta 键与 HTTP 头（规范固定值）。
const (
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaServerInfo         = "io.modelcontextprotocol/serverInfo"

	HeaderProtocolVersion = "MCP-Protocol-Version"
	HeaderMethod          = "Mcp-Method"
	HeaderName            = "Mcp-Name"

	// base64 哨兵格式：=?base64?{value}?=
	b64Prefix = "=?base64?"
	b64Suffix = "?="
)

// JSON-RPC 错误码（含 MCP 规范保留段）。
const (
	CodeParseError         = -32700
	CodeInvalidRequest     = -32600
	CodeMethodNotFound     = -32601
	CodeInvalidParams      = -32602
	CodeInternalError      = -32603
	CodeHeaderMismatch     = -32020
	CodeUnsupportedVersion = -32022
)

// cacheScopePrivate 工具清单按令牌身份过滤，只能由该客户端缓存。
const cacheScopePrivate = "private"

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
	ResultType        string    `json:"resultType,omitempty"`
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError"`
}

// TextResult 构造文本结果。
func TextResult(text string, isError bool) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}, IsError: isError}
}

// Info 服务端信息（每个请求读取，支持热更新）。ListCacheTTL<=0 时不返回 ttlMs。
type Info struct {
	Name         string
	Version      string
	Instructions string
	ListCacheTTL time.Duration
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
	Data    any    `json:"data,omitempty"`
}

// String 便于日志。
func (e *RPCError) String() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

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

// reqParams 公共参数：_meta 与 tools/call 等的名称字段。
type reqParams struct {
	Meta map[string]json.RawMessage `json:"_meta"`
	Name string                     `json:"name"`
	URI  string                     `json:"uri"`
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// IsModern 是否现代（无握手）协议版本。
func IsModern(v string) bool { return contains(ModernProtocolVersions, v) }

// decodeHeaderValue 解码 base64 哨兵格式；非哨兵原样返回。
func decodeHeaderValue(v string) (string, error) {
	if strings.HasPrefix(v, b64Prefix) && strings.HasSuffix(v, b64Suffix) && len(v) >= len(b64Prefix)+len(b64Suffix) {
		raw, err := base64.StdEncoding.DecodeString(v[len(b64Prefix) : len(v)-len(b64Suffix)])
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	return v, nil
}

// EncodeHeaderValue 按规范编码头部值：非可见 ASCII、首尾空白或形如哨兵时使用 base64。
func EncodeHeaderValue(v string) string {
	safe := v != "" && strings.TrimSpace(v) == v &&
		!(strings.HasPrefix(v, b64Prefix) && strings.HasSuffix(v, b64Suffix))
	for i := 0; safe && i < len(v); i++ {
		if c := v[i]; c < 0x20 || c > 0x7e {
			safe = false
		}
	}
	if safe {
		return v
	}
	return b64Prefix + base64.StdEncoding.EncodeToString([]byte(v)) + b64Suffix
}

// nameMethods 需要 Mcp-Name 头的方法 → 对应参数字段。
var nameMethods = map[string]string{"tools/call": "name", "prompts/get": "name", "resources/read": "uri"}

// result 单条消息的处理结果；status 为单条 HTTP 请求时建议的状态码。
type result struct {
	resp   *Response
	status int
}

// handle 处理单条消息。hdr 为 HTTP 请求头（批量请求或无头时可为空）。
func (s *Server) handle(ctx context.Context, req *Request, hdr http.Header) result {
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.IsNotification() {
			return result{status: http.StatusAccepted}
		}
		return result{errResp(req.ID, CodeInvalidRequest, "invalid request"), http.StatusBadRequest}
	}
	if req.IsNotification() {
		// 本版本核心协议不定义 HTTP 上的客户端通知；旧客户端的 notifications/initialized 等直接接受。
		return result{status: http.StatusAccepted}
	}
	if req.Method == "initialize" {
		return result{s.legacyInitialize(ctx, req), http.StatusOK}
	}

	var p reqParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return result{errResp(req.ID, CodeInvalidParams, "invalid params"), http.StatusBadRequest}
		}
	}
	var metaVer string
	if raw, ok := p.Meta[MetaProtocolVersion]; ok {
		_ = json.Unmarshal(raw, &metaVer)
	}
	hdrVer := ""
	if hdr != nil {
		hdrVer = strings.TrimSpace(hdr.Get(HeaderProtocolVersion))
	}

	version := metaVer
	if version == "" {
		version = hdrVer
	}
	if version == "" {
		version = legacyHeaderlessVersion
	}
	if !contains(SupportedProtocolVersions, version) {
		r := errResp(req.ID, CodeUnsupportedVersion, "Unsupported protocol version")
		r.Error.Data = map[string]any{"supported": SupportedProtocolVersions, "requested": version}
		return result{r, http.StatusBadRequest}
	}
	modern := IsModern(version) || IsModern(hdrVer)
	if modern && hdr != nil {
		if msg := validateHeaders(hdr, req.Method, metaVer, hdrVer, p); msg != "" {
			return result{errResp(req.ID, CodeHeaderMismatch, "Header mismatch: "+msg), http.StatusBadRequest}
		}
	}
	if modern && metaVer == "" {
		return result{errResp(req.ID, CodeHeaderMismatch, "Header mismatch: _meta."+MetaProtocolVersion+" is required"), http.StatusBadRequest}
	}

	ok := func(v any) result { return result{&Response{JSONRPC: "2.0", ID: req.ID, Result: v}, http.StatusOK} }
	info := s.Provider.Info(ctx)
	withCache := func(m map[string]any) map[string]any {
		if modern {
			m["resultType"] = "complete"
			m["cacheScope"] = cacheScopePrivate
			if info.ListCacheTTL > 0 {
				m["ttlMs"] = info.ListCacheTTL.Milliseconds()
			}
		}
		return m
	}

	switch req.Method {
	case "server/discover":
		res := withCache(map[string]any{
			"supportedVersions": SupportedProtocolVersions,
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"_meta":             map[string]any{MetaServerInfo: map[string]any{"name": info.Name, "version": info.Version}},
		})
		if strings.TrimSpace(info.Instructions) != "" {
			res["instructions"] = info.Instructions
		}
		return ok(res)
	case "ping":
		if modern {
			return ok(map[string]any{"resultType": "complete"})
		}
		return ok(map[string]any{})
	case "tools/list":
		tools := s.Provider.ListTools(ctx)
		if tools == nil {
			tools = []ToolInfo{}
		}
		return ok(withCache(map[string]any{"tools": tools}))
	case "tools/call":
		var cp struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &cp); err != nil || cp.Name == "" {
			return result{errResp(req.ID, CodeInvalidParams, "invalid params: name is required"), http.StatusOK}
		}
		if len(cp.Arguments) == 0 || string(cp.Arguments) == "null" {
			cp.Arguments = json.RawMessage("{}")
		}
		res, err := s.Provider.CallTool(ctx, cp.Name, cp.Arguments)
		if errors.Is(err, ErrUnknownTool) {
			return result{errResp(req.ID, CodeInvalidParams, "unknown tool: "+cp.Name), http.StatusOK}
		}
		if err != nil {
			return result{errResp(req.ID, CodeInternalError, "tool call failed"), http.StatusOK}
		}
		if modern {
			res.ResultType = "complete"
		}
		return ok(res)
	case "resources/list":
		return ok(withCache(map[string]any{"resources": []any{}}))
	case "prompts/list":
		return ok(withCache(map[string]any{"prompts": []any{}}))
	default:
		status := http.StatusOK
		if modern {
			status = http.StatusNotFound
		}
		return result{errResp(req.ID, CodeMethodNotFound, "method not found: "+req.Method), status}
	}
}

// validateHeaders 现代版本：HTTP 头必须存在且与请求体一致（防止网关与服务端依据不同来源）。
func validateHeaders(hdr http.Header, method, metaVer, hdrVer string, p reqParams) string {
	if hdrVer == "" {
		return HeaderProtocolVersion + " header is required"
	}
	if metaVer != "" && hdrVer != metaVer {
		return fmt.Sprintf("%s header value '%s' does not match body value '%s'", HeaderProtocolVersion, hdrVer, metaVer)
	}
	hm := hdr.Get(HeaderMethod)
	if hm == "" {
		return HeaderMethod + " header is required"
	}
	if hm != method {
		return fmt.Sprintf("%s header value '%s' does not match body value '%s'", HeaderMethod, hm, method)
	}
	field, need := nameMethods[method]
	if !need {
		return ""
	}
	want := p.Name
	if field == "uri" {
		want = p.URI
	}
	raw := hdr.Get(HeaderName)
	if raw == "" {
		return HeaderName + " header is required"
	}
	got, err := decodeHeaderValue(raw)
	if err != nil {
		return HeaderName + " header has invalid base64 encoding"
	}
	if got != want {
		return fmt.Sprintf("%s header value '%s' does not match body value '%s'", HeaderName, got, want)
	}
	return ""
}

// legacyInitialize 旧版本握手：回显受支持的旧版本，否则返回最新旧版本。
func (s *Server) legacyInitialize(ctx context.Context, req *Request) *Response {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	legacy := make([]string, 0, len(SupportedProtocolVersions))
	for _, v := range SupportedProtocolVersions {
		if !IsModern(v) {
			legacy = append(legacy, v)
		}
	}
	version := legacy[0]
	if contains(legacy, p.ProtocolVersion) {
		version = p.ProtocolVersion
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
	return &Response{JSONRPC: "2.0", ID: req.ID, Result: res}
}

// Handle 处理单条消息（无 HTTP 头上下文，按旧版本或 _meta 判定）；通知返回 nil。
func (s *Server) Handle(ctx context.Context, req *Request) *Response {
	return s.handle(ctx, req, nil).resp
}

// ServeHTTP 处理 Streamable HTTP POST。
//
// 调用方负责鉴权、限流与请求体大小限制（应在调用前设置 http.MaxBytesReader）。
// 批量数组仅为旧版本保留（现代版本每个 POST 只能是单条消息）。
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
		if IsModern(strings.TrimSpace(r.Header.Get(HeaderProtocolVersion))) {
			writeJSON(w, http.StatusBadRequest, errResp(nil, CodeInvalidRequest, "batch requests are not supported in this protocol version"))
			return
		}
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
			if res := s.handle(r.Context(), &reqs[i], nil); res.resp != nil {
				out = append(out, res.resp)
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
	res := s.handle(r.Context(), &req, r.Header)
	if res.resp == nil {
		// 通知 / 客户端发来的响应：202 无响应体。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, res.status, res.resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
