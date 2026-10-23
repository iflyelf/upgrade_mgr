// Command quickstart-gen 由 web/quickstart.yaml 生成：
//   - web/quickstart.json：嵌入式结构化文案（页面消费）
//   - README.md 中 <!-- QUICKSTART:START --> 与 <!-- QUICKSTART:END --> 之间的中英对照
//
// 用法（仓库根目录）：
//
//	go run ./cmd/quickstart-gen          # 生成
//	go run ./cmd/quickstart-gen -check   # 仅校验是否与源文件一致（CI）
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	yamlPath      = "web/quickstart.yaml"
	jsonPath      = "web/quickstart.json"
	readmePath    = "README.md"
	startMarker   = "<!-- QUICKSTART:START -->"
	endMarker     = "<!-- QUICKSTART:END -->"
	generatedNote = "<!-- 本段由 web/quickstart.yaml 生成；请勿手工编辑，修改后运行 `make docs-quickstart`。 -->"
)

type Localized struct {
	Zh string `yaml:"zh" json:"zh"`
	En string `yaml:"en" json:"en"`
}

type LocalizedList struct {
	Zh []string `yaml:"zh" json:"zh"`
	En []string `yaml:"en" json:"en"`
}

type Step struct {
	No       string        `yaml:"no" json:"no"`
	Title    Localized     `yaml:"title" json:"title"`
	View     string        `yaml:"view" json:"view"`
	Jump     Localized     `yaml:"jump" json:"jump"`
	Lines    LocalizedList `yaml:"lines" json:"lines"`
	CodeLang string        `yaml:"code_lang" json:"code_lang"`
	Code     Localized     `yaml:"code" json:"code"`
}

type System struct {
	Label Localized `yaml:"label" json:"label"`
	Steps []Step    `yaml:"steps" json:"steps"`
}

type QuickStart struct {
	Version    int                  `yaml:"version" json:"version"`
	Title      Localized            `yaml:"title" json:"title"`
	Summary    Localized            `yaml:"summary" json:"summary"`
	Labels     map[string]Localized `yaml:"labels" json:"labels"`
	ViewLabels map[string]Localized `yaml:"viewLabels" json:"viewLabels"`
	Intro      LocalizedList        `yaml:"intro" json:"intro"`
	Systems    map[string]System    `yaml:"systems" json:"systems"`
	Footer     LocalizedList        `yaml:"footer" json:"footer"`
}

func main() {
	check := flag.Bool("check", false, "仅校验生成物是否与源文件一致")
	flag.Parse()

	src, err := os.ReadFile(yamlPath)
	if err != nil {
		fatal("读取 %s 失败: %v", yamlPath, err)
	}
	var q QuickStart
	if err := yaml.Unmarshal(src, &q); err != nil {
		fatal("解析 %s 失败: %v", yamlPath, err)
	}
	if err := validate(&q); err != nil {
		fatal("校验失败: %v", err)
	}

	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		fatal("生成 JSON 失败: %v", err)
	}
	data = append(data, '\n')

	readme, err := os.ReadFile(readmePath)
	if err != nil {
		fatal("读取 %s 失败: %v", readmePath, err)
	}
	newReadme, err := replaceSection(string(readme), renderReadme(&q))
	if err != nil {
		fatal("%v", err)
	}

	if *check {
		jsonOK := fileEquals(jsonPath, data)
		readmeOK := fileEquals(readmePath, []byte(newReadme))
		if !jsonOK || !readmeOK {
			fmt.Fprintln(os.Stderr, "快速上手文案与 web/quickstart.yaml 不一致，请运行：make docs-quickstart")
			if !jsonOK {
				fmt.Fprintln(os.Stderr, "- web/quickstart.json 需要更新")
			}
			if !readmeOK {
				fmt.Fprintln(os.Stderr, "- README.md 快速上手章节需要更新")
			}
			os.Exit(1)
		}
		fmt.Println("快速上手文案一致")
		return
	}

	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		fatal("写入 %s 失败: %v", jsonPath, err)
	}
	if err := os.WriteFile(readmePath, []byte(newReadme), 0644); err != nil {
		fatal("写入 %s 失败: %v", readmePath, err)
	}
	fmt.Printf("已生成 %s 与 %s 快速上手章节\n", jsonPath, readmePath)
}

