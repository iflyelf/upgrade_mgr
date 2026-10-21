package task

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

func TestScriptArtifact(t *testing.T) {
	spec := protocol.ComponentSpec{DeployMode: protocol.DeployScript, ScriptURL: "https://repo.example.com/install.sh"}
	art, ok := scriptArtifact(spec, "1.2.3", "arm64", "linux")
	if !ok {
		t.Fatal("script 模式配置 script_url 应跳过制品库")
	}
	if art.Version != "1.2.3" || art.Arch != "arm64" || art.OSFamily != "linux" {
		t.Fatalf("下发制品字段不正确: %+v", art)
	}
	if art.URL != "" {
		t.Fatalf("自定义地址不应写入 Artifact.URL（旧版 Agent 会携带凭据请求）: %+v", art)
	}
	if art.MD5 != "" || art.SHA256 != "" {
		t.Fatalf("自定义 URL 不应伪造校验和: %+v", art)
	}

	if _, ok := scriptArtifact(protocol.ComponentSpec{DeployMode: protocol.DeployScript}, "1.2.3", "amd64", "linux"); ok {
		t.Fatal("未配置 script_url 时应回退制品库")
	}
	if _, ok := scriptArtifact(protocol.ComponentSpec{DeployMode: protocol.DeployReplace, ScriptURL: spec.ScriptURL}, "1.2.3", "amd64", "linux"); ok {
		t.Fatal("非 script 模式不应使用 script_url")
	}
}

func TestToComponentSpecCarriesScriptURL(t *testing.T) {
	c := &component.Component{
		Name:       "node_exporter",
		DeployMode: protocol.DeployScript,
		ScriptURL:  "https://repo.example.com/install.sh",
	}
	spec := toComponentSpec(c)
	if spec.ScriptURL != c.ScriptURL {
		t.Fatalf("toComponentSpec 未传递 script_url: %q", spec.ScriptURL)
	}
}
