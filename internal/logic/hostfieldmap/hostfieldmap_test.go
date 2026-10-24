package hostfieldmap

import "testing"

func TestValidateSourceAndTarget(t *testing.T) {
	defs := map[string]bool{"rack": true}

	// 合法：映射到白名单内置列
	m := Mapping{Source: "consul", SourceKey: "app_code", TargetKind: "column", TargetKey: "app_code"}
	if err := validate(&m, defs); err != nil {
		t.Fatalf("合法的列映射被拒: %v", err)
	}

	// 合法：映射到已定义的自定义字段
	m = Mapping{Source: "consul_mgr", SourceKey: "rack-id", TargetKind: "custom", TargetKey: "rack"}
	if err := validate(&m, defs); err != nil {
		t.Fatalf("合法的自定义字段映射被拒: %v", err)
	}

	// 来源为空时默认 consul
	m = Mapping{SourceKey: "a", TargetKind: "column", TargetKey: "vendor"}
	if err := validate(&m, defs); err != nil || m.Source != SourceConsul {
		t.Fatalf("来源默认值错误: source=%q err=%v", m.Source, err)
	}
}

// TestValidateRejectsSensitiveColumns 是本包最重要的保护：
// 绝不允许把外部来源的值写到主机身份/控制字段上。
func TestValidateRejectsSensitiveColumns(t *testing.T) {
	for _, col := range []string{
		"ip", "agent_id", "source", "tenant_id", "agent_status",
		"os_family", "arch", "maintenance_until", "last_seen",
		// 典型注入尝试
		"ip, agent_id", "hostname; DROP TABLE hosts", "*",
	} {
		m := Mapping{Source: "consul", SourceKey: "x", TargetKind: "column", TargetKey: col}
		if err := validate(&m, nil); err == nil {
			t.Errorf("列 %q 必须被拒绝，但校验通过了", col)
		}
	}
}

func TestValidateRejectsBadKeys(t *testing.T) {
	// 未定义的自定义字段
	m := Mapping{Source: "consul", SourceKey: "x", TargetKind: "custom", TargetKey: "nope"}
	if err := validate(&m, map[string]bool{}); err == nil {
		t.Error("未定义的自定义字段必须被拒绝")
	}
	// 非法来源
	m = Mapping{Source: "cmdb", SourceKey: "x", TargetKind: "column", TargetKey: "vendor"}
	if err := validate(&m, nil); err == nil {
		t.Error("未支持的来源必须被拒绝")
	}
	// 非法目标类型
	m = Mapping{Source: "consul", SourceKey: "x", TargetKind: "table", TargetKey: "vendor"}
	if err := validate(&m, nil); err == nil {
		t.Error("未支持的目标类型必须被拒绝")
	}
	// 非法来源键
	for _, k := range []string{"", " ", "-bad", "_bad", "a b"} {
		m = Mapping{Source: "consul", SourceKey: k, TargetKind: "column", TargetKey: "vendor"}
		if err := validate(&m, nil); err == nil {
			t.Errorf("来源键 %q 必须被拒绝", k)
		}
	}
	// 自定义字段键必须字母开头
	m = Mapping{Source: "consul", SourceKey: "x", TargetKind: "custom", TargetKey: "1bad"}
	if err := validate(&m, map[string]bool{"1bad": true}); err == nil {
		t.Error("数字开头的自定义字段键必须被拒绝")
	}
}

// TestValidateAcceptsReservedSourceKeys 平台注入的结构化键（__group_name 等）
// 以下划线开头，必须被接受，否则无法映射 consul_mgr 的服务组信息。
func TestValidateAcceptsReservedSourceKeys(t *testing.T) {
	for _, k := range []string{"__group_name", "__group_code", "__health"} {
		m := Mapping{Source: SourceConsulMgr, SourceKey: k, TargetKind: "column", TargetKey: "biz_group"}
		if err := validate(&m, nil); err != nil {
			t.Errorf("保留键 %q 应被接受: %v", k, err)
		}
	}
	// 但普通的单下划线前缀仍然非法，避免与保留键混淆
	m := Mapping{Source: SourceConsulMgr, SourceKey: "_x", TargetKind: "column", TargetKey: "vendor"}
	if err := validate(&m, nil); err == nil {
		t.Error("单下划线前缀的来源键必须被拒绝")
	}
}

