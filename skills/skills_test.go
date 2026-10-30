package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
	"time"
)

var testData = Data{ServerName: "my-mcp", MCPURL: "https://ops.example.com/mcp", BaseURL: "https://ops.example.com", TokenEnv: "MY_TOKEN"}

// TestCatalogValid 每个内嵌 Skill 必须符合 Agent Skills 规范，且模板变量全部可渲染。
func TestCatalogValid(t *testing.T) {
	list, err := Catalog(testData)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("至少应内嵌一个 Skill")
	}
	for _, m := range list {
		if m.Metadata["mcp_server"] != "my-mcp" || m.Metadata["token_env"] != "MY_TOKEN" {
			t.Fatalf("%s 未按设置渲染 metadata: %+v", m.Name, m.Metadata)
		}
	}
}

// TestRenderUsesSettings 渲染结果不得残留模板语法，且使用传入的服务名/地址。
func TestRenderUsesSettings(t *testing.T) {
	for _, name := range Names() {
		files, err := Files(name, testData)
		if err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			s := string(body)
			if strings.Contains(s, "{{") {
				t.Errorf("%s/%s 残留模板语法", name, f)
			}
		}
		if !strings.Contains(string(files[skillFile]), "https://ops.example.com/mcp") {
			t.Errorf("%s 未渲染 MCP 地址", name)
		}
	}
}

func TestArchive(t *testing.T) {
	name := Names()[0]
	data, err := Archive(name, testData, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(h.Name, name+"/") {
			t.Fatalf("归档条目 %s 不在 %s/ 目录下", h.Name, name)
		}
		if h.Name == name+"/"+skillFile {
			found = true
		}
	}
	if !found {
		t.Fatal("归档缺少 SKILL.md")
	}
}

func TestExistsRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "../x", "a/b", "UPPER", "a--b"} {
		if Exists(bad) {
			t.Errorf("Exists(%q) 应为 false", bad)
		}
	}
}
