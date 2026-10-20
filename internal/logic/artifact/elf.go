package artifact

import (
	"debug/elf"
	"fmt"
)

// elfArchMap Go GOARCH → ELF machine
var elfArchMap = map[string]elf.Machine{
	"amd64": elf.EM_X86_64,
	"386":   elf.EM_386,
	"arm64": elf.EM_AARCH64,
	"arm":   elf.EM_ARM,
}

// checkELF 校验 ELF 二进制的架构与静态链接。
//
// 返回告警字符串（空表示无问题）。非 ELF 文件（如 tar.gz 压缩包）跳过校验。
func checkELF(path, arch string) string {
	f, err := elf.Open(path)
	if err != nil {
		// 非 ELF（可能是压缩包），不校验
		return ""
	}
	defer f.Close()

	// 架构匹配
	if want, ok := elfArchMap[arch]; ok {
		if f.Machine != want {
			return fmt.Sprintf("⚠️ELF架构不匹配: 声明 arch=%s 期望 %v, 实际 %v", arch, want, f.Machine)
		}
	}

	// 静态链接检查：动态可执行文件含 INTERP 段（动态链接器路径）
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return "⚠️非静态二进制(含动态链接器)，在旧系统(如CentOS6)可能因glibc缺失无法运行，建议 CGO_ENABLED=0 静态编译"
		}
	}
	return ""
}
