package task

import (
	"reflect"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

func TestHostPlatformURL(t *testing.T) {
	s := &Service{}
	if got := s.hostPlatformURL(""); got != "" {
		t.Fatalf("未配置 external_url 且主机未上报时应为空: %q", got)
	}
	s.SetExternalURL(func() string { return " https://um.example.com/ " })
	if got := s.hostPlatformURL(""); got != "https://um.example.com" {
		t.Fatalf("应回退 external_url: %q", got)
	}
	if got := s.hostPlatformURL("http://10.0.0.5:8090/"); got != "http://10.0.0.5:8090" {
		t.Fatalf("应优先主机上报的连接地址: %q", got)
	}
}

func TestAppendPlatformEnv(t *testing.T) {
	if got := appendPlatformEnv(nil, ""); got != nil {
		t.Fatalf("地址为空时不应追加: %v", got)
	}
	base := make([]string, 1, 4)
	base[0] = "A=1"
	got := appendPlatformEnv(base, "http://p")
	if !reflect.DeepEqual(got, []string{"A=1", "UPGRADE_MGR_SERVER=http://p"}) {
		t.Fatalf("追加结果不符: %v", got)
	}
	// 不能写入调用方共享切片的底层数组（多主机共用同一组件定义）。
	if other := append(base, "B=2"); other[1] != "B=2" || got[1] != "UPGRADE_MGR_SERVER=http://p" {
		t.Fatalf("返回值与原切片共享了底层数组: %v %v", got, other)
	}
	custom := []string{"UPGRADE_MGR_SERVER=http://custom"}
	if got := appendPlatformEnv(custom, "http://p"); !reflect.DeepEqual(got, custom) {
		t.Fatalf("用户显式设置时不应覆盖: %v", got)
	}

	spec := protocol.CommandSpec{Script: "echo"}
	out := withPlatformEnv(spec, "http://p")
	if len(spec.Env) != 0 || len(out.Env) != 1 {
		t.Fatalf("withPlatformEnv 不应修改原规格: %v %v", spec.Env, out.Env)
	}
}
