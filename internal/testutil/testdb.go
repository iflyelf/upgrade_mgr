// Package testutil 集成测试的基础设施。
//
// 只被 *_test.go 引用，不会进入生产二进制。
package testutil

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// DSN 返回集成测试使用的数据库连接串，并对生产库做硬性保护。
//
// 规则（按顺序）：
//   - 未设置 DATABASE_URL：跳过测试；
//   - 显式设置 ALLOW_PROD_DB_TESTS=1：放行（仅供临时排查，测试必须自行快照/恢复）；
//   - 连接 localhost / 127.0.0.1 / ::1：视为测试库（CI 使用）；
//   - 数据库名包含 test，或 search_path 包含 test：视为独立测试库/schema；
//   - 其它情况（生产地址、public schema）：直接跳过，拒绝运行。
//
// 这样即使开发机误把生产 DATABASE_URL 导进来，测试也不会写生产配置。
func DSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过集成测试")
	}
	if strings.TrimSpace(os.Getenv("ALLOW_PROD_DB_TESTS")) == "1" {
		t.Logf("⚠️ ALLOW_PROD_DB_TESTS=1：允许对非测试库运行集成测试；请确认用例已快照/恢复配置")
		return dsn
	}
	if isTestDSN(dsn) {
		return dsn
	}
	t.Skipf("拒绝对生产数据库运行集成测试：%s；请使用独立测试库，或设置 search_path=*test* 的测试 schema（必要时显式 ALLOW_PROD_DB_TESTS=1）",
		redactDSN(dsn))
	return ""
}

// isTestDSN 判断连接串是否明确指向测试库/测试 schema。
func isTestDSN(dsn string) bool {
	lower := strings.ToLower(dsn)
	// search_path / options 里出现 test schema（如 search_path=upgrade_mgr_test,public）。
	if strings.Contains(lower, "search_path=") && strings.Contains(lower, "test") {
		return true
	}
	host, dbname := parseDSN(dsn)
	if isLoopback(host) {
		return true
	}
	return strings.Contains(strings.ToLower(dbname), "test")
}

func isLoopback(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseDSN 提取 host 与 dbname，兼容 URL 与 key=value 两种格式。
func parseDSN(dsn string) (host, dbname string) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			return u.Hostname(), strings.TrimPrefix(u.Path, "/")
		}
	}
	for _, f := range strings.Fields(dsn) {
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			continue
		}
		v := strings.Trim(kv[1], "'\"")
		switch strings.ToLower(kv[0]) {
		case "host":
			host = v
		case "dbname", "database":
			dbname = v
		}
	}
	return host, dbname
}

// redactDSN 隐藏密码，避免测试跳过日志泄露凭据。
func redactDSN(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			if u.User != nil {
				u.User = url.UserPassword(u.User.Username(), "***")
			}
			return u.String()
		}
	}
	out := []string{}
	for _, f := range strings.Fields(dsn) {
		kv := strings.SplitN(f, "=", 2)
		if len(kv) == 2 && strings.EqualFold(kv[0], "password") {
			out = append(out, kv[0]+"=***")
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// WithSearchPath 返回强制使用指定 schema 的连接串（兼容 URL 与 key=value 格式）。
func WithSearchPath(dsn, schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return dsn
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	parts := strings.Fields(dsn)
	out := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(p), "search_path=") {
			continue
		}
		out = append(out, p)
	}
	out = append(out, "search_path="+schema)
	return strings.Join(out, " ")
}
