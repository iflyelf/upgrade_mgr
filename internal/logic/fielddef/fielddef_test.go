package fielddef

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestValidate 字段定义校验：键、类型、选项。
func TestValidate(t *testing.T) {
	ok := []Def{
		{FieldKey: "empCode", Label: "工号", FieldType: TypeText},
		{FieldKey: "note_1", Label: "备注", FieldType: TypeTextarea},
		{FieldKey: "age", Label: "年龄", FieldType: TypeNumber},
		{FieldKey: "type", Label: "类型", FieldType: TypeSelect, Options: `["A","B"]`},
		{FieldKey: "hired", Label: "入职", FieldType: TypeDate},
	}
	for _, d := range ok {
		if err := validate(&d); err != nil {
			t.Errorf("应通过 %+v: %v", d, err)
		}
	}

	bad := []Def{
		{FieldKey: "1abc", Label: "x", FieldType: TypeText},                      // 数字开头
		{FieldKey: "a-b", Label: "x", FieldType: TypeText},                       // 含短横线
		{FieldKey: "ok", Label: "", FieldType: TypeText},                         // 空标签
		{FieldKey: "ok", Label: "x", FieldType: "unknown"},                       // 非法类型
		{FieldKey: "ok", Label: "x", FieldType: TypeSelect, Options: `not-json`}, // 非法 options
	}
	for _, d := range bad {
		if err := validate(&d); err == nil {
			t.Errorf("应拒绝 %+v", d)
		}
	}
}

// TestValidateValues 按定义校验值：数字/日期/下拉选项，未知键丢弃。
func TestValidateValues(t *testing.T) {
	defs := []Def{
		{FieldKey: "age", Label: "年龄", FieldType: TypeNumber},
		{FieldKey: "type", Label: "类型", FieldType: TypeSelect, Options: `["正编","外包"]`},
		{FieldKey: "hired", Label: "入职", FieldType: TypeDate},
		{FieldKey: "name", Label: "姓名", FieldType: TypeText},
	}

	ok, err := ValidateValues(defs, map[string]string{
		"age": "30", "type": "正编", "hired": "2024-01-02", "name": " 张三 ",
	})
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if ok["name"] != "张三" {
		t.Fatalf("应去除首尾空白: %q", ok["name"])
	}

	bad := []map[string]string{
		{"age": "abc"},
		{"type": "实习"},
		{"hired": "2024/01/02"},
	}
	for _, v := range bad {
		if _, err := ValidateValues(defs, v); err == nil {
			t.Errorf("应拒绝 %v", v)
		}
	}

	// 未知字段直接丢弃（不报错，避免旧前端提交脏键）
	got, err := ValidateValues(defs, map[string]string{"unknown": "x", "name": "李四"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got["unknown"]; exists {
		t.Fatal("未知字段应被丢弃")
	}
	if got["name"] != "李四" {
		t.Fatal("已知字段应保留")
	}
}

// TestRepoCRUD 用户字段定义的增删改查，内置字段不可删。
func TestRepoCRUD(t *testing.T) {
	db := setupDB(t)
	repo, err := NewRepo(db, KindUser)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	defs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// 迁移应已插入内置字段
	builtinCount := 0
	for _, d := range defs {
		if d.Builtin {
			builtinCount++
		}
	}
	if builtinCount == 0 {
		t.Fatal("应有内置人事字段")
	}

	key := "test_field_" + t.Name()
	// 清理可能残留的测试数据
	db.Exec(`DELETE FROM user_field_defs WHERE field_key=$1`, key)
	
	id, err := repo.Create(ctx, &Def{
		FieldKey: key, Label: "测试字段", FieldType: TypeText,
		ShowInList: true, ShowInForm: true, Editable: true, SortOrder: 999,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM user_field_defs WHERE id=$1`, id) })

	got, err := repo.Get(ctx, id)
	if err != nil || got.FieldKey != key {
		t.Fatalf("Get 不符: %+v err=%v", got, err)
	}

	got.Label = "改名后"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, _ := repo.Get(ctx, id)
	if again.Label != "改名后" {
		t.Fatalf("更新未生效: %q", again.Label)
	}

	// 重复键应报错
	if _, err := repo.Create(ctx, &Def{FieldKey: key, Label: "dup", FieldType: TypeText}); err == nil {
		t.Fatal("重复键应报错")
	}

	// 内置字段不可删
	for _, d := range defs {
		if d.Builtin {
			if err := repo.Delete(ctx, d.ID); err == nil {
				t.Fatal("内置字段不应可删除")
			}
			break
		}
	}

	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, id); err == nil {
		t.Fatal("删除后应查不到")
	}
}

// TestSaveUserValues 保存并读取用户扩展字段值。
func TestSaveUserValues(t *testing.T) {
	db := setupDB(t)
	repo, err := NewRepo(db, KindUser)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := "test-user-" + t.Name()
	key := "test_val_" + t.Name()

	t.Cleanup(func() {
		db.Exec(`DELETE FROM user_field_values WHERE user_id=$1`, user)
	})

	if err := repo.SaveUserValues(ctx, user, map[string]string{key: "v1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	vals, err := repo.UserValues(ctx, user)
	if err != nil || vals[key] != "v1" {
		t.Fatalf("读取不符: %v err=%v", vals, err)
	}
	// 覆盖
	if err := repo.SaveUserValues(ctx, user, map[string]string{key: "v2"}); err != nil {
		t.Fatal(err)
	}
	vals, _ = repo.UserValues(ctx, user)
	if vals[key] != "v2" {
		t.Fatalf("覆盖未生效: %q", vals[key])
	}
	// 批量读取
	all, err := repo.UserValuesAll(ctx)
	if err != nil || all[user][key] != "v2" {
		t.Fatalf("UserValuesAll 不符: %v err=%v", all[user], err)
	}
}
