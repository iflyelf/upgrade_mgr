package task

import (
	"sort"
	"strings"

	"github.com/lib/pq"
)

func join(parts []string, sep string) string { return strings.Join(parts, sep) }

// sortHosts 按展示地址排序。
//
// 多网卡主机优先用出口 IP 排序：运维通常按业务网段组织主机，
// 出口地址比服务端观察到的来源地址更符合直觉。
func sortHosts(h []hostRow) {
	key := func(r hostRow) string {
		if r.exitIP != "" {
			return r.exitIP
		}
		return r.ip
	}
	sort.Slice(h, func(i, j int) bool { return key(h[i]) < key(h[j]) })
}

// arrayWrapper 返回 pq.Array 以便作为 SQL 参数传入 ANY($n)。
func arrayWrapper(vals []string) any { return pq.Array(vals) }
