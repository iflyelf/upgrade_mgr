// Package errutil 错误处理工具：过滤敏感信息，防止泄露内部细节。
package errutil

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// 敏感信息匹配模式
	pathPattern     = regexp.MustCompile(`/[\w\-./]+`)                    // 文件路径
	ipPattern       = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`) // IP 地址
	sqlPattern      = regexp.MustCompile(`(?i)(SELECT|INSERT|UPDATE|DELETE|FROM|WHERE)\s+\S+`) // SQL 片段
	dbConnPattern   = regexp.MustCompile(`postgresql://[^\s]+`)          // 数据库连接串
	tokenPattern    = regexp.MustCompile(`token[=:]\s*[\w\-]+`)          // token 值
)

// Sanitize 过滤错误消息中的敏感信息（供返回给客户端）。
// 保留错误类型，脱敏路径/SQL/IP/token 等细节。
func Sanitize(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	
	// 数据库错误
	if strings.Contains(msg, "pq:") || strings.Contains(msg, "sql:") {
		return "数据库操作失败"
	}
	if dbConnPattern.MatchString(msg) {
		return "数据库连接失败"
	}
	
	// 文件系统错误
	if strings.Contains(msg, "no such file") || strings.Contains(msg, "permission denied") {
		return "文件操作失败"
	}
	
	// 过滤具体路径
	msg = pathPattern.ReplaceAllString(msg, "/***")
	
	// 过滤 IP 地址（保留格式）
	msg = ipPattern.ReplaceAllString(msg, "***.***.***.***")
	
	// 过滤 SQL 片段
	msg = sqlPattern.ReplaceAllString(msg, "SQL查询")
	
	// 过滤 token
	msg = tokenPattern.ReplaceAllString(msg, "token=***")
	
	// 通用错误消息（超过 200 字符截断）
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	
	return msg
}

// SafeError 创建安全的错误响应（开发环境返回详细错误，生产环境脱敏）。
func SafeError(err error, devMode bool) error {
	if err == nil {
		return nil
	}
	if devMode {
		return err // 开发环境返回原始错误
	}
	return fmt.Errorf("%s", Sanitize(err))
}

// IsNotFound 判断是否为"不存在"类错误（无需脱敏）。
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "不存在") || 
	       strings.Contains(msg, "not found") ||
	       strings.Contains(msg, "no rows")
}
