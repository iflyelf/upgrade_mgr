// Package ipx 解析 IP 段表达式，支持灰度升级按 IP 段选择目标主机。
//
// 支持的写法（逗号分隔，可混合）：
//   - 单个 IP：           10.1.255.38
//   - 短范围：            10.1.255.24-26
//   - 完整范围：          10.1.26.5-10.1.26.7
//   - CIDR：              10.1.255.0/24
//
// 相比 consul_mgr 的实现，本包为升级平台增加了：
//   - 展开数量上限保护（防止 /8、/16 等误写导致内存暴涨）
//   - 独立的排除表达式（差集）
//   - 去重且保持稳定
package ipx

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// DefaultMaxExpand 默认单次展开的 IP 数量上限。
// /16 = 65534 台，作为常见批量上限；超过需显式放大或分批，避免误写 /8 打爆内存。
const DefaultMaxExpand = 65536

// Parse 解析 include 表达式为去重、排序后的 IP 列表，使用默认上限。
func Parse(include string) ([]string, error) {
	return ParseWithExclude(include, "", DefaultMaxExpand)
}

// ParseWithExclude 解析 include 表达式并从中排除 exclude 表达式命中的 IP。
//
// maxExpand <= 0 时使用 DefaultMaxExpand。返回的列表按 IP 数值升序、去重。
func ParseWithExclude(include, exclude string, maxExpand int) ([]string, error) {
	if maxExpand <= 0 {
		maxExpand = DefaultMaxExpand
	}

	incSet, err := expandToSet(include, maxExpand)
	if err != nil {
		return nil, err
	}
	if len(incSet) == 0 {
		return nil, fmt.Errorf("未能解析出任何 IP")
	}

	if strings.TrimSpace(exclude) != "" {
		excSet, err := expandToSet(exclude, maxExpand)
		if err != nil {
			return nil, fmt.Errorf("排除表达式解析失败: %w", err)
		}
		for ip := range excSet {
			delete(incSet, ip)
		}
	}

	out := make([]string, 0, len(incSet))
	for ip := range incSet {
		out = append(out, ip)
	}
	sortIPs(out)
	return out, nil
}

// expandToSet 将逗号分隔的表达式展开为 IP 集合，带数量上限保护。
func expandToSet(input string, maxExpand int) (map[string]struct{}, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("IP 表达式不能为空")
	}

	set := make(map[string]struct{})
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ips, err := expandPart(part)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			set[ip] = struct{}{}
			if len(set) > maxExpand {
				return nil, fmt.Errorf("展开的 IP 数量超过上限 %d，请缩小范围或分批", maxExpand)
			}
		}
	}
	return set, nil
}

// expandPart 展开单个片段（范围 / CIDR / 单 IP）。
func expandPart(part string) ([]string, error) {
	// 范围：a-b（短写 24-26 或完整 10.1.26.5-10.1.26.7）
	if strings.Contains(part, "-") {
		return expandRange(part, DefaultMaxExpand)
	}
	// CIDR
	if strings.Contains(part, "/") {
		return expandCIDR(part, DefaultMaxExpand)
	}
	// 单 IP
	if net.ParseIP(part) == nil {
		return nil, fmt.Errorf("非法 IP %q", part)
	}
	return []string{part}, nil
}

// expandCIDR 展开 CIDR，去掉网络地址与广播地址（与 python ipaddress.hosts() 行为一致）。
func expandCIDR(part string, maxExpand int) ([]string, error) {
	_, ipnet, err := net.ParseCIDR(part)
	if err != nil {
		return nil, fmt.Errorf("非法 CIDR %q: %w", part, err)
	}
	
	// 预先计算数量，避免 /8 等大段直接展开导致 OOM
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	if hostBits > 30 {
		// 超过 2^30 (1B+) 的理论容量，肯定超限
		return nil, fmt.Errorf("CIDR %q 范围过大（/%d），展开数量将超过上限 %d", part, ones, maxExpand)
	}
	count := 1 << uint(hostBits) // 2^hostBits
	// 前缀 < /31 时去掉网络地址与广播地址
	if hostBits > 1 {
		count -= 2
	}
	if count > maxExpand {
		return nil, fmt.Errorf("CIDR %q 展开后将产生 %d 个 IP，超过上限 %d", part, count, maxExpand)
	}

	var ips []string
	for cur := cloneIP(ipnet.IP.Mask(ipnet.Mask)); ipnet.Contains(cur); cur = nextIP(cur) {
		ips = append(ips, cur.String())
	}
	// 前缀 < /31 时去掉网络地址与广播地址
	if hostBits > 1 && len(ips) > 2 {
		ips = ips[1 : len(ips)-1]
	}
	return ips, nil
}

// expandRange 展开 IP 范围，仅支持 IPv4。
func expandRange(part string, maxExpand int) ([]string, error) {
	segs := strings.SplitN(part, "-", 2)
	startStr := strings.TrimSpace(segs[0])
	endStr := strings.TrimSpace(segs[1])

	startIP := net.ParseIP(startStr)
	if startIP == nil {
		return nil, fmt.Errorf("非法起始 IP %q", startStr)
	}
	start4 := startIP.To4()
	if start4 == nil {
		return nil, fmt.Errorf("仅支持 IPv4 范围: %q", part)
	}

	var end4 net.IP
	if full := net.ParseIP(endStr); full != nil {
		end4 = full.To4()
		if end4 == nil {
			return nil, fmt.Errorf("仅支持 IPv4 范围: %q", part)
		}
	} else {
		// 短写：仅最后一段，如 10.1.255.24-26
		last, err := strconv.Atoi(endStr)
		if err != nil || last < 0 || last > 255 {
			return nil, fmt.Errorf("非法结束 IP %q", endStr)
		}
		end4 = net.IPv4(start4[0], start4[1], start4[2], byte(last)).To4()
	}

	if compareIP(start4, end4) > 0 {
		return nil, fmt.Errorf("起始 IP 大于结束 IP: %q", part)
	}

	// 预先计算范围大小（IPv4 按整数差）
	startInt := int64(start4[0])<<24 | int64(start4[1])<<16 | int64(start4[2])<<8 | int64(start4[3])
	endInt := int64(end4[0])<<24 | int64(end4[1])<<16 | int64(end4[2])<<8 | int64(end4[3])
	count := endInt - startInt + 1
	if count > int64(maxExpand) {
		return nil, fmt.Errorf("范围 %q 将产生 %d 个 IP，超过上限 %d", part, count, maxExpand)
	}

	var ips []string
	for cur := cloneIP(start4); compareIP(cur, end4) <= 0; cur = nextIP(cur) {
		ips = append(ips, cur.String())
	}
	return ips, nil
}

// nextIP 返回下一个 IP（尾字节累加处理进位）。
func nextIP(ip net.IP) net.IP {
	next := cloneIP(ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

func cloneIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	return out
}

// compareIP 比较两个等长 IP（-1/0/1）。
func compareIP(a, b net.IP) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// sortIPs 按 IP 数值升序排序（IPv4 在前，其余按字符串）。
func sortIPs(ips []string) {
	sort.Slice(ips, func(i, j int) bool {
		a, b := net.ParseIP(ips[i]), net.ParseIP(ips[j])
		a4, b4 := a.To4(), b.To4()
		if a4 != nil && b4 != nil {
			return compareIP(a4, b4) < 0
		}
		if (a4 == nil) != (b4 == nil) {
			return a4 != nil // IPv4 排前面
		}
		return ips[i] < ips[j]
	})
}
