package auth

import "testing"

func TestSessionCodecRoundTrip(t *testing.T) {
	c := newSessionCodec("test-secret-at-least-16-bytes-long", false)
	u := &User{ID: "7", Username: "alice", DisplayName: "Alice", IsAdmin: true, TenantID: 42, Roles: []string{"admin", "user"}}
	tok := c.sign(u)
	got, err := c.verify(tok)
	if err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	if got.Username != "alice" || got.TenantID != 42 || !got.IsAdmin || len(got.Roles) != 2 {
		t.Fatalf("字段丢失: %+v", got)
	}
	t.Logf("✅ 会话往返保留租户/角色: tenant=%d roles=%v", got.TenantID, got.Roles)

	// 篡改载荷必须被拒
	if _, err := c.verify("eyJ1IjoiZXZpbCJ9." + "deadbeef"); err == nil {
		t.Fatal("篡改的 token 必须被拒绝")
	}
	t.Log("✅ 篡改 token 被拒绝")

	// 用户名含分隔符不应导致字段错位（JSON 载荷的价值）
	u2 := &User{ID: "8", Username: "a|b.c", DisplayName: "x|y", TenantID: 5, Roles: []string{"user"}}
	got2, err := c.verify(c.sign(u2))
	if err != nil || got2.Username != "a|b.c" {
		t.Fatalf("含分隔符用户名解析错误: %+v err=%v", got2, err)
	}
	t.Log("✅ 含分隔符用户名无字段错位")
}
