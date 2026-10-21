package auth

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestChangePassword 测试密码修改流程。
func TestChangePassword(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	
	ctx := context.Background()
	b := NewBuiltin(db, "test-secret-32-bytes-long-1234", false)
	
	// 创建测试用户
	username := "test_change_pw_" + time.Now().Format("20060102150405")
	_, err := db.ExecContext(ctx,
		`INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
		 VALUES ($1, $2, $3, FALSE, 1, ARRAY['user'])`,
		username, "$2a$10$abcdefghijklmnopqrstuv", username)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	var userID string
	err = db.QueryRowContext(ctx, `SELECT id FROM local_users WHERE username=$1`, username).Scan(&userID)
	if err != nil {
		t.Fatalf("查询用户 ID 失败: %v", err)
	}
	defer db.ExecContext(ctx, `DELETE FROM local_users WHERE id=$1`, userID)
	
	// 先设置一个已知密码
	_, _, err = b.Login(ctx, username, "initial-password")
	if err == nil {
		t.Fatal("登录应该失败（密码不匹配哈希）")
	}
	
	// 更新为已知密码
	_, err = db.ExecContext(ctx,
		`UPDATE local_users SET password_hash=$2, updated_at=NOW() WHERE id=$1`,
		userID, mustHash("oldpass123"))
	if err != nil {
		t.Fatalf("更新密码失败: %v", err)
	}
	
	// 登录成功
	u, token, err := b.Login(ctx, username, "oldpass123")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if u.Username != username {
		t.Fatalf("用户名不匹配: got %s, want %s", u.Username, username)
	}
	t.Logf("✅ 登录成功: %s (ID=%s, token=%s...)", username, userID, token[:20])
	
	// 修改密码
	newToken, err := b.ChangePassword(ctx, userID, "oldpass123", "newpass456")
	if err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}
	if newToken == "" {
		t.Fatal("新 token 为空")
	}
	t.Logf("✅ 密码修改成功，新 token: %s", newToken[:20]+"...")
	
	// 旧密码应无法登录
	_, _, err = b.Login(ctx, username, "oldpass123")
	if err == nil {
		t.Fatal("旧密码仍能登录")
	}
	t.Logf("✅ 旧密码已失效")
	
	// 新密码应能登录
	u2, _, err := b.Login(ctx, username, "newpass456")
	if err != nil {
		t.Fatalf("新密码登录失败: %v", err)
	}
	if u2.Username != username {
		t.Fatalf("新密码登录用户名不匹配")
	}
	t.Logf("✅ 新密码登录成功")
	
	// 测试错误的旧密码
	_, err = b.ChangePassword(ctx, userID, "wrongold", "another123")
	if err != ErrWrongPassword {
		t.Fatalf("应返回 ErrWrongPassword，实际: %v", err)
	}
	t.Logf("✅ 错误旧密码被正确拒绝")
	
	// 测试密码重用
	_, err = b.ChangePassword(ctx, userID, "newpass456", "newpass456")
	if err == nil || err == ErrWrongPassword {
		t.Fatalf("应拒绝密码重用，实际: %v", err)
	}
	t.Logf("✅ 密码重用被拒绝")
}

// TestValidatePassword 测试密码验证。
func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		old     string
		new     string
		wantErr bool
	}{
		{"正常", "old123456", "new123456", false},
		{"太短", "old123", "short", true},
		{"太长", "old123", string(make([]byte, 73)), true},
		{"重复", "same123456", "same123456", true},
		{"空旧密码允许", "", "new123456", false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.old, tt.new)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePassword() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func mustHash(pw string) string {
	h, _ := hashOrPassthrough(pw)
	return h
}
