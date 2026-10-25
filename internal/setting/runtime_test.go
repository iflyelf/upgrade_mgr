package setting

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

// TestApplyRuntimeOnlyMemory 运行时同步只改内存，不写库，也不接受只读/未知键。
func TestApplyRuntimeOnlyMemory(t *testing.T) {
	cfg := config.Default()
	svc := NewService(nil, cfg) // db=nil：若实现尝试写库会 panic

	kv := map[string]string{
		"artifact.max_concurrent_uploads": "7",
		"artifact.lock_wait":              "3m",
		"database.max_open_conns":         "999", // 只读，必须忽略
		"not.registered":                  "x",   // 未注册，必须忽略
	}
	applied := svc.ApplyRuntime(kv)
	if len(applied) != 2 {
		t.Fatalf("applied = %v, want 2 项（只读/未知键应忽略）", applied)
	}
	if cfg.Artifact.MaxConcurrentUploads != 7 {
		t.Fatalf("MaxConcurrentUploads = %d, want 7", cfg.Artifact.MaxConcurrentUploads)
	}
	if cfg.Artifact.LockWait != "3m" {
		t.Fatalf("LockWait = %q, want 3m", cfg.Artifact.LockWait)
	}
	if cfg.Database.MaxOpenConns == 999 {
		t.Fatal("只读项 database.max_open_conns 不应被运行时同步修改")
	}

	// 值未变化时不应重复返回，避免多副本轮询反复触发热更新。
	if again := svc.ApplyRuntime(kv); len(again) != 0 {
		t.Fatalf("值未变化时 applied 应为空，实际 %v", again)
	}
}
