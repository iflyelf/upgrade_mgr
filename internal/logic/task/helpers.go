package task

import (
	"sort"
	"strings"

	"github.com/lib/pq"
)

func join(parts []string, sep string) string { return strings.Join(parts, sep) }

func sortHosts(h []hostRow) {
	sort.Slice(h, func(i, j int) bool { return h[i].ip < h[j].ip })
}

// arrayWrapper 返回 pq.Array 以便作为 SQL 参数传入 ANY($n)。
func arrayWrapper(vals []string) any { return pq.Array(vals) }
