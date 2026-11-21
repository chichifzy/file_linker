package server

// 拷贝 / 剪切 → 粘贴（仅管理员）。前端只记住「源在哪」，真正的复制和移动都在这里做：
// 重名时自动加序号，剪切跨盘时退回复制后删除，避免 rename 直接失败。

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pasteEntry 把 srcPath 复制或移动到当前目录（query 里的 share / path）。
func (s *Server) pasteEntry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SrcShare int64  `json:"srcShare"`
		SrcPath  string `json:"srcPath"`
		Mode     string `json:"mode"`   // copy / move
		Rename   bool   `json:"rename"` // 用户在重名提示里选了「继续粘贴」
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Mode != "copy" && in.Mode != "move" {
		writeError(w, http.StatusBadRequest, "只能复制或移动")
		return
	}

	// 目标目录：必须存在，且真的在共享目录内（resolve 会解析符号链接）
	_, dstFull, dstRel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if fi, serr := os.Stat(dstFull); serr != nil || !fi.IsDir() {
		writeError(w, http.StatusBadRequest, "只能粘贴到文件夹里")
		return
	}

	srcRoot, err := s.sharePath(r, strconv.FormatInt(in.SrcShare, 10))
	if err != nil {
		fail(w, r, err)
		return
	}
	srcFull, srcRel, err := resolveItemAt(srcRoot, in.SrcPath)
	if err != nil {
		fail(w, r, err)
		return
	}
	srcInfo, err := os.Lstat(srcFull)
	if err != nil {
		writeError(w, http.StatusNotFound, "源文件不存在，可能已被移动或删除")
		return
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusBadRequest, "符号链接不能复制或移动")
		return
	}
	// 文件夹不能放进它自己（或它的子目录）里
	if srcInfo.IsDir() && pathNested(dstFull, srcFull) {
		writeError(w, http.StatusBadRequest, "不能把文件夹放到它自己里面")
		return
	}

	name := srcInfo.Name()
	// 剪切到原目录等于什么都没做，别生成一个「x (1)」
	if in.Mode == "move" && samePath(filepath.Dir(srcFull), dstFull) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": name, "path": srcRel, "mode": in.Mode, "sameDir": true,
		})
		return
	}

	// 重名先问一次，不要悄悄改名：前端拿到 proposed 让用户选取消还是继续
	target := uniquePath(dstFull, name)
	if !in.Rename && target != filepath.Join(dstFull, name) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "当前文件夹已有同名的文件或文件夹",
			"code":     "name_conflict",
			"name":     name,
			"proposed": filepath.Base(target),
		})
		return
	}
	if in.Mode == "copy" {
		err = copyEntryTree(srcFull, target)
	} else {
		err = moveEntry(srcFull, target)
	}
	if err != nil {
		slog.Warn("粘贴失败", "from", srcFull, "to", target, "mode", in.Mode, "err", err)
		writeError(w, http.StatusInternalServerError, pasteErrText(in.Mode, err))
		return
	}
	// 粘贴会把源所在目录的修改时间也改掉（剪切），列表下次刷新时自然就对了
	slog.Info("粘贴完成", "from", srcRel, "to", path.Join(dstRel, filepath.Base(target)), "mode", in.Mode)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": filepath.Base(target), "path": path.Join(dstRel, filepath.Base(target)), "mode": in.Mode,
	})
}

// resolveItemAt 把相对路径解析成「条目本身」的绝对路径：最后一层不跟随符号链接，
// 但父目录必须真的落在共享目录内。resolveItem 与粘贴共用这段逻辑。
func resolveItemAt(root, relIn string) (full, rel string, err error) {
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(relIn, "\\", "/")), "/")
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
	}
	return
}

// copyEntryTree 递归复制文件或目录；目录里的符号链接跳过（免得复制出共享目录外的东西），
// 并保留原来的修改时间，列表里看起来才和源一致。
func copyEntryTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("符号链接不能复制")
	}
	if !fi.IsDir() {
		return copyEntryFile(src, dst, fi)
	}
	if err := os.MkdirAll(dst, fi.Mode().Perm()); err != nil {
		return err
	}
	// 目录的修改时间要等里面的东西都写完才能补，这里记下源目录的时间和落点
	type dirStamp struct {
		dst string
		mod time.Time
	}
	stamps := []dirStamp{{dst: dst, mod: fi.ModTime()}}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == src {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			if merr := os.MkdirAll(out, info.Mode().Perm()); merr != nil {
				return merr
			}
			stamps = append(stamps, dirStamp{dst: out, mod: info.ModTime()})
			return nil
		}
		return copyEntryFile(p, out, info)
	})
	if err != nil {
		return err
	}
	// 从深到浅补目录时间，否则先设的会被后面写入子项的操作改掉
	for i := len(stamps) - 1; i >= 0; i-- {
		_ = os.Chtimes(stamps[i].dst, time.Now(), stamps[i].mod)
	}
	return nil
}

func copyEntryFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// 目标名是 uniquePath 选出来的，O_EXCL 能挡住并发粘贴撞名
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	// 复制出来的文件保留原来的修改时间，列表里看起来才和源一致
	_ = os.Chtimes(dst, time.Now(), info.ModTime())
	return nil
}

// moveTree 优先直接 rename（同盘瞬间完成），跨盘时退回复制 + 删除源。
func moveEntry(src, dst string) error {
	renameErr := os.Rename(src, dst)
	if renameErr == nil {
		return nil
	}
	if cerr := copyTree(src, dst); cerr != nil {
		os.RemoveAll(dst)
		// 复制也失败时，rename 的错误更能说明问题（多半是没权限）
		return renameErr
	}
	if rerr := os.RemoveAll(src); rerr != nil {
		return rerr
	}
	return nil
}

func pasteErrText(mode string, err error) string {
	verb := "复制"
	if mode == "move" {
		verb = "移动"
	}
	switch {
	case errors.Is(err, os.ErrPermission):
		return verb + "失败：没有权限，可能目标位置是只读的"
	case errors.Is(err, os.ErrNotExist):
		return verb + "失败：文件不存在，可能已被移动或删除"
	default:
		return verb + "失败：" + shortOSError(err)
	}
}
