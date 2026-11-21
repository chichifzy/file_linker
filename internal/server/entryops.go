package server

// 重命名与删除（仅管理员）。删除走系统回收站：Windows 回收站、macOS 废纸篓、
// Linux 的 freedesktop 回收站；系统回收站用不了时（跨卷、没有桌面会话等）
// 退回共享目录里的隐藏目录 .file_linker-trash，保证文件不会被直接抹掉。

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// 应用自带的回收站目录名，位于共享目录根部。以点开头，所以默认的列表里看不到。
const trashDirName = ".file_linker-trash"

// resolveItem 把 path 解析成「条目本身」的绝对路径：最后一层不跟随符号链接
// （重命名、删除的对象是链接本身），但中间经过的目录必须真的落在共享目录内。
func (s *Server) resolveItem(r *http.Request) (root, full, rel string, err error) {
	q := r.URL.Query()
	root, err = s.sharePath(r, q.Get("share"))
	if err != nil {
		return
	}
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(q.Get("path"), "\\", "/")), "/")
	if rel == "" {
		err = errStatus(http.StatusBadRequest, "不能对共享目录的根目录执行该操作")
		return
	}
	full = filepath.Join(root, filepath.FromSlash(rel))
	if !within(root, full) {
		err = errStatus(http.StatusForbidden, "不能访问共享目录以外的文件")
		return
	}
	parent, perr := filepath.EvalSymlinks(filepath.Dir(full))
	if perr != nil {
		err = errStatus(http.StatusNotFound, "文件不存在")
		return
	}
	if !within(root, parent) {
		err = errStatus(http.StatusForbidden, "不能访问共享目录以外的文件")
		return
	}
	if _, lerr := os.Lstat(full); lerr != nil {
		if errors.Is(lerr, os.ErrPermission) {
			err = errStatus(http.StatusForbidden, "没有权限访问该文件")
		} else {
			err = errStatus(http.StatusNotFound, "文件不存在")
		}
		return
	}
	return
}

// renameEntry 只改名字（含扩展名），不移动位置。允许改后缀，例如 a.txt → b.zip。
func (s *Server) renameEntry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name, err := cleanEntryName(in.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	_, full, rel, err := s.resolveItem(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if name == filepath.Base(full) {
		// 名字没变，不用碰磁盘
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "path": rel})
		return
	}
	target := filepath.Join(filepath.Dir(full), name)
	// 只改大小写时（a.txt → A.txt），目标看起来已经存在，但那其实就是它自己，允许
	if fi, serr := os.Lstat(target); serr == nil {
		if src, lerr := os.Lstat(full); lerr != nil || !os.SameFile(fi, src) {
			writeError(w, http.StatusConflict, "已存在同名文件或文件夹")
			return
		}
	}
	if err := os.Rename(full, target); err != nil {
		switch {
		case errors.Is(err, os.ErrPermission):
			writeError(w, http.StatusForbidden, "没有权限重命名该文件")
		case errors.Is(err, os.ErrNotExist):
			writeError(w, http.StatusNotFound, "文件不存在，可能已被移动或删除")
		default:
			slog.Warn("rename failed", "from", full, "to", target, "err", err)
			writeError(w, http.StatusInternalServerError, "重命名失败："+shortOSError(err))
		}
		return
	}
	slog.Info("renamed", "from", rel, "to", name)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "path": path.Join(path.Dir(rel), name)})
}

// deleteEntry 把条目移到回收站，返回一句说明（where）用在页面提示里。
func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	root, full, rel, err := s.resolveItem(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if rel == trashDirName || strings.HasPrefix(rel, trashDirName+"/") {
		writeError(w, http.StatusBadRequest, "回收站里的项目不能在这里再删除")
		return
	}
	where, err := s.trashItem(root, full, rel)
	if err != nil {
		slog.Warn("trash failed", "path", full, "err", err)
		writeError(w, http.StatusInternalServerError, "删除失败："+shortOSError(err))
		return
	}
	slog.Info("moved to trash", "path", rel, "where", where)
	writeJSON(w, http.StatusOK, map[string]any{"where": where})
}

// trashItem 优先放进系统回收站，不行就放进共享目录里的隐藏回收站。
func (s *Server) trashItem(root, full, rel string) (string, error) {
	if where, err := systemTrash(full); err == nil {
		return where, nil
	} else {
		slog.Warn("系统回收站不可用，改放共享目录的回收站", "path", full, "err", err)
	}
	dest, shown, err := appTrashPath(root, rel)
	if err != nil {
		return "", err
	}
	if err := os.Rename(full, dest); err != nil {
		return "", err
	}
	return "共享目录的回收站「" + shown + "」", nil
}

// appTrashPath 算出共享目录回收站里的落点：按日期分目录，并保留原来的相对路径，
// 从落点就能看出文件原先在哪。返回实际路径和给人看的相对路径。
func appTrashPath(root, rel string) (dest, shown string, err error) {
	day := time.Now().Format("2006-01-02")
	folder := filepath.Join(root, trashDirName, day, filepath.FromSlash(path.Dir(rel)))
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", "", err
	}
	dest = uniquePath(folder, filepath.Base(rel))
	sub := path.Dir(rel)
	if sub == "." {
		sub = ""
	}
	shown = path.Join(trashDirName, day, sub, filepath.Base(dest))
	return dest, shown, nil
}
