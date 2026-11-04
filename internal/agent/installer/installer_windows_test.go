//go:build windows

package installer

import (
	"strings"
	"testing"
)

func TestBuildServiceCommandLineIncludesLogFlags(t *testing.T) {
	got := buildServiceCommandLine(Options{
		BinPath:    `C:\Program Files\upgrade-agent\upgrade-agent.exe`,
		ServerURL:  "http://mgr.example.com:8090",
		StatePath:  `C:\ProgramData\upgrade-agent\state.json`,
		LogPath:    `C:\ProgramData\upgrade-agent\logs\upgrade-agent.log`,
		LogMaxMB:   10,
		LogBackups: 5,
	})
	for _, want := range []string{"--log", "--log-max-mb 10", "--log-backups 5", "--service"} {
		if !strings.Contains(got, want) {
			t.Fatalf("服务命令行缺少 %q: %s", want, got)
		}
	}
	if !strings.Contains(got, `"C:\Program Files\upgrade-agent\upgrade-agent.exe"`) {
		t.Fatalf("含空格路径未正确引用: %s", got)
	}
}
