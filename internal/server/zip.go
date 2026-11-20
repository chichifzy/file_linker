package server

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 文件夹打包下载：先在服务器临时目录打成 zip，完整下载后立即删除。
const (
	zipTempPattern = "file_linker-*.zip"
	zipKeep        = 30 * time.Minute // 打包完成后多久未下载就删除
)

// 同时进行的打包任务数，超出时提示服务器忙
var zipSem = make(chan struct{}, 2)

type zipJob struct {
	id     string
	userID int64
	name   string // 下载文件名（不含 .zip）
	file   string // 临时 zip 路径
	cancel context.CancelFunc
	done   atomic.Int64 // 已打包的源文件字节数
	total  atomic.Int64

	mu          sync.Mutex
	status      string // running / done / error
	err         string
	finishedAt  time.Time
	downloading bool
	removed     bool
}

var (
	zipMu   sync.Mutex
	zipJobs = map[string]*zipJob{}
)

// initZip 清理上次运行遗留的临时 zip，并定时清理长时间未下载的任务。
func initZip() {
	if old, _ := filepath.Glob(filepath.Join(os.TempDir(), zipTempPattern)); len(old) > 0 {
		for _, f := range old {
			os.Remove(f)
		}
	}
	go func() {
		for range time.Tick(time.Minute) {
			zipMu.Lock()
			for _, j := range zipJobs {
				j.mu.Lock()
				expired := j.status != "running" && !j.downloading && time.Since(j.finishedAt) > zipKeep
				j.mu.Unlock()
				if expired {
					j.removeLocked()
				}
			}
			zipMu.Unlock()
		}
	}()
}

// removeLocked 取消并删除任务，调用方需持有 zipMu。打包中的任务由打包协程在退出时删除文件。
func (j *zipJob) removeLocked() {
	delete(zipJobs, j.id)
	j.cancel()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.removed = true
	if j.status != "running" && !j.downloading {
		os.Remove(j.file)
	}
}

// getZipJob 取当前用户自己的任务。
func getZipJob(r *http.Request) *zipJob {
	zipMu.Lock()
	defer zipMu.Unlock()
	j := zipJobs[r.PathValue("id")]
	if j == nil || j.userID != currentUser(r).ID {
		return nil
	}
	return j
}

func (s *Server) createZip(w http.ResponseWriter, r *http.Request) {
	_, full, _, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	select {
	case zipSem <- struct{}{}:
	default:
		writeError(w, http.StatusServiceUnavailable, "服务器正在打包其他文件夹，请稍后再试")
		return
	}
	f, err := os.CreateTemp("", zipTempPattern)
	if err != nil {
		<-zipSem
		writeError(w, http.StatusInternalServerError, "无法创建临时文件")
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	j := &zipJob{id: hex.EncodeToString(b), userID: currentUser(r).ID, name: fi.Name(), file: f.Name(),
		cancel: cancel, status: "running"}
	zipMu.Lock()
	zipJobs[j.id] = j
	zipMu.Unlock()

	go func() {
		defer func() { <-zipSem }()
		err := writeZip(ctx, f, full, fi.Name(), j)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		j.finishedAt = time.Now()
		if err != nil {
			j.status, j.err = "error", "打包失败"
			if ctx.Err() == nil {
				slog.Warn("zip failed", "dir", full, "err", err)
			}
			os.Remove(j.file)
			return
		}
		j.status = "done"
		if j.removed {
			os.Remove(j.file)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"id": j.id})
}

// writeZip 把 dir 打包进 zip，zip 内以 rootName 为顶层目录；包含隐藏文件，不跟随符号链接，读不了的文件跳过。
func writeZip(ctx context.Context, out io.Writer, dir, rootName string, j *zipJob) error {
	var total int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	j.total.Store(total)

	zw := zip.NewWriter(out)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || !(d.IsDir() || d.Type().IsRegular()) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		h.Name = path.Join(rootName, filepath.ToSlash(rel))
		if d.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer src.Close()
		h.Method = zip.Deflate
		dst, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, &progressReader{ctx: ctx, r: src, n: &j.done})
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

type progressReader struct {
	ctx context.Context
	r   io.Reader
	n   *atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

func (s *Server) zipStatus(w http.ResponseWriter, r *http.Request) {
	j := getZipJob(r)
	if j == nil {
		writeError(w, http.StatusNotFound, "打包任务不存在或已过期")
		return
	}
	j.mu.Lock()
	status, msg := j.status, j.err
	j.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "error": msg, "done": j.done.Load(), "total": j.total.Load(),
	})
}

// downloadZip 输出打包好的 zip；完整发送后删除临时文件，中途断开则保留以便重试。
func (s *Server) downloadZip(w http.ResponseWriter, r *http.Request) {
	j := getZipJob(r)
	if j == nil {
		writeError(w, http.StatusNotFound, "打包任务不存在或已过期")
		return
	}
	j.mu.Lock()
	if j.status != "done" || j.downloading || j.removed {
		j.mu.Unlock()
		writeError(w, http.StatusConflict, "压缩包还没有准备好")
		return
	}
	j.downloading = true
	j.mu.Unlock()

	ok := false
	defer func() {
		zipMu.Lock()
		defer zipMu.Unlock()
		j.mu.Lock()
		j.downloading = false
		j.finishedAt = time.Now()
		j.mu.Unlock()
		if ok || j.removed {
			j.removeLocked()
		}
	}()

	f, err := os.Open(j.file)
	if err != nil {
		writeError(w, http.StatusNotFound, "压缩包不存在")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取压缩包")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": j.name + ".zip"}))
	h.Set("Cache-Control", "no-store")
	n, err := io.Copy(w, f)
	ok = err == nil && n == fi.Size()
}

func (s *Server) deleteZip(w http.ResponseWriter, r *http.Request) {
	if j := getZipJob(r); j != nil {
		zipMu.Lock()
		j.removeLocked()
		zipMu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}