func validate(q *QuickStart) error {
	if q.Version != 1 {
		return fmt.Errorf("不支持的 version=%d", q.Version)
	}
	for name, l := range map[string]Localized{
		"title": q.Title, "summary": q.Summary,
	} {
		if strings.TrimSpace(l.Zh) == "" || strings.TrimSpace(l.En) == "" {
			return fmt.Errorf("%s 缺少中文或英文", name)
		}
	}
	for _, key := range []string{"linux", "windows"} {
		sys, ok := q.Systems[key]
		if !ok {
			return fmt.Errorf("缺少 systems.%s", key)
		}
		if strings.TrimSpace(sys.Label.Zh) == "" || strings.TrimSpace(sys.Label.En) == "" {
			return fmt.Errorf("systems.%s.label 缺少中英文", key)
		}
		if len(sys.Steps) == 0 {
			return fmt.Errorf("systems.%s.steps 为空", key)
		}
		for i, st := range sys.Steps {
			where := fmt.Sprintf("systems.%s.steps[%d]", key, i)
			if strings.TrimSpace(st.Title.Zh) == "" || strings.TrimSpace(st.Title.En) == "" {
				return fmt.Errorf("%s.title 缺少中英文", where)
			}
			if len(st.Lines.Zh) == 0 || len(st.Lines.En) == 0 {
				return fmt.Errorf("%s.lines 缺少中英文", where)
			}
			if st.Code.Zh != "" && st.Code.En == "" {
				return fmt.Errorf("%s.code 缺少英文", where)
			}
		}
	}
	if len(q.Intro.Zh) == 0 || len(q.Intro.En) == 0 {
		return fmt.Errorf("intro 缺少中英文")
	}
	return nil
}

func replaceSection(readme, section string) (string, error) {
	start := strings.Index(readme, startMarker)
	if start < 0 {
		return "", fmt.Errorf("README.md 缺少 %s", startMarker)
	}
	end := strings.Index(readme[start:], endMarker)
	if end < 0 {
		return "", fmt.Errorf("README.md 缺少 %s", endMarker)
	}
	end += start + len(endMarker)
	return readme[:start] + section + readme[end:], nil
}

// placeholderRe 匹配 <控制台>/<令牌>/<版本> 等占位符。
var placeholderRe = regexp.MustCompile(`<[^<>\s]{1,40}>`)

// mdInline 对正文做 Markdown 安全处理：给占位符加反引号，避免被当作 HTML 标签。
func mdInline(s string) string {
	return placeholderRe.ReplaceAllString(s, "`$0`")
}

func renderReadme(q *QuickStart) string {
	var b strings.Builder
	b.WriteString(startMarker + "\n")
	b.WriteString(generatedNote + "\n\n")

	// 中文
	b.WriteString("## " + q.Title.Zh + "\n\n")
	b.WriteString("> 约定：\n")
	for _, l := range q.Intro.Zh {
		b.WriteString("> - " + mdInline(l) + "\n")
	}
	b.WriteString("\n")
	renderSystems(&b, q, "zh")

	b.WriteString("> " + mdInline(strings.Join(q.Footer.Zh, "\n> ")) + "\n\n")

	// English
	b.WriteString("## " + q.Title.En + "\n\n")
	b.WriteString("> Conventions:\n")
	for _, l := range q.Intro.En {
		b.WriteString("> - " + mdInline(l) + "\n")
	}
	b.WriteString("\n")
	renderSystems(&b, q, "en")
	b.WriteString("> " + mdInline(strings.Join(q.Footer.En, "\n> ")) + "\n")
	b.WriteString(endMarker)
	return b.String()
}

func renderSystems(b *strings.Builder, q *QuickStart, lang string) {
	for _, key := range []string{"linux", "windows"} {
		sys := q.Systems[key]
		b.WriteString("### " + pick(sys.Label, lang) + "\n\n")
		for _, st := range sys.Steps {
			b.WriteString("**" + st.No + ". " + pick(st.Title, lang) + "**\n\n")
			for _, line := range linesOf(st.Lines, lang) {
				b.WriteString("- " + mdInline(line) + "\n")
			}
			b.WriteString("\n")
			code := pick(st.Code, lang)
			if strings.TrimSpace(code) != "" {
				b.WriteString("```" + st.CodeLang + "\n")
				b.WriteString(strings.TrimRight(code, "\n") + "\n")
				b.WriteString("```\n\n")
			}
		}
	}
}

func pick(l Localized, lang string) string {
	if lang == "en" {
		return l.En
	}
	return l.Zh
}

func linesOf(l LocalizedList, lang string) []string {
	if lang == "en" {
		return l.En
	}
	return l.Zh
}

func fileEquals(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	return err == nil && bytes.Equal(got, want)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
