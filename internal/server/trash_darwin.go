//go:build darwin

package server

// 删除 = 移到废纸篓，效果和 Finder 的「移到废纸篓」一样，之后可以在废纸篓里
// 右键「放回原处」。同一卷内的文件直接移到 ~/.Trash；外接磁盘上的文件按 macOS
// 的约定放到该卷的 .Trashes/<uid> 里。

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// systemTrash 把条目移到废纸篓。
func systemTrash(full string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	mount, err := mountPoint(full)
	if err != nil {
		return "", err
	}
	trash := filepath.Join(home, ".Trash")
	if mount != "/" {
		trash = filepath.Join(mount, ".Trashes", fmt.Sprint(os.Getuid()))
		if err := os.MkdirAll(trash, 0o700); err != nil {
			return "", err
		}
	}
	dest := uniquePath(trash, filepath.Base(full))
	if err := os.Rename(full, dest); err != nil {
		return "", err
	}
	return "废纸篓", nil
}

// mountPoint 找出 p 所在卷的挂载点：一直往上找到设备号发生变化的那一层。
func mountPoint(p string) (string, error) {
	dir, err := filepath.Abs(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	dev, err := deviceOf(dir)
	if err != nil {
		return "", err
	}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir, nil
		}
		pdev, err := deviceOf(parent)
		if err != nil || pdev != dev {
			return dir, nil
		}
		dir = parent
	}
}

func deviceOf(p string) (uint64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("无法判断 %s 所在的卷", p)
	}
	return uint64(st.Dev), nil
}
