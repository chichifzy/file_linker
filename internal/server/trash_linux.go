//go:build linux

package server

// 删除 = 按 freedesktop.org 的约定移进用户回收站（~/.local/share/Trash），
// 同时写一份 .trashinfo，桌面环境的回收站里能看到、也能还原。

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// systemTrash 把条目移进回收站。
func systemTrash(full string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	filesDir := filepath.Join(data, "Trash", "files")
	infoDir := filepath.Join(data, "Trash", "info")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(uniquePath(filesDir, filepath.Base(full)))
	// Path= 要求是 URL 编码过的原始绝对路径，还原时靠它把文件放回去
	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: full}).EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))
	infoPath := filepath.Join(infoDir, name+".trashinfo")
	if err := os.WriteFile(infoPath, []byte(info), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(full, filepath.Join(filesDir, name)); err != nil {
		os.Remove(infoPath)
		return "", err
	}
	return "回收站", nil
}
