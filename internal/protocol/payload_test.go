package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCommandPayloadOmitsEmptyCommand 防止回归：Command 必须用指针 + omitempty。
//
// 早期实现把 Command 写成值类型（CommandSpec）并带 omitempty，但 encoding/json
// 不会省略零值结构体，导致所有指令（含 self_update/upgrade）载荷都多出
// "command":{"script":""}。旧 Agent 不认识该字段，验签时重新序列化会丢弃它，
// 结果就是「signer: 签名不匹配」。
func TestCommandPayloadOmitsEmptyCommand(t *testing.T) {
	cases := []struct {
		name string
		pl   CommandPayload
	}{
		{
			name: "self_update",
			pl: CommandPayload{
				ToVersion: "1.2.3",
				Artifact:  Artifact{Version: "1.2.3", Arch: "amd64", URL: "/api/agent/binary/linux-amd64", SHA256: "abc"},
			},
		},
		{
			name: "upgrade",
			pl: CommandPayload{
				Component: ComponentSpec{Name: "node_exporter", DeployMode: DeployScript},
				Artifact:  Artifact{Version: "latest", Arch: "amd64", URL: "/api/artifacts/1/download"},
				ToVersion: "latest",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.pl)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(b), `"command"`) {
				t.Fatalf("载荷不应包含 command 字段: %s", b)
			}
		})
	}
}

// TestCommandPayloadKeepsCommand 确认 run_command 仍然携带命令规格。
func TestCommandPayloadKeepsCommand(t *testing.T) {
	pl := CommandPayload{
		ToVersion: "command",
		Command:   &CommandSpec{Script: "echo hello", Interpreter: "/bin/bash"},
	}
	b, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"script":"echo hello"`) {
		t.Fatalf("载荷应包含命令脚本: %s", b)
	}
}

// TestCanonicalPayloadWithLegacyCommand 覆盖 512d1f9 版 Agent 的签名兼容。
//
// 该版本 Command 是值类型，验签时总会在载荷末尾补出 `"command":{"script":""}`。
// 服务端对这类 Agent 必须签同样的字节，否则连 self_update 都会被拒绝。
func TestCanonicalPayloadWithLegacyCommand(t *testing.T) {
	pl := CommandPayload{
		ToVersion: "1.2.3",
		Artifact:  Artifact{Version: "1.2.3", Arch: "amd64", URL: "/api/agent/binary/linux-amd64", SHA256: "abc"},
	}
	base, _ := json.Marshal(pl)
	got := string(CanonicalPayloadWithLegacyCommand(base))
	want := strings.TrimSuffix(string(base), "}") + `,"command":{"script":""}}`
	if got != want {
		t.Fatalf("旧版 canonical 不匹配:\n got=%s\nwant=%s", got, want)
	}

	// 已经带 command 的载荷不得重复追加。
	withCmd := CommandPayload{Command: &CommandSpec{Script: "echo hi"}}
	b, _ := json.Marshal(withCmd)
	if again := string(CanonicalPayloadWithLegacyCommand(b)); again != string(b) {
		t.Fatalf("已含 command 时不应改动: %s", again)
	}

	// 空载荷不做处理，避免生成非法 JSON。
	if got := string(CanonicalPayloadWithLegacyCommand(nil)); got != "" {
		t.Fatalf("空载荷应原样返回: %q", got)
	}
}
