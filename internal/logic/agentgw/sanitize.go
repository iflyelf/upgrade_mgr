package agentgw

import (
	"strings"
	"unicode/utf8"
)

const (
	// 单条阶段详情上限，避免脚本输出把指令日志撑爆。
	maxStageMessageBytes = 4 * 1024
	// 最终结果日志上限。PostgreSQL 的 TEXT 可存更大，但 Agent 上报不应无限增长。
	maxReportLogBytes = 128 * 1024
	// 指令状态/阶段等短字段上限（与数据库列宽保持一致）。
	maxStageBytes     = 32
	maxVersionBytes   = 64
	maxReasonBytes    = 300
	maxHostIPLogBytes = 8 * 1024
)

// sanitizeLog 清理要写入 PostgreSQL 的日志。
//
// PostgreSQL 的 TEXT/JSONB 都不接受 NUL（\x00），而脚本输出、二进制文件
// 片段或异常 stderr 中完全可能出现 NUL。旧实现直接把 req.Log 拼进 SQL，
// 一旦包含 NUL，整个结果上报事务会失败；Agent 又忽略错误，最终指令和任务
// 会永久停在 running。这里统一：
//   - 移除 NUL；
//   - 把非法 UTF-8 替换为 U+FFFD；
//   - 去掉除换行/回车/制表符外的控制字符（含 ANSI ESC）。
func sanitizeLog(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	hasControl := false
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			hasControl = true
			break
		}
	}
	if !hasControl {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sanitizeAndTruncate 清洗文本并按字节上限截断（保留开头，避免破坏 UTF-8）。
func sanitizeAndTruncate(s string, maxBytes int) string {
	s = sanitizeLog(s)
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return truncateUTF8(s, maxBytes)
}

// truncateUTF8 按字节上限截断，避免把多字节字符切成非法 UTF-8。
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// appendLog 拼接日志，并在单条日志过长时保留尾部。
//
// 阶段日志通常很短，但脚本输出可能一次几 MB；保留尾部比保留开头更有用
// （失败原因一般在最后）。超长时插入截断提示。
func appendLog(current, line string, maxBytes int) string {
	if line == "" {
		return current
	}
	combined := current + line
	if maxBytes <= 0 || len(combined) <= maxBytes {
		return combined
	}
	tail := tailUTF8(combined, maxBytes)
	const note = "…（日志过长，已截断）…\n"
	if len(note) >= len(tail) {
		return tail
	}
	return note + tail
}

// tailUTF8 保留字符串尾部 maxBytes 字节，并保证从合法 rune 边界开始。
func tailUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// normalizeVersion 清洗版本号并截断到数据库列宽（VARCHAR(64)）。
// 版本命令输出可能是多行或超长文本，直接写库会触发 value too long，
// 导致最终结果上报失败、任务卡在 running。
func normalizeVersion(s string) string {
	s = strings.TrimSpace(sanitizeLog(s))
	if len(s) <= maxVersionBytes {
		return s
	}
	return truncateUTF8(s, maxVersionBytes)
}

// normalizeStage 清洗阶段名并截断到 VARCHAR(32)。
func normalizeStage(s string) string {
	s = strings.TrimSpace(sanitizeLog(s))
	if len(s) <= maxStageBytes {
		return s
	}
	return truncateUTF8(s, maxStageBytes)
}
