package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// bridgeTargets stdio 桥支持的 os/arch 组合（与 Makefile bridge 目标一致）。
var bridgeTargets = []struct{ OS, Arch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// bridgeFileName 按配置前缀生成 stdio 桥文件名：<prefix>-<os>-<arch>[.exe]。
func bridgeFileName(prefix, osName, arch string) string {
	name := prefix + "-" + osName + "-" + arch
	if osName == "windows" {
		name += ".exe"
	}
	return name
}

// validBridgeName 只接受「配置前缀 + 白名单 os/arch」组成的文件名，防路径穿越。
func validBridgeName(prefix, name string) bool {
	if prefix == "" || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, t := range bridgeTargets {
		if name == bridgeFileName(prefix, t.OS, t.Arch) {
			return true
		}
	}
	return false
}

// DownloadBridge 下载 stdio 桥二进制：GET /api/ai/bridge/{name}。
//
// 文件放在 server.agent_binary_dir（与 Agent 二进制同目录），文件名前缀取 ai.bridge_binary_prefix。
// 接受登录会话或 AI 令牌（命令行下载用）。
func DownloadBridge(s *svc.ServiceContext) http.HandlerFunc {
	return requireSessionOrAIToken(s, func(w http.ResponseWriter, r *http.Request) {
		snap := s.Settings.Snapshot()
		name := r.PathValue("name")
		if !validBridgeName(snap.AI.BridgeBinaryPrefix, name) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(snap.Server.AgentBinaryDir, name)
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			http.Error(w, "stdio 桥二进制不存在：请将 "+name+" 放入 Agent 二进制目录", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		http.ServeFile(w, r, path)
	})
}

// bridgeAvailability 返回各平台 stdio 桥是否已放置（页面据此提示）。
func bridgeAvailability(s *svc.ServiceContext) []map[string]any {
	snap := s.Settings.Snapshot()
	out := make([]map[string]any, 0, len(bridgeTargets))
	for _, t := range bridgeTargets {
		name := bridgeFileName(snap.AI.BridgeBinaryPrefix, t.OS, t.Arch)
		out = append(out, map[string]any{
			"os": t.OS, "arch": t.Arch, "file": name,
			"available": bridgeFileAvailable(snap, name),
		})
	}
	return out
}

func bridgeFileAvailable(snap config.Config, file string) bool {
	st, err := os.Stat(filepath.Join(snap.Server.AgentBinaryDir, file))
	return err == nil && !st.IsDir()
}
