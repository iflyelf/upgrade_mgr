// Package corsx 提供严格的 Origin 白名单校验与匹配。
package corsx

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeOrigins 校验并规范化 Origin 列表。
//
// 只接受完整的 http/https origin，不接受 *、路径、查询、片段、用户信息，
// 避免凭证请求与宽松匹配造成跨站数据泄露。
func NormalizeOrigins(origins []string) ([]string, error) {
	out := make([]string, 0, len(origins))
	seen := map[string]bool{}
	for _, raw := range origins {
		s := strings.TrimRight(strings.TrimSpace(raw), "/")
		if s == "" {
			continue
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("非法 CORS Origin %q（必须是 http(s)://host[:port]）", raw)
		}
		normalized := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

// Match 判断请求 Origin 是否在白名单中。
func Match(origins []string, origin string) bool {
	normalized, err := NormalizeOrigins([]string{origin})
	if err != nil || len(normalized) != 1 {
		return false
	}
	target := normalized[0]
	for _, allowed := range origins {
		if strings.TrimRight(strings.ToLower(strings.TrimSpace(allowed)), "/") == target {
			return true
		}
	}
	return false
}
