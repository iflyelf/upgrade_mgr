package extbin

import (
	"net"
	"testing"
)

// TestCheckHostPrivatePolicy 内网放行策略：默认拒绝，AllowPrivate 全放开，CIDR 精确放开。
func TestCheckHostPrivatePolicy(t *testing.T) {
	const internal = "http://10.8.0.5:8081/file.exe"
	const loopback = "http://127.0.0.1:8080/file.exe"
	hosts := []string{"10.8.0.5", "127.0.0.1"}

	// 默认严格：拒绝内网
	if err := CheckHost(internal, hosts, AllowRule{}); err == nil {
		t.Fatal("默认应拒绝内网地址")
	}
	if err := CheckHost(loopback, hosts, AllowRule{}); err == nil {
		t.Fatal("默认应拒绝环回地址")
	}

	// 精确放行网段
	rule := AllowRule{AllowCIDRs: []string{"10.0.0.0/8"}}
	if err := CheckHost(internal, hosts, rule); err != nil {
		t.Fatalf("放行网段内的地址应通过: %v", err)
	}
	if err := CheckHost(loopback, hosts, rule); err == nil {
		t.Fatal("不在放行网段内的环回地址仍应拒绝")
	}

	// 单个 IP 放行
	rule = AllowRule{AllowCIDRs: []string{"127.0.0.1"}}
	if err := CheckHost(loopback, hosts, rule); err != nil {
		t.Fatalf("单个 IP 放行应生效: %v", err)
	}

	// 全局开关
	if err := CheckHost(internal, hosts, AllowRule{AllowPrivate: true}); err != nil {
		t.Fatalf("AllowPrivate 应放行内网: %v", err)
	}

	// 域名白名单仍然优先
	if err := CheckHost("http://10.9.9.9/x", []string{"10.8.0.5"}, AllowRule{AllowPrivate: true}); err == nil {
		t.Fatal("域名不在白名单时即使 AllowPrivate 也应拒绝")
	}
}

func TestValidateCIDRs(t *testing.T) {
	ok := [][]string{
		nil,
		{"10.0.0.0/8"},
		{"192.168.1.10"},
		{"10.0.0.0/8", "172.16.0.0/12", "127.0.0.1"},
	}
	for _, list := range ok {
		if err := ValidateCIDRs(list); err != nil {
			t.Errorf("应通过 %v: %v", list, err)
		}
	}
	bad := [][]string{
		{"10.0.0.0/33"},
		{"not-a-cidr"},
		{"10.0.0.0/8", "bad"},
	}
	for _, list := range bad {
		if err := ValidateCIDRs(list); err == nil {
			t.Errorf("应拒绝 %v", list)
		}
	}
}

// TestCheckIPNonInternalAlwaysAllowed 公网地址不受内网策略影响。
func TestCheckIPNonInternalAlwaysAllowed(t *testing.T) {
	if err := (AllowRule{}).checkIP(net.ParseIP("203.0.113.10")); err != nil {
		t.Fatalf("公网地址应放行: %v", err)
	}
}
