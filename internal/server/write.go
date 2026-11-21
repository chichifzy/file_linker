package server

// 写入共享目录：新建文件夹、按链接把文件下载到运行服务的这台电脑上。
// 两个操作都会改动本机磁盘，只开放给管理员（普通用户仍然只读）。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// 单个文件名（含扩展名）的最大长度，主流文件系统的上限是 255 字节，
// 中文名按 UTF-8 一个字 3 字节算，留一些余量。
const maxEntryName = 200

// 新建文件夹 / 重命名一类操作共用的名称校验。
func cleanEntryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", errStatus(http.StatusBadRequest, "请输入名称")
	case name == "." || name == "..":
		return "", errStatus(http.StatusBadRequest, "名称无效")
	case strings.ContainsAny(name, `/\`):
		return "", errStatus(http.StatusBadRequest, `名称不能包含 "/" 或 "\"`)
	case strings.ContainsFunc(name, func(r rune) bool { return r == 0 || unicode.IsControl(r) }):
		return "", errStatus(http.StatusBadRequest, "名称不能包含控制字符")
	case utf8.RuneCountInString(name) > maxEntryName:
		return "", errStatus(http.StatusBadRequest, "名称过长")
	case strings.HasSuffix(name, "."):
		return "", errStatus(http.StatusBadRequest, "名称不能以点结尾（Windows 不允许）")
	}
	if runtime.GOOS == "windows" && windowsReserved(name) {
		return "", errStatus(http.StatusBadRequest, "该名称是 Windows 保留名称，请换一个")
	}
	return name, nil
}

// windowsReserved 判断是否是 CON、PRN、NUL、COM1 这类保留名（带扩展名也算）。
func windowsReserved(name string) bool {
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 {
		switch base[:3] {
		case "COM", "LPT":
			return base[3] >= '1' && base[3] <= '9'
		}
	}
	return false
}

// createDir 在当前目录下新建一个文件夹。
func (s *Server) createDir(w http.ResponseWriter, r *http.Request) {
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
	root, parent, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		writeError(w, http.StatusBadRequest, "当前位置不是文件夹")
		return
	}
	target := filepath.Join(parent, name)
	if !within(root, target) {
		writeError(w, http.StatusForbidden, "不能访问共享目录以外的文件")
		return
	}
	// 用 Mkdir 而不是 MkdirAll：已存在同名条目时直接报错，避免误判成功
	if err := os.Mkdir(target, 0o755); err != nil {
		switch {
		case errors.Is(err, os.ErrExist):
			writeError(w, http.StatusConflict, "已存在同名文件或文件夹")
		case errors.Is(err, os.ErrPermission):
			writeError(w, http.StatusForbidden, "没有权限在该目录中创建文件夹")
		case errors.Is(err, os.ErrNotExist):
			writeError(w, http.StatusNotFound, "当前目录不存在")
		default:
			slog.Warn("mkdir failed", "path", target, "err", err)
			writeError(w, http.StatusInternalServerError, "创建文件夹失败："+shortOSError(err))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "path": path.Join(rel, name)})
}

// shortOSError 把系统错误压缩成一句能看懂的话（Windows 的提示常常带一长串前缀）。
func shortOSError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// ---------- 按链接下载 ----------

const (
	// 任务结束后在内存里再留一会儿，正在轮询的弹窗还能取到最终状态；
	// 记录本身存在数据库里长期保留，只有用户主动删除才消失。
	liveKeep   = 10 * time.Minute
	downloadUA = "Mozilla/5.0 (compatible; file_linker)"
)

// 同时进行的下载任务数。每个任务都会占满一条下行带宽，多了没意义。
var downloadSem = make(chan struct{}, 2)

type downloadJob struct {
	id         string
	userID     int64
	shareID    int64
	dirRel     string // 目标目录，相对共享目录
	url        string // 原始链接，用于在任务列表里重试
	cancel     context.CancelFunc
	done       atomic.Int64
	total      atomic.Int64
	started    time.Time
	mu         sync.Mutex
	name       string // 最终文件名
	tmp        string // 未完成的临时文件
	status     string // running / done / error / cancelled
	err        string
	finishedAt time.Time
}

var (
	downloadMu   sync.Mutex
	downloadJobs = map[string]*downloadJob{}
)

// initDownloads 定时清理内存里的任务对象（数据库里的记录不动）。
func initDownloads() {
	go func() {
		for range time.Tick(time.Minute) {
			pruneFinishedJobs()
		}
	}()
}

