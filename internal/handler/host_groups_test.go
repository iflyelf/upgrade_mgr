package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// TestHostGroupOptions 返回去重后的主机业务分组，供授权下拉选择。
func TestHostGroupOptions(t *testing.T) {
	db, sc := setupHostDB(t)
	sfx := "hgo-" + t.Name()
	for i, g := range []string{"appA", "appB", "appA", ""} {
		if _, err := db.Exec(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, app_code, biz_group)
			VALUES ($1,$2,1,'manual',$3,$4)`,
			"10.9."+string(rune('0'+i))+".1", sfx, g, "g"+g); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE hostname=$1`, sfx) })

	req := httptest.NewRequest("GET", "/api/host-groups", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostGroupOptions(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Groups []string `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, g := range out.Data.Groups {
		if seen[g] {
			t.Fatalf("分组重复: %q", g)
		}
		seen[g] = true
	}
	if !seen["appA"] || !seen["appB"] || !seen["gappA"] {
		t.Fatalf("分组缺失: %v", out.Data.Groups)
	}
}
