package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestHostUpdateCustomFields 主机自定义字段：按定义校验后按键合并。
func TestHostUpdateCustomFields(t *testing.T) {
	db, sc := setupHostDB(t)

	// 建一个自定义字段定义
	key := "rack_" + strings.ReplaceAll(t.Name(), "/", "_")
	var defID int64
	if err := db.QueryRow(`
		INSERT INTO host_field_defs (field_key, label, field_type, options, show_in_list, show_in_form, editable, sort_order)
		VALUES ($1,'机柜','text','',true,true,true,10) RETURNING id`, key).Scan(&defID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM host_field_defs WHERE id=$1`, defID)
		db.Exec(`UPDATE hosts SET custom_fields = custom_fields - $1`, key)
	})

	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	// 写入
	body := `{"custom_fields":{"` + key + `":"A-01"}}`
	rec := callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr, body, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存自定义字段失败 %d: %s", rec.Code, rec.Body.String())
	}
	var raw []byte
	if err := db.QueryRow(`SELECT custom_fields FROM hosts WHERE id=$1`, hostID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m[key] != "A-01" {
		t.Fatalf("自定义字段未写入: %v", m)
	}

	// 未定义字段被丢弃（不报错，也不写入）
	body = `{"custom_fields":{"unknown_field":"x"}}`
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr, body, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("未知字段应被静默丢弃，实际 %d: %s", rec.Code, rec.Body.String())
	}
	db.QueryRow(`SELECT custom_fields FROM hosts WHERE id=$1`, hostID).Scan(&raw)
	m = nil
	json.Unmarshal(raw, &m)
	if _, ok := m["unknown_field"]; ok {
		t.Fatalf("未知字段不应写入: %v", m)
	}
	if m[key] != "A-01" {
		t.Fatalf("已知字段应保留: %v", m)
	}
}

// TestHostUpdateCustomFieldsNumberValidation number 类型非法值应 400。
func TestHostUpdateCustomFieldsNumberValidation(t *testing.T) {
	db, sc := setupHostDB(t)
	key := "cpu_" + strings.ReplaceAll(t.Name(), "/", "_")
	var defID int64
	if err := db.QueryRow(`
		INSERT INTO host_field_defs (field_key, label, field_type, show_in_list, show_in_form, editable)
		VALUES ($1,'核数','number',true,true,true) RETURNING id`, key).Scan(&defID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM host_field_defs WHERE id=$1`, defID) })

	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	rec := callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"custom_fields":{"`+key+`":"abc"}}`, hostTestUser(1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非数字应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"custom_fields":{"`+key+`":"16"}}`, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("数字应通过，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 确保测试用到 time 之外没有未使用导入（保留空引用）
var _ = httptest.NewRequest
