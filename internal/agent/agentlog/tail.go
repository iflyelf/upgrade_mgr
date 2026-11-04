package agentlog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Tail 输出日志文件内容；follow=true 时持续跟随新增内容。
//
// lines 为启动时显示的历史行数（<=0 表示不显示历史，只跟随新内容）。
// 兼容 Agent 内部的 copytruncate 切割（检测到文件变小后从头读取）
// 以及外部 rename 轮转（检测到路径指向新文件后重新打开）。
func Tail(ctx context.Context, path string, lines int, follow bool, out io.Writer) error {
	if lines > 0 {
		if err := printLastLines(path, lines, out); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	// 闭包捕获变量 f：外部轮转后 f 会被替换，退出时关闭当前文件。
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	// fp 保存已读内容末尾最多 64 字节，用于识别 copytruncate：
	// 日志被截断并写入更长的新内容时，文件大小可能仍大于旧 offset，
	// 仅靠 size 无法发现；回读一小段比对即可。
	fp := make([]byte, 0, 64)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if f == nil {
			nf, oerr := os.Open(path)
			if oerr != nil {
				if !sleepCtx(ctx, 500*time.Millisecond) {
					return nil
				}
				continue
			}
			f = nf
			offset, _ = f.Seek(0, io.SeekStart)
			fp = fp[:0]
		}

		// 截断/轮转检测：回读 offset 前的指纹，不一致则从头读。
		if len(fp) > 0 && offset >= int64(len(fp)) {
			probe := make([]byte, len(fp))
			n, perr := f.ReadAt(probe, offset-int64(len(fp)))
			if perr != nil || n != len(probe) || !bytes.Equal(probe, fp) {
				if _, serr := f.Seek(0, io.SeekStart); serr == nil {
					offset = 0
					fp = fp[:0]
					continue
				}
			}
		}

		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			offset += int64(n)
			fp = append(fp, buf[:n]...)
			if len(fp) > 64 {
				fp = append(fp[:0], fp[len(fp)-64:]...)
			}
			continue
		}
		if rerr != nil && rerr != io.EOF {
			// 文件被删除或临时不可读：关闭后稍后重试。
			_ = f.Close()
			f = nil
			fp = fp[:0]
			if !sleepCtx(ctx, 500*time.Millisecond) {
				return nil
			}
			continue
		}

		// copytruncate：文件变小说明已切割，从头读取新内容。
		if fi, serr := f.Stat(); serr == nil && fi.Size() < offset {
			if _, serr := f.Seek(0, io.SeekStart); serr == nil {
				offset = 0
				fp = fp[:0]
				continue
			}
		}
		// 外部 rename 轮转：路径已指向新文件，关闭后重新打开。
		if cur, serr := os.Stat(path); serr == nil {
			if fi, ferr := f.Stat(); ferr == nil && !os.SameFile(fi, cur) {
				_ = f.Close()
				f = nil
				fp = fp[:0]
				continue
			}
		}
		if !sleepCtx(ctx, 500*time.Millisecond) {
			return nil
		}
	}
}

// printLastLines 输出文件最后 n 行。
//
// 日志已限制为 10MB 量级，整文件读取实现简单且不会漏行；
// 若日志文件不存在返回可读错误，便于 CLI 提示。
func printLastLines(path string, n int, out io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取日志文件失败: %w", err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	all := strings.Split(text, "\n")
	if n < len(all) {
		all = all[len(all)-n:]
	}
	if _, err := io.WriteString(out, strings.Join(all, "\n")+"\n"); err != nil {
		return err
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
