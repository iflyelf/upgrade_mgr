package sync

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
)

// TestSyncFromConsulReal 对接真实 Consul 与 DB，验证主机与业务分组同步。
// 需要 DATABASE_URL + CONSUL_ADDRESS(+CONSUL_TOKEN)；未设置则跳过。
func TestSyncFromConsulReal(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	addr := os.Getenv("CONSUL_ADDRESS")
	if dsn == "" || addr == "" {
		t.Skip("未设置 DATABASE_URL / CONSUL_ADDRESS，跳过 Consul 同步集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cli, err := consul.New(consul.Config{
		Address:    addr,
		Token:      os.Getenv("CONSUL_TOKEN"),
		Datacenter: os.Getenv("CONSUL_DATACENTER"),
		Timeout:    15 * time.Second,
	})
	if err != nil {
		t.Fatalf("consul client: %v", err)
	}
	if err := cli.TestConnection(); err != nil {
		t.Fatalf("consul connect: %v", err)
	}

	res, err := NewSyncer(db).SyncFromConsul(context.Background(), cli, true)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Logf("同步结果: 服务=%d 实例=%d 主机=%d 耗时=%s",
		res.Services, res.Instances, res.Upserted, res.Duration)
	if res.Upserted == 0 {
		t.Fatal("期望至少同步到 1 台主机")
	}

	// 校验业务分组字段落库
	var ip, appCode, bizGroup string
	err = db.QueryRow(`SELECT ip, app_code, biz_group FROM hosts
		WHERE source='consul' AND app_code <> '' LIMIT 1`).Scan(&ip, &appCode, &bizGroup)
	if err != nil {
		t.Fatalf("查询同步主机失败: %v", err)
	}
	t.Logf("样例主机: ip=%s app_code=%s biz_group=%s", ip, appCode, bizGroup)
}
