// Package downloader 制品下载器：Go Range 续传+重试+md5，wget 兵底。
package downloader

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Spec 下载规格
type Spec struct {
	URL        string
	Dest       string
	MD5        string // 可选，兼容旧制品
	SHA256     string // 推荐，优先校验
	MaxRetries int
	Timeout    time.Duration
	UseWget    bool // 强制 wget 兜底
	// AuthHeader 可选的 Authorization 头值（如 "Bearer <agent_id>:<token>"）。
	// 制品下载端点要求认证，避免二进制被匿名拖取。
	AuthHeader string
}

// Download 下载文件，支持 Range 续传与重试；成功后校验 md5。
func Download(ctx context.Context, spec Spec) error {
	if spec.MaxRetries <= 0 {
		spec.MaxRetries = 3
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	// 续传前先校验已有文件：
	//   - 完整且校验和匹配 → 直接复用，避免对已下载完成的文件发 Range（会 416）；
	//   - 校验和不匹配 → 删除，避免带着旧版本的残留文件继续续传。
	// 这一步能显著减少“反复卡在下载”的概率（例如进程重启后再次执行同版本升级）。
	if fi, err := os.Stat(spec.Dest); err == nil && fi.Size() > 0 {
		if spec.SHA256 != "" || spec.MD5 != "" {
			if verifyChecksum(spec.Dest, spec.MD5, spec.SHA256) == nil {
				return nil
			}
			_ = os.Remove(spec.Dest)
		}
	}

	if spec.UseWget || !goDownloadSupported() {
		return downloadWget(ctx, spec)
	}

	var lastErr error
	for attempt := 1; attempt <= spec.MaxRetries; attempt++ {
		if err := downloadGo(ctx, spec); err != nil {
			lastErr = err
			if attempt < spec.MaxRetries {
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
				continue
			}
			return fmt.Errorf("下载失败(尝试%d次): %w", spec.MaxRetries, err)
		}
		if err := verifyChecksum(spec.Dest, spec.MD5, spec.SHA256); err != nil {
			// 校验失败可能是续传到了旧版本残留内容。删除后重新完整下载，
			// 而不是直接失败等人工重试。
			_ = os.Remove(spec.Dest)
			lastErr = err
			if attempt < spec.MaxRetries {
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
				continue
			}
			return fmt.Errorf("下载校验失败(尝试%d次): %w", spec.MaxRetries, err)
		}
		return nil
	}
	return lastErr
}

func downloadGo(ctx context.Context, spec Spec) error {
	fi, _ := os.Stat(spec.Dest)
	offset := int64(0)
	if fi != nil {
		offset = fi.Size()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", spec.URL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	if spec.AuthHeader != "" {
		req.Header.Set("Authorization", spec.AuthHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 416：本地残留文件比服务端文件还大（例如同名制品已换版本、上次下载中断在半截），
	// 继续用同一个 Range 重试只会永远 416。清理残留后由外层重试完整下载。
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if err := os.Remove(spec.Dest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("HTTP 416，且清理残留文件失败: %w", err)
		}
		return fmt.Errorf("HTTP 416（本地残留文件 %d 字节，已清理并将重新下载）", offset)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	if err := os.MkdirAll(filepath.Dir(spec.Dest), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(spec.Dest, flag, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadWget(ctx context.Context, spec Spec) error {
	// wget -c -O dest -t max_retries url
	args := []string{"-c", "-O", spec.Dest, "-t", fmt.Sprintf("%d", spec.MaxRetries)}
	if spec.AuthHeader != "" {
		args = append(args, "--header=Authorization: "+spec.AuthHeader)
	}
	// "--" 终止选项解析：防止 URL 以 "-" 开头被 wget 当作选项（参数注入）
	args = append(args, "--", spec.URL)
	cmd := exec.CommandContext(ctx, "wget", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wget: %w", err)
	}
	return verifyChecksum(spec.Dest, spec.MD5, spec.SHA256)
}

// verifyChecksum 优先校验 SHA256（更安全），若无则回退到 MD5（兼容旧制品）。
func verifyChecksum(path, md5sum, sha256sum string) error {
	if sha256sum != "" {
		return verifySHA256(path, sha256sum)
	}
	if md5sum != "" {
		return verifyMD5(path, md5sum)
	}
	return nil // 两者都无则跳过（不推荐，但兼容无校验和的情况）
}

func verifySHA256(path, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 不匹配: want=%s got=%s", want, got)
	}
	return nil
}

func verifyMD5(path, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("md5 不匹配: want=%s got=%s", want, got)
	}
	return nil
}

func goDownloadSupported() bool { return true }