// pruneFinishedJobs 把结束超过 liveKeep 的任务从内存里去掉，记录仍在数据库里。
func pruneFinishedJobs() {
	now := time.Now()
	downloadMu.Lock()
	defer downloadMu.Unlock()
	for id, j := range downloadJobs {
		j.mu.Lock()
		status, finishedAt := j.status, j.finishedAt
		j.mu.Unlock()
		if status != "running" && now.Sub(finishedAt) > liveKeep {
			delete(downloadJobs, id)
		}
	}
}

// downloadClient 专用于下载外部链接：没有整体超时（大文件要下很久），
// 但连接、TLS 握手和等待响应都有上限，卡住时能尽快报错。
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		// 不要透明解压：保持字节与服务器上一致，Content-Length 也才有意义
		DisableCompression: true,
		MaxIdleConns:       4,
		ForceAttemptHTTP2:  true,
	},
}

// getDownloadJob 取当前用户自己的任务。
func getDownloadJob(r *http.Request) *downloadJob {
	downloadMu.Lock()
	defer downloadMu.Unlock()
	j := downloadJobs[r.PathValue("id")]
	if j == nil || j.userID != currentUser(r).ID {
		return nil
	}
	return j
}

// createDownload 在后台把链接指向的文件下载到当前目录，立即返回任务 id。
func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	raw := strings.TrimSpace(in.URL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "请填写以 http:// 或 https:// 开头的下载链接")
		return
	}
	hint := ""
	if strings.TrimSpace(in.Name) != "" {
		if hint, err = cleanEntryName(in.Name); err != nil {
			fail(w, r, err)
			return
		}
	}
	_, dir, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeError(w, http.StatusBadRequest, "当前位置不是文件夹")
		return
	}
	select {
	case downloadSem <- struct{}{}:
	default:
		writeError(w, http.StatusServiceUnavailable, "服务器正在下载其他文件，请稍后再试")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	shareID, _ := strconv.ParseInt(r.URL.Query().Get("share"), 10, 64)
	// 还没发请求时先用链接里的文件名占位，这样任务中断、失败时列表里也有名字
	preset := hint
	if preset == "" {
		if p, err := url.PathUnescape(u.Path); err == nil {
			preset = sanitizeName(path.Base(p))
		}
	}
	j := &downloadJob{
		id: newJobID(), userID: currentUser(r).ID, shareID: shareID, dirRel: rel,
		url: u.String(), cancel: cancel, status: "running", name: preset, started: time.Now(),
	}
	// 先落库再开下：记录存不下就说明数据库有问题，别让用户以为任务已经记上了
	if err := s.insertDownload(j); err != nil {
		cancel()
		<-downloadSem
		slog.Error("insert download record", "err", err)
		writeError(w, http.StatusInternalServerError, "无法保存下载记录："+shortOSError(err))
		return
	}
	downloadMu.Lock()
	downloadJobs[j.id] = j
	downloadMu.Unlock()

	go func() {
		defer func() { <-downloadSem }()
		name, err := s.downloadToFile(ctx, j, u, hint, dir)
		j.mu.Lock()
		j.finishedAt = time.Now()
		if err != nil {
			// 用户点了「取消」时状态已经是 cancelled，不要覆盖成错误
			if j.status != "cancelled" {
				j.status, j.err = "error", downloadErrText(err)
			}
			tmp := j.tmp
			j.mu.Unlock()
			if err := s.saveDownload(j); err != nil {
				slog.Warn("save download record", "id", j.id, "err", err)
			}
			if tmp != "" {
				os.Remove(tmp)
			}
			if ctx.Err() == nil {
				slog.Warn("download failed", "url", u.Redacted(), "dir", rel, "err", err)
			} else {
				slog.Info("download cancelled", "url", u.Redacted(), "dir", rel)
			}
			return
		}
		j.status, j.name = "done", name
		j.mu.Unlock()
		if err := s.saveDownload(j); err != nil {
			slog.Warn("save download record", "id", j.id, "err", err)
		}
		slog.Info("download done", "url", u.Redacted(), "dir", rel, "name", name,
			"bytes", fmtBytes(j.done.Load()))
	}()
	writeJSON(w, http.StatusOK, map[string]any{"id": j.id, "name": j.name})
}

