// Package examples 内嵌示例安装脚本，并校验示例脚本与示例配置保持一致。
package examples

import (
	_ "embed"
	"strings"
)

//go:embed node_exporter/install.sh
var nodeExporterInstallSh []byte

//go:embed windows_exporter/install.ps1
var windowsExporterInstallPs1 []byte

// embeddedScripts 外部文件白名单名称 → 随服务端二进制发布的脚本内容。
//
// 平台无法出网、管理员也未上传离线文件时，/api/external/binary/{name} 用它兜底，
// 主机无需任何手工操作即可拿到与当前服务端版本配套的安装脚本。
var embeddedScripts = map[string][]byte{
	"node-exporter-install.sh":     nodeExporterInstallSh,
	"windows-exporter-install.ps1": windowsExporterInstallPs1,
}

// EmbeddedScript 返回内嵌脚本内容（名称大小写不敏感）；不存在时返回 nil。
func EmbeddedScript(name string) []byte {
	for k, v := range embeddedScripts {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}
