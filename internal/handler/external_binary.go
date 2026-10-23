package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// 外部二进制白名单：由控制台代为下载并缓存，供老系统（无法 TLS 1.2）通过 HTTP 获取。
//
// 背景：Windows Server 2008 R2 自带的 PowerShell 2.0 / .NET 2.0/3.5 不支持 TLS 1.2，
// 主机侧无法直连 GitHub。控制台（Go）支持 TLS 1.2，可代为下载后由主机用 http 拉取。
type externalAsset struct {
	URL  string
	Exec bool // true=PE 可执行文件（校验 MZ），false=文本脚本
}

var externalBinaries = map[string]externalAsset{
	"windows-exporter-amd64": {
		URL:  "https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64.exe",
		Exec: true,
	},
	"windows-exporter-arm64": {
		URL:  "https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-arm64.exe",
		Exec: true,
	},
	// 安装脚本也由平台代缓存：Windows 2008 R2 的 PowerShell 2.0 无法用 TLS 1.2
	// 直连 GitHub，但可以 http 访问平台。
	"windows-exporter-install.ps1": {
		URL:  "https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/windows_exporter/install.ps1",
		Exec: false,
	},
}

// externalBinaryMaxSize 单个文件上限，防止异常响应把磁盘写满。
const externalBinaryMaxSize = 200 << 20

// externalBinaryTTL 缓存有效期：过期后下次请求重新下载。
const externalBinaryTTL = 24 * time.Hour

// externalBinaryClient 下载外部二进制使用的客户端（不跟随无限重定向）。
var externalBinaryClient = &http.Client{Timeout: 10 * time.Minute}

// ExternalBinaryDownload 下载白名单外部二进制（无鉴权，内容为公开文件）。
//
// 缓存目录：<agent_binary_dir>/.external-cache。首次请求下载并落盘，
// 后续直接命中缓存；文件大小与内容异常（HTML 错误页）会返回 502。
func ExternalBinaryDownload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
		key := strings.TrimSuffix(name, ".exe")
		asset, ok := externalBinaries[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		source := asset.URL

		dir := s.Config.Server.AgentBinaryDir
		if dir == "" {
			dir = "./dist"
		}
		cacheDir := filepath.Join(dir, ".external-cache")
		target := filepath.Join(cacheDir, key+".exe")
		if err := safepath.EnsureWithin(dir, target); err != nil {
			http.NotFound(w, r)
			return
		}

		if !freshFile(target, externalBinaryTTL) {
			if err := fetchExternalBinary(r.Context(), source, target, asset.Exec); err != nil {
				log.Printf("⚠️ 下载外部二进制失败 %s: %v", name, err)
				httpx.Fail(w, http.StatusBadGateway,
					"控制台下载外部二进制失败（请确认控制台能访问 GitHub）: "+err.Error())
				return
			}
		}

		f, err := os.Open(target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		contentType := "application/octet-stream"
		if !asset.Exec {
			contentType = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "attachment; filename="+key+".exe")
		http.ServeContent(w, r, key+".exe", info.ModTime(), f)
	}
}

// freshFile 判断缓存文件存在且未过期。
func freshFile(path string, ttl time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	return time.Since(info.ModTime()) < ttl
}

// fetchExternalBinary 下载到临时文件再原子替换，避免并发/中断留下半个文件。
func fetchExternalBinary(ctx context.Context, source, target string, executable bool) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "upgrade-mgr/external-binary-cache")
	resp, err := externalBinaryClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("源站返回 HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	limited := io.LimitReader(resp.Body, externalBinaryMaxSize+1)
	n, copyErr := io.Copy(tmp, limited)
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > externalBinaryMaxSize {
		return fmt.Errorf("文件超过上限 %d 字节", externalBinaryMaxSize)
	}
	// 内容校验：可执行文件要求 MZ 头；脚本要求非 HTML。
	head := make([]byte, 64)
	var hn int
	if f, err := os.Open(tmpPath); err == nil {
		hn, _ = f.Read(head)
		f.Close()
	}
	sample := strings.TrimSpace(strings.ToLower(string(head[:hn])))
	if executable {
		if !strings.HasPrefix(sample, "mz") {
			return fmt.Errorf("下载内容不是 Windows 可执行文件（可能是错误页）")
		}
	} else {
		for _, prefix := range []string{"<!doctype", "<html", "<head", "<body", "<?xml"} {
			if strings.HasPrefix(sample, prefix) {
				return fmt.Errorf("下载内容不是脚本，而是 HTML/XML 页面（可能是错误页）")
			}
		}
	}
	return os.Rename(tmpPath, target)
}
