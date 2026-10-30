// Package llm OpenAI 兼容的 LLM 客户端（支持 tool calling）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config LLM 配置（全部来自设置快照，不设默认值）。
type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	AuthHeader   string // 默认 "Authorization"
	AuthScheme   string // 默认 "Bearer"，留空则直接发 Key
	Temperature  string // 留空使用模型默认
	MaxTokens    int    // 0 表示不传参
	Timeout      time.Duration
	MaxRespBytes int // 响应体最大字节数
}

// Message 消息（支持 user/assistant/tool 角色）。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 工具调用请求。
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool 工具定义。
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// Response LLM 响应。
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Usage token 使用统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Client LLM 客户端。
type Client struct {
	cfg Config
}

// New 创建客户端（cfg 全部来自快照，此处不设默认值）。
func New(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Chat 发起对话（携带工具定义）。
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*Response, error) {
	req := map[string]any{"model": c.cfg.Model, "messages": messages}
	if c.cfg.Temperature != "" {
		if temp, err := parseFloat(c.cfg.Temperature); err == nil && temp >= 0 {
			req["temperature"] = temp
		}
	}
	if c.cfg.MaxTokens > 0 {
		req["max_tokens"] = c.cfg.MaxTokens
	}
	if len(tools) > 0 {
		req["tools"] = tools
		req["tool_choice"] = "auto"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败")
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" && c.cfg.AuthHeader != "" {
		val := c.cfg.APIKey
		if c.cfg.AuthScheme != "" {
			val = c.cfg.AuthScheme + " " + val
		}
		httpReq.Header.Set(c.cfg.AuthHeader, val)
	}

	client := &http.Client{Timeout: c.cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("LLM 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody := io.LimitReader(resp.Body, int64(c.cfg.MaxRespBytes))
	if resp.StatusCode != 200 {
		preview := readPreview(respBody, 500)
		return nil, fmt.Errorf("LLM 返回 HTTP %d: %s", resp.StatusCode, sanitize(preview))
	}

	var result struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析 LLM 响应失败")
	}
	if result.Error.Message != "" {
		return nil, fmt.Errorf("LLM 错误: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("LLM 未返回结果")
	}

	return &Response{
		Message:      result.Choices[0].Message,
		FinishReason: result.Choices[0].FinishReason,
		Usage:        result.Usage,
	}, nil
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func readPreview(r io.Reader, max int) string {
	buf := make([]byte, max)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// sanitize 脱敏错误消息（移除可能的 API key）。
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	// 移除看起来像 token 的长字符串
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 40 && !strings.Contains(w, "http") {
			words[i] = "***"
		}
	}
	return strings.Join(words, " ")
}
