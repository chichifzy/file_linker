package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// 转码缓存目录可以在网页上改。改的时候把旧目录下的缓存整条搬到新目录，
// 搬完再清空旧目录，用户不必重新转一遍。
//
// 目录记在 settings 表里，重启后仍然生效；命令行 -cache-dir 优先于这里的设置。

const settingMediaDir = "media_dir"

var (
	mediaRootMu sync.RWMutex
	mediaRoot   string

	// 迁移期间不接受新的转码请求，否则新任务会往正在被搬走的旧目录里写
	mediaMoving atomic.Bool
	// 同时只允许一个迁移
	mediaMoveMu sync.Mutex
)

// defaultMediaDir 是没配置时的默认目录：系统缓存目录下的 file_linker\media。
func defaultMediaDir() string {
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "file_linker", "media")
}

func mediaDir() string {
	mediaRootMu.RLock()
	defer mediaRootMu.RUnlock()
	return mediaRoot
}

func setMediaDirRoot(dir string) {
	mediaRootMu.Lock()
	mediaRoot = dir
	mediaRootMu.Unlock()
}

// savedMediaDir 读取网页上设置的缓存目录，没设置过返回空串。
func (s *Server) savedMediaDir(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingMediaDir).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

func (s *Server) saveMediaDir(ctx context.Context, dir string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingMediaDir, dir)
	return err
}

// mediaDirEditable 判断当前请求能否修改缓存目录：
// 和修改共享目录一样，本机始终可以，其他设备需要本机开启开关。
func (s *Server) mediaDirEditable(r *http.Request) bool {
	ok, err := s.canEditShares(r)
	return err == nil && ok
}

// requireMediaDirAdmin 与修改共享目录同一策略：本机管理员始终可以，
// 其他设备需要本机开启「允许远程修改共享目录」，只是提示文案针对缓存目录。
func (s *Server) requireMediaDirAdmin(next http.HandlerFunc) http.Handler {
	return s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.canEditShares(r)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, http.StatusForbidden,
				"只能在运行服务的电脑上修改缓存目录；如需在其他设备修改，请先在本机允许远程修改共享目录")
			return
		}
		next(w, r)
	})
}

