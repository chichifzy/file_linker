package server

// 解压（仅管理员）：把 zip / 7z 解压到压缩包所在的当前目录。
// 先解压到隐藏的临时目录，全部成功后再搬到目标目录：
// 取消或中途失败不会在共享目录里留下半个文件。

import (
	"context"
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
	"sync"
	"sync/atomic"
	"time"

	"file_linker/internal/unpack"
)

// 解压临时目录的前缀，位于目标目录里。以点开头，默认的列表里看不到。
const extractTmpPrefix = ".file_linker-ex-"

// 同时进行的解压数：都要读写磁盘，多了只会互相拖慢。
var extractSem = make(chan struct{}, 2)

type extractJob struct {
	id         string
	userID     int64
	shareID    int64
	name       string // 压缩包文件名
	archiveRel string // 压缩包相对共享目录的路径
	dirRel     string // 解压到的目录，相对共享目录
	cancel     context.CancelFunc
	done       atomic.Int64
	total      atomic.Int64
	mu         sync.Mutex
	status     string // running / done / error / cancelled
	err        string
	code       string // need_password / wrong_password，前端据此弹密码框
	files      int
	skipped    int
	finishedAt time.Time
}

var (
	extractMu   sync.Mutex
	extractJobs = map[string]*extractJob{}
)

// initExtract 定时清理已结束的任务（解压出来的文件当然保留）。
func initExtract() {
	go func() {
		for range time.Tick(time.Minute) {
			now := time.Now()
			extractMu.Lock()
			for id, j := range extractJobs {
				j.mu.Lock()
				status, finishedAt := j.status, j.finishedAt
				j.mu.Unlock()
				if status != "running" && now.Sub(finishedAt) > liveKeep {
					delete(extractJobs, id)
				}
			}
			extractMu.Unlock()
		}
	}()
}

// getExtractJob 取当前用户自己的任务。
func getExtractJob(r *http.Request) *extractJob {
	extractMu.Lock()
	defer extractMu.Unlock()
	j := extractJobs[r.PathValue("id")]
	if j == nil || j.userID != currentUser(r).ID {
		return nil
	}
	return j
}

// extractRunning 判断某个 id 的任务是否还在跑（用于避免误删别人的临时目录）。
func extractRunning(id string) bool {
	extractMu.Lock()
	defer extractMu.Unlock()
	j := extractJobs[id]
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == "running"
}

// createExtract 校验压缩包和密码，然后开始后台解压。
func (s *Server) createExtract(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	_, full, rel, err := s.resolveItem(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !unpack.IsArchive(full) {
		writeError(w, http.StatusBadRequest, "只能解压 .zip 或 .7z 文件")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	// 先打开一次：密码不对时当场告诉前端，不用等任务跑起来才失败
	ar, err := unpack.Open(full, in.Password)
	if err != nil {
		writeUnpackError(w, err)
		return
	}
	entries := ar.Entries()
	ar.Close()

	var total int64
	for _, e := range entries {
		if !e.IsDir {
			total += e.Size
		}
	}

	select {
	case extractSem <- struct{}{}:
	default:
		writeError(w, http.StatusServiceUnavailable, "已有 2 个解压任务在进行，请稍后再试")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	shareID, _ := strconv.ParseInt(r.URL.Query().Get("share"), 10, 64)
	j := &extractJob{
		id: newJobID(), userID: currentUser(r).ID, shareID: shareID,
		name: filepath.Base(full), archiveRel: rel, dirRel: dir,
		cancel: cancel, status: "running",
	}
	j.total.Store(total)
	extractMu.Lock()
	extractJobs[j.id] = j
	extractMu.Unlock()

	go func() {
		defer func() { <-extractSem }()
		s.runExtract(ctx, j, full, in.Password)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"id": j.id, "name": j.name, "total": total})
}

func (s *Server) runExtract(ctx context.Context, j *extractJob, full, password string) {
	target := filepath.Dir(full)
	tmp := filepath.Join(target, extractTmpPrefix+j.id)
	err := s.extractTo(ctx, j, full, password, tmp)
	if err == nil {
		err = moveIntoPlace(tmp, target)
		if err != nil {
			err = errStatus(http.StatusInternalServerError, "解压完成，但移动到目标目录时失败："+shortOSError(err))
		}
	}
	os.RemoveAll(tmp) // 无论成败，临时目录都不留下

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != "running" {
		return // 用户已经点了取消，状态由取消那边写好
	}
	j.finishedAt = time.Now()
	switch {
	case err == nil:
		j.status = "done"
		slog.Info("extract done", "archive", j.archiveRel, "dir", j.dirRel,
			"files", j.files, "bytes", fmtBytes(j.done.Load()))
	case errors.Is(err, context.Canceled):
		j.status, j.err = "cancelled", "已取消"
	default:
		j.status, j.code = "error", unpackCode(err)
		j.err = unpackMessage(err)
		slog.Warn("extract failed", "archive", j.archiveRel, "err", err)
	}
}

// extractTo 把条目逐个解到临时目录 tmp 下。
func (s *Server) extractTo(ctx context.Context, j *extractJob, full, password, tmp string) error {
	ar, err := unpack.Open(full, password)
	if err != nil {
		return err
	}
	defer ar.Close()
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	cleanStaleExtractDirs(filepath.Dir(tmp), j.id)

	for i, e := range ar.Entries() {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 符号链接指向哪里不可预知，不往共享目录里放
		rel, ok := cleanArchiveEntry(e.Name)
		if e.Symlink || !ok {
			j.mu.Lock()
			j.skipped++
			j.mu.Unlock()
			continue
		}
		dest := filepath.Join(tmp, filepath.FromSlash(rel))
		if e.IsDir {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		rc, err := ar.Open(i)
		if err != nil {
			return err
		}
		err = writeEntry(ctx, dest, rc, e, j)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeEntry 写出一个文件并累计进度；失败时把自己删掉，不留半个文件。
func writeEntry(ctx context.Context, dest string, r io.Reader, e unpack.Entry, j *extractJob) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(&progressWriter{ctx: ctx, w: out, n: &j.done}, r, make([]byte, 128<<10))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return err
	}
	if !e.ModTime.IsZero() {
		os.Chtimes(dest, time.Now(), e.ModTime) // 保留压缩包里的修改时间，失败也不影响解压结果
	}
	j.mu.Lock()
	j.files++
	j.mu.Unlock()
	return nil
}

// moveIntoPlace 把临时目录里的内容搬到目标目录；重名的文件加「(1)」，不覆盖已有文件。
func moveIntoPlace(tmp, target string) error {
	return filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tmp, p)
		if err != nil || rel == "." {
			return nil
		}
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if _, err := os.Lstat(dst); err == nil {
			dst = uniquePath(filepath.Dir(dst), filepath.Base(dst))
		}
		return os.Rename(p, dst)
	})
}