func TestSetResolve(t *testing.T) {
	set := NewSet([]Mapping{
		{ID: 1, SourceKey: "app_code", TargetKind: "column", TargetKey: "app_code", Enabled: true, SortOrder: 10},
		{ID: 2, SourceKey: "rack", TargetKind: "custom", TargetKey: "rack", Enabled: true, SortOrder: 20},
		{ID: 3, SourceKey: "owner", TargetKind: "custom", TargetKey: "owner", Enabled: true, SortOrder: 30, Overwrite: true},
		// 未启用的映射必须被忽略
		{ID: 4, SourceKey: "vendor", TargetKind: "column", TargetKey: "vendor", Enabled: false, SortOrder: 40},
	})
	if set.Len() != 3 {
		t.Fatalf("启用映射数 = %d, want 3", set.Len())
	}

	got := set.Resolve(map[string]string{
		"app_code": "pay",
		"rack":     " A-12 ",
		"owner":    "zhang",
		"vendor":   "dell", // 映射未启用，不应出现
		"unmapped": "x",    // 没有映射，不应出现
	})
	if got.Columns["app_code"] != "pay" {
		t.Errorf("app_code = %q", got.Columns["app_code"])
	}
	if _, ok := got.Columns["vendor"]; ok {
		t.Error("未启用的映射不应产生结果")
	}
	if got.Custom["rack"] != "A-12" {
		t.Errorf("rack 应被去空白, got %q", got.Custom["rack"])
	}
	if !got.OverwriteCustom["owner"] || got.OverwriteCustom["rack"] {
		t.Errorf("overwrite 标记错误: %#v", got.OverwriteCustom)
	}
	if len(got.Custom) != 2 {
		t.Errorf("自定义字段数 = %d, want 2", len(got.Custom))
	}
}

// TestSetResolveFallbackChain 多条映射指向同一目标时按 sort_order 先到先得，
// 用于配置「优先 app_code，缺失则取 appCode」这类回退链。
func TestSetResolveFallbackChain(t *testing.T) {
	set := NewSet([]Mapping{
		{ID: 1, SourceKey: "app_code", TargetKind: "column", TargetKey: "app_code", Enabled: true, SortOrder: 10},
		{ID: 2, SourceKey: "appCode", TargetKind: "column", TargetKey: "app_code", Enabled: true, SortOrder: 20},
	})

	// 首选键存在：用首选值
	if got := set.Resolve(map[string]string{"app_code": "a", "appCode": "b"}); got.Columns["app_code"] != "a" {
		t.Errorf("首选映射未生效: %q", got.Columns["app_code"])
	}
	// 首选键缺失：回退到次选
	if got := set.Resolve(map[string]string{"appCode": "b"}); got.Columns["app_code"] != "b" {
		t.Errorf("回退映射未生效: %q", got.Columns["app_code"])
	}
	// 首选键存在但为空白：视为缺失，回退
	if got := set.Resolve(map[string]string{"app_code": "  ", "appCode": "b"}); got.Columns["app_code"] != "b" {
		t.Errorf("空白值应视为缺失并回退: %q", got.Columns["app_code"])
	}
}

// TestNewSetDropsNonWhitelistColumn 库里的数据被手工改坏时，
// 解析阶段必须再次拦住非白名单列。
func TestNewSetDropsNonWhitelistColumn(t *testing.T) {
	set := NewSet([]Mapping{
		{ID: 1, SourceKey: "x", TargetKind: "column", TargetKey: "agent_id", Enabled: true},
		{ID: 2, SourceKey: "y", TargetKind: "column", TargetKey: "vendor", Enabled: true},
	})
	if set.Len() != 1 {
		t.Fatalf("非白名单列应被丢弃, Len=%d", set.Len())
	}
	got := set.Resolve(map[string]string{"x": "evil", "y": "dell"})
	if _, ok := got.Columns["agent_id"]; ok {
		t.Error("agent_id 绝不能出现在解析结果中")
	}
	if got.Columns["vendor"] != "dell" {
		t.Errorf("vendor = %q", got.Columns["vendor"])
	}
}

func TestResolvedEmpty(t *testing.T) {
	set := NewSet(nil)
	if !set.Resolve(map[string]string{"a": "b"}).Empty() {
		t.Error("没有映射时结果应为空")
	}
	if set.Resolve(nil).Empty() != true {
		t.Error("meta 为 nil 时结果应为空")
	}
}

// TestAllowedColumnsExcludeIdentity 白名单本身的回归测试：
// 新增列时若误把身份/状态字段加进来，这里会失败。
func TestAllowedColumnsExcludeIdentity(t *testing.T) {
	forbidden := []string{
		"id", "ip", "agent_id", "agent_version", "agent_status", "source",
		"tenant_id", "os_family", "os_version", "arch", "kernel",
		"last_seen", "last_synced_at", "maintenance_until", "custom_fields",
		"component_versions", "created_at", "updated_at",
	}
	for _, col := range forbidden {
		if _, ok := AllowedColumns[col]; ok {
			t.Errorf("列 %q 不应出现在可映射白名单中", col)
		}
	}
	if len(ColumnOptions()) != len(AllowedColumns) {
		t.Error("ColumnOptions 应覆盖全部白名单列")
	}
}