// setMediaDir 修改缓存目录，并把已有缓存迁移过去。
func (s *Server) setMediaDir(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir string `json:"dir"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	to, err := normalizeMediaDir(in.Dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from := mediaDir()
	if samePath(to, from) {
		writeJSON(w, http.StatusOK, map[string]any{"dir": from, "moved": 0, "bytes": 0})
		return
	}

	if !mediaMoveMu.TryLock() {
		writeError(w, http.StatusConflict, "缓存目录正在迁移，请稍候")
		return
	}
	defer mediaMoveMu.Unlock()

	mediaMu.Lock()
	for _, e := range mediaEntries {
		if e.Status == "running" {
			name := e.Name
			mediaMu.Unlock()
			writeError(w, http.StatusConflict,
				fmt.Sprintf("「%s」正在转换，等它完成（或在列表里删除它）后再修改目录", name))
			return
		}
	}
	mediaMu.Unlock()

	if err := prepareMediaDir(to); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	mediaMoving.Store(true)
	defer mediaMoving.Store(false)

	moved, bytes, err := migrateMediaEntries(from, to)
	if err != nil {
		internalError(w, r, err)
		return
	}
	// 先落库再切内存里的目录：写库失败说明这次修改没成功，把缓存搬回去，
	// 否则重启后会回到旧目录，而旧目录已经被清空，缓存就白丢了
	if err := s.saveMediaDir(r.Context(), to); err != nil {
		if _, _, rbErr := migrateMediaEntries(to, from); rbErr != nil {
			slog.Warn("目录设置写入失败且回滚失败", "err", rbErr)
		}
		internalError(w, r, err)
		return
	}
	setMediaDirRoot(to)
	cleanOldMediaDir(from)
	slog.Info("转码缓存目录已修改", "from", from, "to", to,
		"moved", moved, "bytes", fmtBytes(bytes))
	writeJSON(w, http.StatusOK, map[string]any{"dir": to, "moved": moved, "bytes": bytes})
}

// normalizeMediaDir 校验并规范化用户输入的目录。
func normalizeMediaDir(in string) (string, error) {
	dir := strings.TrimSpace(in)
	if dir == "" {
		return "", errors.New("目录不能为空")
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return "", errors.New("请填写完整路径，例如 D:\\video-cache")
	}
	if isRootPath(dir) {
		return "", errors.New("不能直接用磁盘根目录，请在其下新建一个子目录，例如 D:\\video-cache")
	}
	// 相等的情况交给调用方当「没变化」处理，这里只拦真正的上下级
	if from := mediaDir(); !samePath(dir, from) && (pathNested(dir, from) || pathNested(from, dir)) {
		return "", errors.New("新目录不能是当前缓存目录的上级或下级")
	}
	return dir, nil
}

// isRootPath 判断是否磁盘根目录（Windows 的盘符根、Unix 的 / 这类）。
func isRootPath(dir string) bool {
	clean := filepath.Clean(dir)
	if vol := filepath.VolumeName(clean); vol != "" {
		return strings.EqualFold(clean, vol+string(filepath.Separator)) || clean == vol
	}
	return clean == string(filepath.Separator)
}

// prepareMediaDir 创建目录并确认可写；已有内容时只允许是缓存目录（全为缓存 ID）。
func prepareMediaDir(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return errors.New("这个路径是个文件，请填写目录")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("无法读取该目录：%w", err)
		}
		for _, de := range entries {
			if !de.IsDir() || !isHexID(de.Name()) {
				return errors.New("目标目录不是空的，请选择一个空目录或新建的目录")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法访问该路径：%w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建目录：%w", err)
	}
	// 真正写一次，避免「有权限创建但没权限写文件」这种半通的情况
	probe := filepath.Join(dir, ".probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("目录不可写：%w", err)
	}
	_ = os.Remove(probe)
	return nil
}

// migrateMediaEntries 把 from 下的缓存整条搬到 to。任意一条失败就把已搬的搬回去，
// 保持「要么全搬走，要么保持原样」，避免出现两份缓存。
func migrateMediaEntries(from, to string) (int, int64, error) {
	mediaMu.Lock()
	ids := make([]string, 0, len(mediaEntries))
	for id := range mediaEntries {
		ids = append(ids, id)
	}
	mediaMu.Unlock()
	sort.Strings(ids)

	type moved struct{ src, dst string }
	var done []moved
	var total int64
	for _, id := range ids {
		src := filepath.Join(from, id)
		dst := filepath.Join(to, id)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			// 磁盘上的目录已经不在了（被手动删过），这条缓存是空壳，一并去掉
			mediaMu.Lock()
			delete(mediaEntries, id)
			mediaMu.Unlock()
			slog.Warn("缓存目录缺失，丢弃该条目", "id", id)
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			// 目标里已经有同一个文件的缓存（之前用过这个目录），保留目标、清掉旧的
			os.RemoveAll(src)
			done = append(done, moved{src: src, dst: dst})
			continue
		}
		sz := dirSize(src)
		if err := moveDir(src, dst); err != nil {
			// 回滚：把已经搬过去的搬回来
			for i := len(done) - 1; i >= 0; i-- {
				if err := moveDir(done[i].dst, done[i].src); err != nil {
					slog.Warn("迁移回滚失败", "dir", done[i].dst, "err", err)
				}
			}
			return 0, 0, fmt.Errorf("迁移缓存失败：%w", err)
		}
		done = append(done, moved{src: src, dst: dst})
		total += sz
	}
	return len(done), total, nil
}

// cleanOldMediaDir 清空旧目录里的缓存文件。只删缓存相关的内容，
// 万一用户把缓存目录指到了别处，也不会误删他自己的文件。
func cleanOldMediaDir(dir string) {
	// 旧目录万一是磁盘根（历史遗留的设置），一律不清理
	if dir == "" || isRootPath(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range entries {
		name := de.Name()
		p := filepath.Join(dir, name)
		switch {
		case de.IsDir() && isHexID(name):
			os.RemoveAll(p)
		case name == mediaMetaName, strings.HasSuffix(name, ".part"), name == ".probe":
			os.Remove(p)
		}
	}
	// 空了就顺手删掉，留着也只是个空壳
	if left, err := os.ReadDir(dir); err == nil && len(left) == 0 {
		os.Remove(dir)
	}
}

// moveDir 把整个目录搬到别处。同一块盘用 rename（瞬间完成），
// 跨盘先复制到 .part 再改名，避免中途失败留下半个目录。
func moveDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	tmp := dst + ".part"
	os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return os.RemoveAll(src)
}

// copyTree 递归复制目录，保留文件权限。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pathNested 判断 child 是否在 parent 之内（含相等）。
func pathNested(child, parent string) bool {
	if parent == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
