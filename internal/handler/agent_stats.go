package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/agentupdate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// agentStatTarget 某个 OS/架构/变体对应的目标 Agent 二进制。
//
// ok=false 表示目标二进制缺失或架构非法，此时退回按版本字符串判断。
type agentStatTarget struct {
	hash string
	ok   bool
}

// AgentStats 返回 Agent 版本/OS/架构分布、离线数与过期数。
//
// 用于主机页「Agent 版本分布」卡片：升级 Agent 时快速确认剩余旧版本数量。
//
// 版本优先取 build_version（Agent 自身编译版本），旧 Agent 未上报时回退
// agent_version。是否最新以目标二进制 SHA256 为准：这与自升级的判定一致，
// 避免版本字符串相同但二进制不同的情况被误标为最新。
func AgentStats(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.Settings.Snapshot()
		offlineAfter := snap.Agent.OfflineAfter
		// 解析失败时回退默认 1m，避免配置错误导致统计失效。
		d, err := time.ParseDuration(strings.TrimSpace(offlineAfter))
		if err != nil || d < 1*time.Second {
			d = 1 * time.Minute
		}
		offlineInterval := fmt.Sprintf("'%.0f seconds'", d.Seconds())

		tCond, tArgs := reqScope(r).Where("h", 1)
		// 兼容历史数据：早期注册未回填 agents.host_id，此时按 agent_id 关联主机；
		// 新数据优先使用 agents.host_id（避免同 agent_id 多主机时重复计数）。
		//
		// variant 使用与自升级相同的推导：旧 Agent 未上报 build_variant 时，
		// 2.x 内核按 legacy 处理，否则按普通构建。
		//
		// offline 判定：agent.status='offline' 或心跳超时（last_heartbeat_at 为空
		// 或超过配置的 offline_after 阈值）。
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT COALESCE(NULLIF(a.build_version,''), NULLIF(a.agent_version,''), '') AS version,
			       COALESCE(a.os,''), COALESCE(a.arch,''),
			       CASE WHEN COALESCE(a.build_variant,'')=''
			                 AND COALESCE(h.kernel,'') LIKE '2.%'
			            THEN 'legacy' ELSE COALESCE(a.build_variant,'') END AS variant,
			       COALESCE(a.build_hash,''),
			       COUNT(*) AS total,
			       COUNT(*) FILTER (
			           WHERE a.status='offline'
			              OR a.last_heartbeat_at IS NULL
			              OR a.last_heartbeat_at < NOW() - INTERVAL `+offlineInterval+`
			       ) AS offline
			  FROM agents a
			  JOIN hosts h ON h.id = a.host_id OR (a.host_id IS NULL AND h.agent_id = a.agent_id)
			 WHERE `+tCond+`
			 GROUP BY 1,2,3,4,5
			 ORDER BY total DESC`, tArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		resolver := agentUpdateResolver(s)
		targetVersion := resolver.Version
		targets := map[string]agentStatTarget{}
		resolveTarget := func(osName, arch, variant string) agentStatTarget {
			key := osName + "\x00" + arch + "\x00" + variant
			if t, ok := targets[key]; ok {
				return t
			}
			var info agentStatTarget
			target, err := resolver.ResolveOS(agentupdate.ChooseOS(osName), arch, variant, "")
			if err == nil {
				info = agentStatTarget{hash: target.SHA256, ok: true}
			}
			targets[key] = info
			return info
		}

		// 同一版本/OS/架构下可能同时存在旧哈希和最新哈希（例如同一 commit
		// 重复构建），这里按这三列合并计数，避免页面出现重复版本行。
		type statGroup struct {
			version, osName, arch    string
			count, offline, oldCount int
		}
		groups := make(map[string]*statGroup, 16)
		order := make([]string, 0, 16)
		total, offline, outdated := 0, 0, 0
		for rows.Next() {
			var version, osName, arch, variant, hash string
			var count, off int
			if err := rows.Scan(&version, &osName, &arch, &variant, &hash, &count, &off); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			var isOld bool
			if target := resolveTarget(osName, arch, variant); target.ok {
				// 空哈希无法证明是最新，按旧版本处理；自升级同样会下发。
				isOld = !strings.EqualFold(hash, target.hash)
			} else {
				// 目标二进制缺失（或架构非法）：退回版本字符串比较。
				isOld = hash == "" || version == "" || version != targetVersion
			}

			key := version + "\x00" + osName + "\x00" + arch
			g := groups[key]
			if g == nil {
				g = &statGroup{version: version, osName: osName, arch: arch}
				groups[key] = g
				order = append(order, key)
			}
			g.count += count
			g.offline += off
			if isOld {
				g.oldCount += count
				outdated += count
			}
			total += count
			offline += off
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		list := make([]map[string]any, 0, len(order))
		for _, key := range order {
			g := groups[key]
			list = append(list, map[string]any{
				"version": g.version, "os": g.osName, "arch": g.arch,
				"count": g.count, "offline": g.offline,
				"outdated": g.oldCount > 0, "outdated_count": g.oldCount,
			})
		}
		sort.SliceStable(list, func(i, j int) bool {
			ci, cj := list[i]["count"].(int), list[j]["count"].(int)
			if ci != cj {
				return ci > cj
			}
			vi, vj := list[i]["version"].(string), list[j]["version"].(string)
			if vi != vj {
				return vi < vj
			}
			oi, oj := list[i]["os"].(string), list[j]["os"].(string)
			if oi != oj {
				return oi < oj
			}
			return list[i]["arch"].(string) < list[j]["arch"].(string)
		})

		httpx.OK(w, map[string]any{
			"target_version": targetVersion, "total": total, "offline": offline,
			"outdated": outdated, "list": list,
			"offline_after": offlineAfter,
		})
	}
}
