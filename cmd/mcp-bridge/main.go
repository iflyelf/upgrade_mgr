// upgrade-mgr-mcp-bridge: stdio bridge for MCP clients without HTTP support.
//
// Reads JSON-RPC 2.0 messages from stdin, POSTs to MCP server, writes responses to stdout.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/mcp"
)

var (
	urlFlag      = flag.String("url", os.Getenv("UPGRADE_MGR_MCP_URL"), "MCP server URL")
	tokenEnvFlag = flag.String("token-env", os.Getenv("UPGRADE_MGR_TOKEN_ENV"), "Token environment variable name")
	timeoutFlag  = flag.Duration("timeout", 120*time.Second, "Request timeout")
	maxLineFlag  = flag.Int("max-line", 16<<20, "Max bytes per JSON-RPC line on stdin")
)

func main() {
	flag.Parse()

	if *urlFlag == "" {
		log.Fatal("Missing -url (or UPGRADE_MGR_MCP_URL)")
	}
	if *tokenEnvFlag == "" {
		log.Fatal("Missing -token-env (or UPGRADE_MGR_TOKEN_ENV): name of the env var holding the AI token")
	}

	token := os.Getenv(*tokenEnvFlag)
	if token == "" {
		log.Fatalf("Environment variable %s not set", *tokenEnvFlag)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), *maxLineFlag)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse to check if it has an id (notification vs request)
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		hasID := json.Unmarshal(line, &msg) == nil && len(msg.ID) > 0 && string(msg.ID) != "null"

		ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
		resp, err := post(ctx, *urlFlag, token, line)
		cancel()

		if err != nil {
			if hasID {
				// Return JSON-RPC error
				errResp := map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"error":   map[string]any{"code": -32603, "message": err.Error()},
				}
				json.NewEncoder(os.Stdout).Encode(errResp)
			} else {
				log.Printf("Error (notification): %v", err)
			}
			continue
		}

		if len(resp) > 0 {
			os.Stdout.Write(resp)
			os.Stdout.Write([]byte("\n"))
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}

// httpClient 复用连接；超时由每次请求的 context 控制。
var httpClient = &http.Client{}

func post(ctx context.Context, url, token string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range routingHeaders(body) {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// 202: notification acknowledged, no response body
		return nil, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		// 4xx 携带 JSON-RPC 错误（如 -32020 / -32022）时原样透传，客户端据此重试或降级。
		if isJSONRPCError(respBody) {
			return bytes.TrimSpace(respBody), nil
		}
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	return respBody, nil
}

// routingHeaders 从单条消息推导现代协议（2026-07-28）要求的 HTTP 头，值与请求体保持一致。
// 消息未在 _meta 声明版本（旧客户端）或为批量数组时不加头，沿用旧版本行为。
func routingHeaders(body []byte) map[string]string {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
			Name string                     `json:"name"`
			URI  string                     `json:"uri"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &msg) != nil || msg.Method == "" {
		return nil
	}
	var ver string
	if raw, ok := msg.Params.Meta[mcp.MetaProtocolVersion]; ok {
		_ = json.Unmarshal(raw, &ver)
	}
	if ver == "" {
		// 旧客户端：保持原有行为，由服务端按 initialize 握手处理。
		return nil
	}
	h := map[string]string{
		mcp.HeaderProtocolVersion: ver,
		mcp.HeaderMethod:          msg.Method,
	}
	switch msg.Method {
	case "tools/call", "prompts/get":
		h[mcp.HeaderName] = mcp.EncodeHeaderValue(msg.Params.Name)
	case "resources/read":
		h[mcp.HeaderName] = mcp.EncodeHeaderValue(msg.Params.URI)
	}
	return h
}

func isJSONRPCError(b []byte) bool {
	var m struct {
		JSONRPC string          `json:"jsonrpc"`
		Error   json.RawMessage `json:"error"`
	}
	return json.Unmarshal(b, &m) == nil && m.JSONRPC == "2.0" && len(m.Error) > 0
}
