//go:build windows

package server

import (
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/sys/windows/registry"
)

// 桌面、下载等用户文件夹在注册表里的键名。这些文件夹允许通过
// “属性 → 位置 → 移动”改到其他磁盘（例如 D:\Desktop），所以不能只拼主目录。
var winQuickFolderKeys = []struct{ name, key string }{
	{"桌面", "Desktop"},
	{"下载", "{374DE290-123F-4565-9164-39C4925E467B}"},
}

var winEnvRef = regexp.MustCompile(`%([^%]+)%`)

// systemQuickFolders 从注册表读取桌面、下载的真实路径。读取失败时返回空，由调用方兜底。
func systemQuickFolders() []quickLink {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()

	var list []quickLink
	for _, it := range winQuickFolderKeys {
		v, _, err := key.GetStringValue(it.key)
		if err != nil || v == "" {
			continue
		}
		// 值多为 REG_EXPAND_SZ，形如 %USERPROFILE%\Desktop
		p := expandWinEnv(v)
		if !filepath.IsAbs(p) {
			continue
		}
		list = append(list, quickLink{it.name, filepath.Clean(p)})
	}
	return list
}

// expandWinEnv 展开 %VAR% 形式的环境变量；os.ExpandEnv 只认 $VAR，这里单独处理。
func expandWinEnv(s string) string {
	return winEnvRef.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}
