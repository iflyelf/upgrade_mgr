package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
	"github.com/iflyelf/upgrade_mgr/skills"
)

// snippetClient 支持生成接入配置的 AI 客户端。
//
// 这里只描述各客户端自身的接入方式（命令格式、配置文件位置），属于客户端事实；
// 服务名、地址、令牌环境变量、Skills 目录、桥文件名全部来自设置。
type snippetClient struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// SkillClient 对应 ai.skill_install_dirs 中的客户端名（为空表示不安装 Skill）。
	SkillClient string `json:"skill_client"`
}

var snippetClients = []snippetClient{
	{ID: "opencode", Label: "OpenCode", SkillClient: "opencode"},
	{ID: "claude", Label: "Claude Code", SkillClient: "claude"},
	{ID: "codex", Label: "Codex", SkillClient: "codex"},
	{ID: "cursor", Label: "Cursor"},
	{ID: "bridge", Label: "stdio 桥（通用）"},
}

var snippetOSes = []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}{{"linux", "Linux"}, {"darwin", "macOS"}, {"windows", "Windows（PowerShell）"}}

// tokenCharset 令牌只允许这些字符（前缀已由设置校验，随机部分为 hex），嵌入命令前校验。
var tokenCharset = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)

type snippet struct {
	Title   string `json:"title"`
	Lang    string `json:"lang"`
	Content string `json:"content"`
	Note    string `json:"note,omitempty"`
}

type snippetData struct {
	ServerName  string
	MCPURL      string
	BaseURL     string
	TokenEnv    string
	Token       string // 为空时使用占位符
	OS          string
	Arch        string
	BridgeFile  string
	SkillName   string
	SkillDir    string
	Placeholder string
}

