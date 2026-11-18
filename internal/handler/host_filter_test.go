package handler

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// hostFilterParams 从 hosts.go 的 hostListFilter 源码中提取全部 q.Get("...") 参数名。
//
// 用源码而不是手写清单：新增筛选参数时若忘了在 TestHostFilterListMatchesDeletable
// 里补用例，测试会直接失败，而不是靠人记住。
func hostFilterParams(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "hosts.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 hosts.go 失败: %v", err)
	}
	seen := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "hostListFilter" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// filterValues(r, "key")：多选筛选参数（两个参数，先于单参数检查）。
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "filterValues" && len(call.Args) == 2 {
				if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						seen[v] = true
					}
				}
				return true
			}
			if len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// 兼容两种写法：q.Get("key") 与 filterValues(r, "key")（多选）。
			if sel.Sel.Name == "Get" {
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "q" {
					return true
				}
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						seen[v] = true
					}
				}
				return true
			}
			return true
		})
	}
	if len(seen) == 0 {
		t.Fatal("未在 hostListFilter 中找到任何筛选参数，源码结构可能已变化，请同步更新本测试")
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestHostFilterQSKnownToBackend 前端 hostFilterQS() 发送的每个参数，后端 hostListFilter 都必须支持。
//
// 否则后端会静默忽略该参数：列表看似已筛选（前端可能本地过滤），
// 「按筛选条件全选」却选中了全部主机，造成误删。不需要数据库。
func TestHostFilterQSKnownToBackend(t *testing.T) {
	src, err := os.ReadFile("../../web/dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	body := regexp.MustCompile(`(?s)function hostFilterQS\(\) \{(.*?)\n  \}`).FindSubmatch(src)
	if body == nil {
		t.Fatal("app.js 中找不到 hostFilterQS()，请同步更新本测试")
	}
	backend := map[string]bool{}
	for _, p := range hostFilterParams(t) {
		backend[p] = true
	}
	// 匹配 'name=' + ...、'name=固定值' 以及多选 addAll('name') 三种写法。
	keys := regexp.MustCompile(`(?:'([a-z_]+)=|addAll\('([a-z_]+)')`).FindAllSubmatch(body[1], -1)
	if len(keys) == 0 {
		t.Fatal("hostFilterQS() 中没有识别到任何参数，请同步更新本测试")
	}
	for _, k := range keys {
		key := string(k[1])
		if key == "" {
			key = string(k[2])
		}
		if !backend[key] {
			t.Errorf("前端 hostFilterQS() 发送了 %q，但后端 hostListFilter 不支持", key)
		}
	}
}

func sortedIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestHostFilterListMatchesDeletable 每个筛选参数下，「按筛选条件全选」返回的主机
// 必须等于主机列表结果减去有未完成任务的主机。
//
// 新增筛选参数时：后端改 hostListFilter，前端改 app.js 的 hostFilterQS()，
// 再在下面 cases 里加一行；漏加会被 hostFilterParams 的覆盖检查拦下。
func TestHostFilterListMatchesDeletable(t *testing.T) {
	db, sc := setupHostDB(t)
	tag := fmt.Sprintf("hflt-%d", time.Now().UnixNano())

	// online：有 Agent 且心跳新鲜；idle：无 Agent（显示「未知」）；
	// stale：库里仍是 online 但心跳超时（显示「离线」）；busy：有运行中任务。
	online, _ := insertTestHost(t, db, 1)
	idle, _ := insertTestHost(t, db, 1)
	stale, _ := insertTestHost(t, db, 1)
	busy := insertBusyHost(t, db, tag)
	noUpdate, _ := insertTestHost(t, db, 1) // 有 Agent 但从未自升级
	other, _ := insertTestHost(t, db, 1)    // 不匹配关键字，任何用例都不应出现
	biz1, biz2 := tag+"-B1", tag+"-B2"
	seed := []struct {
		id                              int64
		app, biz, osFamily, acct, state string
		agent                           bool
		seenAgo                         string // last_seen 距今时长；空表示无 Agent（NULL）
	}{
		{online, "APP-A", biz1, "centos", "acc1", "online", true, "0 seconds"},
		{idle, "APP-B", biz2, "ubuntu", "acc2", "unknown", false, ""},
		{stale, "APP-B", biz2, "centos", "acc2", "online", true, "1 hour"},
		{busy, "APP-A", biz1, "ubuntu", "acc1", "online", true, "0 seconds"},
		{noUpdate, "APP-A", biz1, "ubuntu", "acc1", "online", true, "0 seconds"},
	}
	for _, h := range seed {
		agentID := ""
		if h.agent {
			agentID = fmt.Sprintf("%s-agent-%d", tag, h.id)
		}
		if _, err := db.Exec(`UPDATE hosts SET hostname=$2, app_code=$3, biz_group=$4, os_family=$5, account=$6,
			agent_status=$7, agent_id=$8::varchar,
			last_seen=CASE WHEN $9::text='' THEN NULL ELSE NOW() - $9::text::interval END WHERE id=$1`,
			h.id, tag, h.app, h.biz, h.osFamily, h.acct, h.state, agentID, h.seenAgo); err != nil {
			t.Fatalf("初始化主机 %d 失败: %v", h.id, err)
		}
	}

	// 自升级状态：online/busy 成功，stale 失败，idle 无 Agent（未升级）。
	insertSelfUpdate := func(hostID int64, status string) {
		t.Helper()
		var agentID string
		if err := db.QueryRow(`SELECT COALESCE(agent_id,'') FROM hosts WHERE id=$1`, hostID).Scan(&agentID); err != nil {
			t.Fatal(err)
		}
		if agentID == "" {
			t.Fatalf("主机 %d 没有 agent_id", hostID)
		}
		if _, err := db.Exec(`INSERT INTO commands (host_id, agent_id, type, status)
			VALUES ($1,$2,'self_update',$3)`, hostID, agentID, status); err != nil {
			t.Fatalf("插入自升级指令失败: %v", err)
		}
	}
	insertSelfUpdate(online, "success")
	insertSelfUpdate(busy, "success")
	insertSelfUpdate(stale, "failed")

	cases := []struct {
		param string
		value string
		multi []string // 非空时按多选参数发送（重复 key）
		want  []int64  // 列表应命中的主机（含 busy）
	}{
		{"q", tag, nil, []int64{online, idle, stale, busy, noUpdate}},
		{"app_code", "APP-A", nil, []int64{online, busy, noUpdate}},
		{"biz_group", biz1, nil, []int64{online, busy, noUpdate}},
		{"os_family", "ubuntu", nil, []int64{idle, busy, noUpdate}},
		{"account", "acc2", nil, []int64{idle, stale}},
		{"agent_status", "online", nil, []int64{online, busy, noUpdate}},
		{"agent_status", "offline", nil, []int64{stale}},
		{"agent_status", "unknown", nil, []int64{idle}},
		{"upgrade_status", "success", nil, []int64{online, busy}},
		{"upgrade_status", "failed", nil, []int64{stale}},
		{"upgrade_status", "none", nil, []int64{noUpdate}},
		// 多选：重复参数应取并集。
		{"app_code", "", []string{"APP-A", "APP-B"}, []int64{online, idle, stale, busy, noUpdate}},
		{"agent_status", "", []string{"offline", "unknown"}, []int64{idle, stale}},
		{"upgrade_status", "", []string{"failed", "none"}, []int64{stale, noUpdate}},
	}

	// 覆盖检查：hostListFilter 支持的每个参数都至少有一个用例。
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.param] = true
	}
	for _, p := range hostFilterParams(t) {
		if !covered[p] {
			t.Errorf("hostListFilter 新增了筛选参数 %q，请在 cases 中补用例，并同步前端 hostFilterQS()", p)
		}
	}

	u := hostTestUser(1)
	call := func(h http.HandlerFunc, path string, v url.Values) []byte {
		req := httptest.NewRequest(http.MethodGet, path+"?"+v.Encode(), nil)
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s?%s 应 200，实际 %d: %s", path, v.Encode(), rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	for _, c := range cases {
		name := c.param + "=" + c.value
		if len(c.multi) > 0 {
			name = c.param + "=" + strings.Join(c.multi, ",")
		}
		t.Run(name, func(t *testing.T) {
			v := url.Values{}
			v.Set("q", tag) // 关键字隔离本测试数据；q 用例本身就是只带 q
			if len(c.multi) > 0 {
				for _, x := range c.multi {
					v.Add(c.param, x)
				}
			} else if c.param != "q" {
				v.Set(c.param, c.value)
			}

			var list struct {
				Data struct {
					List []Host `json:"list"`
				} `json:"data"`
			}
			lv := url.Values{}
			for k, vs := range v {
				lv[k] = vs
			}
			lv.Set("size", "500")
			_ = json.Unmarshal(call(ListHosts(sc), "/api/hosts", lv), &list)
			listIDs := []int64{}
			for _, h := range list.Data.List {
				if h.ID == other {
					t.Fatalf("不匹配关键字的主机 %d 出现在列表中", other)
				}
				// 按状态筛选时，列表显示的状态必须属于筛选值（筛选与显示同一规则）。
				if c.param == "agent_status" {
					allowed := c.multi
					if len(allowed) == 0 {
						allowed = []string{c.value}
					}
					found := false
					for _, v := range allowed {
						if h.AgentStatus == v {
							found = true
						}
					}
					if !found {
						t.Fatalf("主机 %d 显示状态 %q，不在筛选值 %v 中", h.ID, h.AgentStatus, allowed)
					}
				}
				listIDs = append(listIDs, h.ID)
			}
			if fmt.Sprint(sortedIDs(listIDs)) != fmt.Sprint(sortedIDs(c.want)) {
				t.Fatalf("列表结果不符: got %v want %v", sortedIDs(listIDs), sortedIDs(c.want))
			}

			var del struct {
				Data struct {
					IDs []int64 `json:"ids"`
				} `json:"data"`
			}
			_ = json.Unmarshal(call(HostDeletableIDs(sc), "/api/hosts/deletable-ids", v), &del)
			// 可删除 = 列表结果 - 有未完成任务的主机 - 有效在线/维护中的主机。
			wantDel := []int64{}
			for _, id := range listIDs {
				if id == busy || id == online || id == noUpdate {
					continue
				}
				wantDel = append(wantDel, id)
			}
			if fmt.Sprint(sortedIDs(del.Data.IDs)) != fmt.Sprint(sortedIDs(wantDel)) {
				t.Fatalf("按筛选全选与列表不一致: deletable %v, 列表减去忙碌/在线主机 %v",
					sortedIDs(del.Data.IDs), sortedIDs(wantDel))
			}
		})
	}
}