// downloadToFile 执行一次下载并返回最终文件名。先写隐藏的临时文件，
// 完整下载后才改名，中途取消或失败不会留下半个文件。
func (s *Server) downloadToFile(ctx context.Context, j *downloadJob, u *url.URL, hint, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", downloadUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errStatus(http.StatusBadGateway, "下载链接返回 "+resp.Status)
	}
	if resp.ContentLength > 0 {
		j.total.Store(resp.ContentLength)
	}
	target := uniquePath(dir, downloadFileName(u, resp, hint))
	tmp, err := os.CreateTemp(dir, ".file_linker-dl-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", errStatus(http.StatusForbidden, "没有权限写入该目录")
		}
		return "", err
	}
	j.mu.Lock()
	j.tmp, j.name = tmp.Name(), filepath.Base(target)
	j.mu.Unlock()
	// 记下临时文件路径：进程被强杀后，下次启动能把半个文件清掉
	if err := s.setDownloadTmp(j.id, tmp.Name()); err != nil {
		slog.Warn("record download tmp", "id", j.id, "err", err)
	}

	_, err = io.CopyBuffer(&progressWriter{ctx: ctx, w: tmp, n: &j.done}, resp.Body, make([]byte, 128<<10))
	if err == nil {
		err = tmp.Sync() // 落盘后再改名，避免断电后留下长度不对的文件
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	j.mu.Lock()
	j.tmp = ""
	j.mu.Unlock()
	return filepath.Base(target), nil
}

type progressWriter struct {
	ctx context.Context
	w   io.Writer
	n   *atomic.Int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.n.Add(int64(n))
	return n, err
}

// downloadErrText 把内部错误翻译成给管理员看的提示。
func downloadErrText(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.msg
	}
	if errors.Is(err, context.Canceled) {
		return "下载已取消"
	}
	// net.Error 的原文最有用（超时、DNS 失败、证书错误），直接带上
	return "下载失败：" + err.Error()
}

var extByContentType = map[string]string{
	"application/zip":              ".zip",
	"application/x-7z-compressed":  ".7z",
	"application/x-rar-compressed": ".rar",
	"application/pdf":              ".pdf",
	"application/json":             ".json",
	"application/msword":           ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.ms-powerpoint":                                             ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/octet-stream":                                                  ".bin",
	"application/x-iso9660-image":                                               ".iso",
	"application/vnd.android.package-archive":                                   ".apk",
	"text/plain":       ".txt",
	"text/html":        ".html",
	"image/jpeg":       ".jpg",
	"image/png":        ".png",
	"image/gif":        ".gif",
	"image/webp":       ".webp",
	"audio/mpeg":       ".mp3",
	"audio/flac":       ".flac",
	"video/mp4":        ".mp4",
	"video/x-matroska": ".mkv",
	"video/webm":       ".webm",
	"video/quicktime":  ".mov",
}

// downloadFileName 依次按「用户指定 → Content-Disposition → 链接路径 → 类型推断」取名。
func downloadFileName(u *url.URL, resp *http.Response, hint string) string {
	if hint != "" {
		return hint
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if n := sanitizeName(params["filename"]); n != "" {
				return n
			}
		}
	}
	name := ""
	if p, err := url.PathUnescape(u.Path); err == nil {
		name = sanitizeName(path.Base(p))
	}
	if name == "" {
		name = "下载文件"
	}
	// 链接里没有扩展名（如 /download?id=3）时，按响应类型补一个，方便双击打开
	if filepath.Ext(name) == "" {
		mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if ext, ok := extByContentType[mt]; ok {
			name += ext
		}
	}
	return name
}

// sanitizeName 去掉路径分隔符和各系统不允许的字符，得到一个可直接落盘的文件名。
func sanitizeName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		switch r {
		case '<', '>', ':', '"', '|', '?', '*', '/', '\\':
			return -1
		}
		return r
	}, name)
	name = strings.TrimRight(name, " .")
	if name == "." || name == ".." {
		return ""
	}
	if utf8.RuneCountInString(name) > maxEntryName {
		ext := filepath.Ext(name)
		base := []rune(strings.TrimSuffix(name, ext))
		keep := maxEntryName - utf8.RuneCountInString(ext)
		if keep < 1 {
			keep = 1
		}
		if len(base) > keep {
			base = base[:keep]
		}
		name = string(base) + ext
	}
	return name
}