// AISnippets 生成 AI 客户端接入命令：POST /api/ai/snippets。
//
// 请求：{"client","os","arch","token"}。token 可选（页面「生成接入命令」时先创建令牌再传入）；
// 不传时以占位符代替，令牌永不写入服务端日志。
func AISnippets(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := auth.FromContext(r.Context()); !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req struct {
			Client string `json:"client"`
			OS     string `json:"os"`
			Arch   string `json:"arch"`
			Token  string `json:"token"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		client, ok := findSnippetClient(req.Client)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "不支持的客户端类型")
			return
		}
		if !validSnippetTarget(req.OS, req.Arch) {
			httpx.Fail(w, http.StatusBadRequest, "不支持的操作系统或架构")
			return
		}
		req.Token = strings.TrimSpace(req.Token)
		if req.Token != "" && !tokenCharset.MatchString(req.Token) {
			httpx.Fail(w, http.StatusBadRequest, "令牌格式错误")
			return
		}
		snap := s.Settings.Snapshot()
		base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		d := snippetData{
			ServerName: snap.AI.MCPServerName, MCPURL: base + mcpPath, BaseURL: base,
			TokenEnv: snap.AI.ClientTokenEnv, Token: req.Token, OS: req.OS, Arch: req.Arch,
			BridgeFile:  bridgeFileName(snap.AI.BridgeBinaryPrefix, req.OS, req.Arch),
			Placeholder: "<粘贴 AI 访问令牌>",
		}
		if client.SkillClient != "" {
			d.SkillName, d.SkillDir = skillTarget(snap.AI, client.SkillClient)
		}
		out := buildSnippets(client.ID, d)
		resp := map[string]any{
			"client": client, "os": req.OS, "arch": req.Arch,
			"mcp_url": d.MCPURL, "server_name": d.ServerName, "token_env": d.TokenEnv,
			"token_embedded": req.Token != "", "snippets": out,
		}
		if client.ID == "bridge" {
			resp["bridge_file"] = d.BridgeFile
			resp["bridge_available"] = bridgeFileAvailable(snap, d.BridgeFile)
		}
		httpx.OK(w, resp)
	}
}

func findSnippetClient(id string) (snippetClient, bool) {
	for _, c := range snippetClients {
		if c.ID == id {
			return c, true
		}
	}
	return snippetClient{}, false
}

func validSnippetTarget(osName, arch string) bool {
	for _, t := range bridgeTargets {
		if t.OS == osName && t.Arch == arch {
			return true
		}
	}
	return false
}

// skillTarget 返回第一个内嵌 Skill 名与该客户端的安装目录（未配置目录时返回空）。
func skillTarget(c config.AIConfig, client string) (string, string) {
	names := skills.Names()
	if len(names) == 0 {
		return "", ""
	}
	dirs, _ := config.ParseSkillInstallDirs(c.SkillInstallDirs)
	for _, d := range dirs {
		if d.Client == client {
			return names[0], d.Dir
		}
	}
	return "", ""
}

// snippetMeta 页面渲染生成器所需的候选项（客户端 / 系统 / 架构），不在前端写死。
func snippetMeta() map[string]any {
	arches := map[string][]string{}
	for _, t := range bridgeTargets {
		arches[t.OS] = append(arches[t.OS], t.Arch)
	}
	return map[string]any{"clients": snippetClients, "oses": snippetOSes, "arches": arches}
}

func posixQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func psQuoteStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (d snippetData) win() bool { return d.OS == "windows" }

func (d snippetData) shell() string {
	if d.win() {
		return "powershell"
	}
	return "bash"
}

// tokenValue 嵌入命令的令牌（或占位符）。
func (d snippetData) tokenValue() string {
	if d.Token != "" {
		return d.Token
	}
	return d.Placeholder
}

// envRef 当前 shell 中引用令牌环境变量的写法。
func (d snippetData) envRef() string {
	if d.win() {
		return "$env:" + d.TokenEnv
	}
	return "$" + d.TokenEnv
}

func (d snippetData) envStep() snippet {
	content := "export " + d.TokenEnv + "=" + posixQuote(d.tokenValue())
	note := "建议写入 ~/.bashrc / ~/.zshrc 等 shell 配置，令牌不要写进项目文件或提交到仓库。"
	if d.win() {
		content = "[Environment]::SetEnvironmentVariable(" + psQuoteStr(d.TokenEnv) + ", " +
			psQuoteStr(d.tokenValue()) + ", 'User')\n$env:" + d.TokenEnv + " = " + psQuoteStr(d.tokenValue())
		note = "第一行持久化到当前用户环境变量（新开终端生效），第二行让当前终端立即可用。"
	}
	return snippet{Title: "设置令牌环境变量 " + d.TokenEnv, Lang: d.shell(), Content: content, Note: note}
}

// skillStep 下载并解压 Skill 到客户端 skills 目录（目录取自 ai.skill_install_dirs）。
func (d snippetData) skillStep() *snippet {
	if d.SkillName == "" || d.SkillDir == "" {
		return nil
	}
	archive := d.BaseURL + "/api/ai/skills/" + d.SkillName + "/archive"
	content := "mkdir -p " + posixQuote(d.SkillDir) + " && curl -fsSL -H \"Authorization: Bearer " + d.envRef() + "\" \\\n  " +
		posixQuote(archive) + " | tar -xzf - -C " + posixQuote(d.SkillDir)
	if d.win() {
		tmp := "$env:TEMP\\" + d.SkillName + ".tar.gz"
		content = "New-Item -ItemType Directory -Force -Path " + psQuoteStr(d.SkillDir) + " | Out-Null\n" +
			"Invoke-WebRequest -UseBasicParsing -Headers @{ Authorization = \"Bearer " + d.envRef() + "\" } `\n  -Uri " +
			psQuoteStr(archive) + " -OutFile \"" + tmp + "\"\n" +
			"tar -xzf \"" + tmp + "\" -C " + psQuoteStr(d.SkillDir)
	}
	return &snippet{Title: "安装 Skill " + d.SkillName + "（项目目录下执行）", Lang: d.shell(), Content: content,
		Note: "解压到 " + d.SkillDir + "/" + d.SkillName + "/SKILL.md；内容已按当前服务名与地址渲染。"}
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func buildSnippets(client string, d snippetData) []snippet {
	out := []snippet{d.envStep()}
	switch client {
	case "opencode":
		cfg := map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": map[string]any{
			d.ServerName: map[string]any{
				"type": "remote", "url": d.MCPURL, "enabled": true, "oauth": false,
				"headers": map[string]string{"Authorization": "Bearer {env:" + d.TokenEnv + "}"},
			}}}
		out = append(out, snippet{Title: "写入 OpenCode 配置（项目根目录 opencode.json，或全局 ~/.config/opencode/opencode.json）",
			Lang: "json", Content: mustJSON(cfg), Note: "已有配置时只合并 mcp 段；{env:" + d.TokenEnv + "} 由 OpenCode 读取环境变量。验证：opencode mcp list"})
	case "claude":
		cmd := "claude mcp add --transport http --scope user " + posixQuote(d.ServerName) + " " + posixQuote(d.MCPURL) +
			" \\\n  --header \"Authorization: Bearer " + d.envRef() + "\""
		if d.win() {
			cmd = "claude mcp add --transport http --scope user " + psQuoteStr(d.ServerName) + " " + psQuoteStr(d.MCPURL) +
				" `\n  --header \"Authorization: Bearer " + d.envRef() + "\""
		}
		out = append(out, snippet{Title: "添加 MCP 服务器", Lang: d.shell(), Content: cmd,
			Note: "--scope user 对当前用户所有项目生效，改为 project 则写入项目 .mcp.json。验证：claude mcp list"})
	case "codex":
		cmd := "codex mcp add " + posixQuote(d.ServerName) + " --url " + posixQuote(d.MCPURL) +
			" --bearer-token-env-var " + d.TokenEnv
		if d.win() {
			cmd = "codex mcp add " + psQuoteStr(d.ServerName) + " --url " + psQuoteStr(d.MCPURL) +
				" --bearer-token-env-var " + d.TokenEnv
		}
		toml := fmt.Sprintf("[mcp_servers.%q]\nurl = %q\nbearer_token_env_var = %q\n", d.ServerName, d.MCPURL, d.TokenEnv)
		out = append(out,
			snippet{Title: "添加 MCP 服务器", Lang: d.shell(), Content: cmd, Note: "验证：codex mcp list"},
			snippet{Title: "或手工写入 ~/.codex/config.toml", Lang: "toml", Content: toml})
	case "cursor":
		cfg := map[string]any{"mcpServers": map[string]any{d.ServerName: map[string]any{
			"url": d.MCPURL, "headers": map[string]string{"Authorization": "Bearer ${env:" + d.TokenEnv + "}"},
		}}}
		out = append(out, snippet{Title: "写入 Cursor 配置（全局 ~/.cursor/mcp.json，或项目 .cursor/mcp.json）",
			Lang: "json", Content: mustJSON(cfg), Note: "已有配置时只合并 mcpServers 段，保存后在 Cursor 设置 → MCP 中刷新。"})
	case "bridge":
		bin := "./" + d.BridgeFile
		dl := "curl -fSL -H \"Authorization: Bearer " + d.envRef() + "\" \\\n  " +
			posixQuote(d.BaseURL+"/api/ai/bridge/"+d.BridgeFile) + " -o " + posixQuote(d.BridgeFile) +
			" && chmod +x " + posixQuote(d.BridgeFile)
		if d.win() {
			bin = ".\\" + d.BridgeFile
			dl = "Invoke-WebRequest -UseBasicParsing -Headers @{ Authorization = \"Bearer " + d.envRef() + "\" } `\n  -Uri " +
				psQuoteStr(d.BaseURL+"/api/ai/bridge/"+d.BridgeFile) + " -OutFile " + psQuoteStr(d.BridgeFile)
		}
		cfg := map[string]any{"mcpServers": map[string]any{d.ServerName: map[string]any{
			"command": "<桥的绝对路径>/" + d.BridgeFile,
			"args":    []string{"-url", d.MCPURL, "-token-env", d.TokenEnv},
		}}}
		out = append(out,
			snippet{Title: "下载 stdio 桥", Lang: d.shell(), Content: dl},
			snippet{Title: "在支持 stdio MCP 的客户端中配置", Lang: "json", Content: mustJSON(cfg),
				Note: "桥从环境变量 " + d.TokenEnv + " 读取令牌；客户端启动时需能继承该环境变量。自测：echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}' | " +
					bin + " -url " + d.MCPURL + " -token-env " + d.TokenEnv})
	}
	if st := d.skillStep(); st != nil {
		out = append(out, *st)
	}
	return out
}
