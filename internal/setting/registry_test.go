package setting

import (
	"context"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

// TestRegistryIntegrity 校验设置注册表自身的一致性。
//
// 注册表是手写的大列表，最容易出的错是「键重复」（后者静默覆盖前者，
// 页面上会出现两个同名输入框，保存结果取决于遍历顺序）与「漏填 Get/Set」
// （渲染或保存时 panic）。这些都无法靠编译发现，所以用测试兜住。
func TestRegistryIntegrity(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Registry {
		if it.Key == "" {
			t.Error("存在空 Key 的配置项")
			continue
		}
		if seen[it.Key] {
			t.Errorf("配置键重复: %s", it.Key)
		}
		seen[it.Key] = true

		if it.Get == nil {
			t.Errorf("%s 缺少 Get", it.Key)
		}
		if it.Set == nil {
			t.Errorf("%s 缺少 Set", it.Key)
		}
		if it.Label == "" {
			t.Errorf("%s 缺少 Label", it.Key)
		}
		if it.Group == "" {
			t.Errorf("%s 缺少 Group", it.Key)
		}
		// enum 必须给候选值，否则页面渲染出空下拉，用户无法选择。
		if it.Type == "enum" && len(it.Options) == 0 {
			t.Errorf("%s 为 enum 但未提供 Options", it.Key)
		}
	}
	if len(Registry) != len(seen) {
		t.Errorf("注册表 %d 项，唯一键 %d 个", len(Registry), len(seen))
	}
}

// TestReadOnlyItemsAreInert 只读项的 Set 必须是空操作。
//
// 只读项（数据库连接参数）在 Apply 中会被跳过，但万一将来有人改了跳过逻辑，
// 空实现的 Set 是第二道防线：即使被调用也不会篡改正在使用的连接配置。
func TestReadOnlyItemsAreInert(t *testing.T) {
	for _, it := range Registry {
		if !it.ReadOnly {
			continue
		}
		cfg := config.Default()
		before := it.Get(cfg)
		it.Set(cfg, "tampered-value")
		if after := it.Get(cfg); after != before {
			t.Errorf("只读项 %s 的 Set 修改了配置: %q → %q", it.Key, before, after)
		}
	}
}

// TestApplyRejectsReadOnlyChange 篡改只读键必须报错，不能静默写库。
//
// 页面已禁用这些输入框，但 API 可被直接调用。若放任写入，
// app_settings 里会留下一个无效的数据库连接参数，下次启动 Load 时
// 被套用到 Config 上，排查起来非常隐蔽，所以这里要明确拒绝。
func TestApplyRejectsReadOnlyChange(t *testing.T) {
	readOnlyKey := firstReadOnlyKey(t)

	cfg := config.Default()
	// db 传 nil：校验阶段就应拒绝，绝不该走到 SQL（否则 nil 解引用会 panic）。
	svc := &Service{db: nil, cfg: cfg, items: registryMap}
	_, err := svc.Apply(context.Background(), map[string]string{readOnlyKey: "tampered"})
	if err == nil {
		t.Fatalf("篡改只读键 %s 应报错，却被接受", readOnlyKey)
	}
	if !strings.Contains(err.Error(), readOnlyKey) {
		t.Errorf("错误信息应指明是哪一项：%v", err)
	}
}

// TestApplyIgnoresUnchangedReadOnly 原值回传只读键应被忽略，不写库。
//
// 页面保存时会把整个表单提交一遍（含禁用项的当前值），
// 这种"没改动"的提交必须当作无事发生，否则用户每次保存都会收到报错。
func TestApplyIgnoresUnchangedReadOnly(t *testing.T) {
	readOnlyKey := firstReadOnlyKey(t)

	cfg := config.Default()
	svc := &Service{db: nil, cfg: cfg, items: registryMap}
	current := registryMap[readOnlyKey].Get(cfg)
	// db 为 nil：若实现尝试把只读键写库，这里会 panic，正是要暴露的问题。
	applied, err := svc.Apply(context.Background(), map[string]string{readOnlyKey: current})
	if err != nil {
		t.Fatalf("回传只读键原值不应报错: %v", err)
	}
	for _, k := range applied {
		if k == readOnlyKey {
			t.Errorf("只读键 %s 不应出现在 applied 中", k)
		}
	}
}

// firstReadOnlyKey 取注册表中第一个只读键，没有则跳过测试。
func firstReadOnlyKey(t *testing.T) string {
	t.Helper()
	for _, it := range Registry {
		if it.ReadOnly {
			return it.Key
		}
	}
	t.Skip("注册表中没有只读项")
	return ""
}

// TestRestartItemsAreSignposted 需重启的项必须让用户看得出来。
//
// 不写死「每项都要 Note」：大量同类参数（如 S3 的 region/bucket/key）
// 逐条重复同一句说明只是噪音。两种方式都算合格：
//   - 分组名里带「需重启」（整组统一提示，如「Casdoor（需重启）」）；
//   - 或该项自带 Note 说明原因/影响。
//
// 两者都没有时用户会以为保存即生效，排查很费时间。
func TestRestartItemsAreSignposted(t *testing.T) {
	for _, it := range Registry {
		if !it.Restart {
			continue
		}
		if strings.Contains(it.Group, "需重启") || strings.TrimSpace(it.Note) != "" {
			continue
		}
		t.Errorf("%s 标记为需重启，但分组名未含「需重启」且没有 Note 说明", it.Key)
	}
}

// TestReadOnlyItemsAreSignposted 只读项同样必须有提示。
//
// 只读项是「展示当前生效值」，用户若以为能改会反复尝试。
func TestReadOnlyItemsAreSignposted(t *testing.T) {
	for _, it := range Registry {
		if !it.ReadOnly {
			continue
		}
		if strings.Contains(it.Group, "只读") || strings.TrimSpace(it.Note) != "" {
			continue
		}
		t.Errorf("%s 标记为只读，但分组名未含「只读」且没有 Note 说明", it.Key)
	}
}

// TestSecretItemsMasked View 必须对 secret 项脱敏。
func TestSecretItemsMasked(t *testing.T) {
	cfg := config.Default()
	cfg.Redis.Password = "super-secret"
	cfg.ConsulMgr.ServiceToken = "token-value"

	svc := &Service{cfg: cfg, items: registryMap}
	for _, row := range svc.View() {
		key, _ := row["key"].(string)
		isSecret, _ := row["secret"].(bool)
		val, _ := row["value"].(string)
		if !isSecret {
			continue
		}
		if val != "" && val != MaskedSecret {
			t.Errorf("secret 项 %s 未脱敏，返回了 %q", key, val)
		}
	}
}
