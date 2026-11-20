//go:build !windows

package server

// systemQuickFolders 在非 Windows 平台不提供系统级路径，由 homeQuickFolders 兜底。
func systemQuickFolders() []quickLink {
	return nil
}
