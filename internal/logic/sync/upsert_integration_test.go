package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/logic/hostfieldmap"
)

// TestUpsertWithMappings 验证「映射驱动的 upsert」真实落库行为。
//
// 这是替换硬编码映射后最关键的一段：SQL 按映射动态拼列名，
// 必须确认内置列与自定义字段都能写对，且 overwrite 语义符合预期。
// 需要 DATABASE_URL；未设置则跳过（不需要 Consul）。
func TestUpsertWithMappings(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 upsert 集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	const ip = "203.0.113.77" // TEST-NET-3，不会与真实主机冲突

	// 自定义字段定义：映射到 custom 必须先有定义。
	var defID int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO host_field_defs (field_key, label, field_type, show_in_list, show_in_form, editable)
		VALUES ('rack','机柜','text',true,true,true)
		ON CONFLICT (field_key) DO UPDATE SET label=EXCLUDED.label
		RETURNING id`).Scan(&defID)
	if err != nil {
		t.Fatalf("准备字段定义失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM host_field_defs WHERE id=$1`, defID)
		db.Exec(`DELETE FROM hosts WHERE ip=$1`, ip)
	})
	db.ExecContext(ctx, `DELETE FROM hosts WHERE ip=$1`, ip)

	syncer := NewSyncer(db)
	now := time.Now()

	// 两条映射：一条写内置列，一条写自定义字段。
	set := hostfieldmap.NewSet([]hostfieldmap.Mapping{
		{Source: "consul", SourceKey: "app_code", TargetKind: "column",
			TargetKey: "app_code", Enabled: true, SortOrder: 10},
		{Source: "consul", SourceKey: "rack_no", TargetKind: "custom",
			TargetKey: "rack", Enabled: true, SortOrder: 20},
	})

	row := &hostRow{
		IP: ip, Node: "node-a", Datacenter: "dc1",
		Meta: map[string]string{"app_code": "pay", "rack_no": "R12", "unmapped": "ignored"},
	}
	if err := syncer.upsert(ctx, row, set, hostfieldmap.SourceConsul, now); err != nil {
		t.Fatalf("首次 upsert 失败: %v", err)
	}

	var appCode, node, dc, source, custom string
	err = db.QueryRowContext(ctx, `
		SELECT app_code, consul_node, datacenter, source, custom_fields::text
		FROM hosts WHERE ip=$1`, ip).Scan(&appCode, &node, &dc, &source, &custom)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if appCode != "pay" {
		t.Errorf("app_code=%q, want pay", appCode)
	}
	if node != "node-a" || dc != "dc1" {
		t.Errorf("node=%q dc=%q，Consul 结构属性应落库", node, dc)
	}
	if source != "consul" {
		t.Errorf("source=%q, want consul", source)
	}
	var cf map[string]string
	if err := json.Unmarshal([]byte(custom), &cf); err != nil {
		t.Fatalf("custom_fields 不是合法 JSON: %v", err)
	}
	if cf["rack"] != "R12" {
		t.Errorf("custom_fields[rack]=%q, want R12", cf["rack"])
	}
	// 未映射的键绝不能进库，否则外部 Meta 会污染字段空间。
	if _, ok := cf["unmapped"]; ok {
		t.Error("未映射的键不应写入 custom_fields")
	}

	// 人工修改后再同步：overwrite=false 必须保留人工值。
	if _, err := db.ExecContext(ctx, `
		UPDATE hosts SET app_code='manual',
		       custom_fields = custom_fields || '{"rack":"MANUAL"}'::jsonb
		WHERE ip=$1`, ip); err != nil {
		t.Fatalf("模拟人工修改失败: %v", err)
	}
	if err := syncer.upsert(ctx, row, set, hostfieldmap.SourceConsul, time.Now()); err != nil {
		t.Fatalf("二次 upsert 失败: %v", err)
	}
	err = db.QueryRowContext(ctx,
		`SELECT app_code, custom_fields::text FROM hosts WHERE ip=$1`, ip).Scan(&appCode, &custom)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	json.Unmarshal([]byte(custom), &cf)
	if appCode != "manual" {
		t.Errorf("overwrite=false 应保留人工值，得到 app_code=%q", appCode)
	}
	if cf["rack"] != "MANUAL" {
		t.Errorf("overwrite=false 应保留人工值，得到 rack=%q", cf["rack"])
	}

	// overwrite=true：以外部来源为准。
	forceSet := hostfieldmap.NewSet([]hostfieldmap.Mapping{
		{Source: "consul", SourceKey: "app_code", TargetKind: "column",
			TargetKey: "app_code", Enabled: true, Overwrite: true, SortOrder: 10},
		{Source: "consul", SourceKey: "rack_no", TargetKind: "custom",
			TargetKey: "rack", Enabled: true, Overwrite: true, SortOrder: 20},
	})
	if err := syncer.upsert(ctx, row, forceSet, hostfieldmap.SourceConsul, time.Now()); err != nil {
		t.Fatalf("overwrite upsert 失败: %v", err)
	}
	err = db.QueryRowContext(ctx,
		`SELECT app_code, custom_fields::text FROM hosts WHERE ip=$1`, ip).Scan(&appCode, &custom)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	json.Unmarshal([]byte(custom), &cf)
	if appCode != "pay" {
		t.Errorf("overwrite=true 应覆盖，得到 app_code=%q", appCode)
	}
	if cf["rack"] != "R12" {
		t.Errorf("overwrite=true 应覆盖，得到 rack=%q", cf["rack"])
	}

	// 空映射集：只更新同步时间，不动业务字段。
	if err := syncer.upsert(ctx, row, hostfieldmap.NewSet(nil),
		hostfieldmap.SourceConsul, time.Now()); err != nil {
		t.Fatalf("空映射 upsert 失败: %v", err)
	}
}
