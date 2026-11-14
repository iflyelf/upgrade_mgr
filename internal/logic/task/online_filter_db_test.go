package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestPreviewSkipsOfflineHosts 预览只包含在线主机，离线主机自动跳过且不出现在示例 IP 中。
func TestPreviewSkipsOfflineHosts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	base := fmt.Sprintf("10.%d.%d", (sfx/65536)%200+20, (sfx/256)%200)
	onlineIP := base + ".11"
	staleIP := base + ".12"
	noAgentIP := base + ".13"

	var ids []int64
	insert := func(ip, hostname, agentID, status string, lastSeen any) {
		t.Helper()
		var id int64
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, arch, os_family, agent_id, agent_status, last_seen, tenant_id)
			VALUES ($1,$2,'amd64','linux',$3,$4,$5,1) RETURNING id`,
			ip, hostname, agentID, status, lastSeen).Scan(&id); err != nil {
			t.Fatalf("建主机 %s 失败: %v", ip, err)
		}
		ids = append(ids, id)
	}
	insert(onlineIP, fmt.Sprintf("online-%d", sfx), "online-agent", "online", time.Now())
	insert(staleIP, fmt.Sprintf("stale-%d", sfx), "stale-agent", "offline", time.Now().Add(-time.Hour))
	insert(noAgentIP, fmt.Sprintf("no-agent-%d", sfx), "", "unknown", nil)
	t.Cleanup(func() {
		for _, id := range ids {
			db.Exec(`DELETE FROM hosts WHERE id=$1`, id)
		}
	})

	svc := NewService(db, nil, nil, func() bool { return true })
	req := CreateRequest{
		Kind:    KindCommand,
		Name:    fmt.Sprintf("online-filter-%d", sfx),
		Command: protocol.CommandSpec{Script: "echo hi", Timeout: "1m"},
		Selector: Selector{
			IPExpr: fmt.Sprintf("%s,%s,%s", onlineIP, staleIP, noAgentIP),
		},
	}

	res, err := svc.Preview(ctx, scope.New(1, true), req)
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	if res.TotalHosts != 1 {
		t.Fatalf("预览应只命中 1 台在线主机，实际 %d", res.TotalHosts)
	}
	if res.SkippedOffline != 2 {
		t.Fatalf("应跳过 2 台离线主机，实际 %d", res.SkippedOffline)
	}
	if len(res.SampleIPs) != 1 || res.SampleIPs[0] != onlineIP {
		t.Fatalf("示例 IP 应只包含在线主机 %s，实际 %v", onlineIP, res.SampleIPs)
	}

	// 只选择离线主机时，创建任务应直接拒绝并说明原因。
	req.Selector.IPExpr = fmt.Sprintf("%s,%s", staleIP, noAgentIP)
	if _, err := svc.Create(ctx, scope.New(1, true), req); err == nil ||
		!strings.Contains(err.Error(), "不在线") {
		t.Fatalf("全部离线时应拒绝创建并提示不在线，实际 err=%v", err)
	}
}
