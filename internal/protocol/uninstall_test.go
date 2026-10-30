package protocol

import "testing"

func TestIsUninstallAction(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"uninstall"}, true},
		{[]string{"uninstall", "-y"}, true},
		{[]string{"-y", "uninstall"}, true},
		{[]string{"UNINSTALL", "-Yes"}, true},
		{[]string{"remove"}, true},
		{[]string{"purge", "--force"}, true},
		{[]string{"install"}, false},
		{[]string{"update"}, false},
		{[]string{"-y", "-v"}, false}, // 全是 flag
		{[]string{}, false},
		{[]string{""}, false},
		{[]string{"  REMOVE  "}, true}, // 前后空格
	}
	for _, tt := range tests {
		got := IsUninstallAction(tt.args)
		if got != tt.want {
			t.Errorf("IsUninstallAction(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