// uniquePath 在目录 dir 下为 name 找一个不冲突的路径，重名时加「(1)」「(2)」。
func uniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 1; i <= 1000; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s%s", base, newJobID()[:6], ext))
}

// downloadStatus 运行中的任务读内存（进度是实时的），
// 服务重启过或已从内存清理的，直接读数据库里的最终结果。
func (s *Server) downloadStatus(w http.ResponseWriter, r *http.Request) {
	if j := getDownloadJob(r); j != nil {
		j.mu.Lock()
		status, msg, name := j.status, j.err, j.name
		j.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"status": status, "error": msg, "name": name,
			"done": j.done.Load(), "total": j.total.Load(),
		})
		return
	}
	rec, err := s.getDownloadRecord(r.PathValue("id"), currentUser(r).ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "下载任务不存在或已删除")
			return
		}
		slog.Warn("get download record", "err", err)
		writeError(w, http.StatusInternalServerError, "读取下载记录失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": rec.Status, "error": rec.Error, "name": rec.Name,
		"done": rec.Done, "total": rec.Total,
	})
}

// downloadInfo 是任务列表里的一条记录，字段名与前端 DownloadJob 对应。
type downloadInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Status   string `json:"status"` // running / done / error / cancelled
	Error    string `json:"error"`
	ShareID  int64  `json:"share"`
	Dir      string `json:"dir"` // 目标目录，相对共享目录
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Started  int64  `json:"startedAt"`  // Unix 毫秒
	Finished int64  `json:"finishedAt"` // Unix 毫秒，0 表示还没结束
}

// listDownloads 返回当前用户的全部下载记录，最新的在前。
// 记录存在数据库里，正在下载的任务用内存中的实时进度覆盖一下。
func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r).ID
	jobs, err := s.listDownloadRecords(uid)
	if err != nil {
		slog.Warn("list download records", "err", err)
		writeError(w, http.StatusInternalServerError, "读取下载记录失败")
		return
	}
	live := map[string]*downloadJob{}
	downloadMu.Lock()
	for id, j := range downloadJobs {
		if j.userID == uid {
			live[id] = j
		}
	}
	downloadMu.Unlock()
	for i := range jobs {
		j := live[jobs[i].ID]
		if j == nil {
			continue
		}
		j.mu.Lock()
		jobs[i].Name, jobs[i].Status, jobs[i].Error = j.name, j.status, j.err
		jobs[i].Finished = 0
		if !j.finishedAt.IsZero() {
			jobs[i].Finished = j.finishedAt.UnixMilli()
		}
		j.mu.Unlock()
		jobs[i].Done, jobs[i].Total = j.done.Load(), j.total.Load()
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// clearDownloads 清掉已经结束的记录（正在下载的任务保留）。
func (s *Server) clearDownloads(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r).ID
	removed, err := s.deleteEndedDownloads(uid)
	if err != nil {
		slog.Warn("clear download records", "err", err)
		writeError(w, http.StatusInternalServerError, "清除记录失败")
		return
	}
	// 内存里已结束的任务也一起丢掉
	downloadMu.Lock()
	for id, j := range downloadJobs {
		j.mu.Lock()
		ended := j.userID == uid && j.status != "running"
		j.mu.Unlock()
		if ended {
			delete(downloadJobs, id)
		}
	}
	downloadMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// cancelDownload 取消正在下载的任务；对已经结束的任务则表示「删除这条记录」。
// 已经下完的文件不受影响。
func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r).ID
	id := r.PathValue("id")
	if j := getDownloadJob(r); j != nil {
		j.mu.Lock()
		running := j.status == "running"
		if running {
			// 先标记再取消：后台协程收尾时看到 cancelled 就不会把提示改成错误
			j.status, j.err = "cancelled", "已取消"
			j.finishedAt = time.Now()
		}
		tmp := j.tmp
		j.mu.Unlock()
		if running {
			j.cancel()
			if err := s.saveDownload(j); err != nil {
				slog.Warn("save download record", "id", j.id, "err", err)
			}
			if tmp != "" {
				os.Remove(tmp)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// 已经结束，或者内存里已经没有了（服务重启过）：删掉这条记录
	if err := s.deleteDownloadRecord(id, uid); err != nil {
		slog.Warn("delete download record", "err", err)
		writeError(w, http.StatusInternalServerError, "删除下载记录失败")
		return
	}
	downloadMu.Lock()
	delete(downloadJobs, id)
	downloadMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func newJobID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
