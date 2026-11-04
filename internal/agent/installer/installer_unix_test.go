//go:build !windows

package installer

import (
	"strings"
	"testing"
)

func TestReplaceServiceLinePreservesCustom(t *testing.T) {
	old := "# custom comment\n[Unit]\nDescription=My Agent\n[Service]\nExecStart=/old/agent run\nEnvironment=FOO=1\n"
	got, found := replaceServiceLine(old, "ExecStart=", "ExecStart=/new/agent run --log /var/log/upgrade-agent.log")
	if !found {
		t.Fatal("应找到 ExecStart")
	}
	for _, want := range []string{"Environment=FOO=1", "Description=My Agent", "ExecStart=/new/agent run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("增量刷新丢失内容 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/old/agent") {
		t.Fatalf("旧 ExecStart 未替换:\n%s", got)
	}
}

func TestUpdateUpstartConfigRemovesRedirection(t *testing.T) {
	old := `# custom
description "agent"
start on runlevel [2345]
respawn
respawn limit unlimited

script
    umask 027
    exec /usr/local/bin/upgrade-agent run --server http://old --state /var/lib/upgrade-agent/state.json >>/var/log/upgrade-agent.log 2>&1
end script
`
	execLine := "exec /usr/local/bin/upgrade-agent run --server http://new --state /var/lib/upgrade-agent/state.json --log /var/log/upgrade-agent.log --log-max-mb 10 --log-backups 5"
	got, found := updateUpstartConfig(old, execLine)
	if !found {
		t.Fatal("应识别 script/exec 块")
	}
	if strings.Contains(got, ">>/var/log/upgrade-agent.log") || strings.Contains(got, "\nscript\n") || strings.Contains(got, "end script") {
		t.Fatalf("旧 stderr 重定向未清理:\n%s", got)
	}
	if !strings.Contains(got, execLine) {
		t.Fatalf("新 exec 行未写入:\n%s", got)
	}
	if !strings.Contains(got, "respawn limit 10 5") {
		t.Fatalf("respawn limit 未修正:\n%s", got)
	}
	if !strings.Contains(got, "# custom") {
		t.Fatalf("自定义注释被丢失:\n%s", got)
	}
}