// cleanStaleExtractDirs 清掉同目录下上次进程被强杀时留下的临时目录。
// 只认自己的前缀，且跳过正在进行的任务。
func cleanStaleExtractDirs(dir, selfID string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, extractTmpPrefix) {
			continue
		}
		id := strings.TrimPrefix(name, extractTmpPrefix)
		if id == selfID || extractRunning(id) {
			continue
		}
		os.RemoveAll(filepath.Join(dir, name))
	}
}

// cleanArchiveEntry 把压缩包里的名字变成可以安全落盘的相对路径：
// 挡掉 ../、绝对路径和盘符，各段再按新建文件夹的规则清洗。
// 返回 false 表示这个条目不能解压，跳过。
func cleanArchiveEntry(name string) (string, bool) {
	n := strings.ReplaceAll(name, "\\", "/")
	n = strings.TrimLeft(n, "/")
	if len(n) >= 2 && n[1] == ':' {
		n = strings.TrimLeft(n[2:], "/")
	}
	c := path.Clean(n)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	parts := strings.Split(c, "/")
	for i, p := range parts {
		p = sanitizeName(p)
		if p == "" {
			return "", false
		}
		parts[i] = p
	}
	return strings.Join(parts, "/"), true
}

func (s *Server) extractStatus(w http.ResponseWriter, r *http.Request) {
	j := getExtractJob(r)
	if j == nil {
		writeError(w, http.StatusNotFound, "解压任务不存在或已结束")
		return
	}
	j.mu.Lock()
	status, msg, code, files, skipped, dir := j.status, j.err, j.code, j.files, j.skipped, j.dirRel
	j.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "error": msg, "code": code, "name": j.name,
		"done": j.done.Load(), "total": j.total.Load(),
		"files": files, "skipped": skipped, "dir": dir,
	})
}

// cancelExtract 取消正在进行的解压；对已经结束的任务则是把记录去掉。
func (s *Server) cancelExtract(w http.ResponseWriter, r *http.Request) {
	j := getExtractJob(r)
	if j == nil {
		writeError(w, http.StatusNotFound, "解压任务不存在或已结束")
		return
	}
	j.mu.Lock()
	if j.status != "running" {
		j.mu.Unlock()
		extractMu.Lock()
		delete(extractJobs, j.id)
		extractMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// 先标记再取消：后台收尾时看到 cancelled 就不会再改成错误
	j.status, j.err = "cancelled", "已取消"
	j.finishedAt = time.Now()
	j.mu.Unlock()
	j.cancel()
	w.WriteHeader(http.StatusNoContent)
}

// writeUnpackError 把打不开压缩包的原因告诉前端，密码问题带上 code。
func writeUnpackError(w http.ResponseWriter, err error) {
	if code := unpackCode(err); code != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": unpackMessage(err), "code": code})
		return
	}
	writeError(w, http.StatusBadRequest, unpackMessage(err))
}

func unpackCode(err error) string {
	switch {
	case errors.Is(err, unpack.ErrNeedPassword):
		return "need_password"
	case errors.Is(err, unpack.ErrWrongPassword):
		return "wrong_password"
	}
	return ""
}

func unpackMessage(err error) string {
	switch {
	case errors.Is(err, unpack.ErrNeedPassword):
		return "这个压缩包有密码，请输入密码"
	case errors.Is(err, unpack.ErrWrongPassword):
		return "密码不正确"
	case errors.Is(err, unpack.ErrUnsupportedFormat):
		return "不是有效的 zip / 7z 文件，或用了暂不支持的压缩算法"
	case errors.Is(err, unpack.ErrBadData):
		return "压缩包已损坏，或密码不正确"
	case errors.Is(err, context.Canceled):
		return "已取消"
	case errors.Is(err, context.DeadlineExceeded):
		return "操作超时"
	}
	return "解压失败：" + shortOSError(err)
}
