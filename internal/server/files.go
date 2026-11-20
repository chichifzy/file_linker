package server

import (
	"context"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 只有这些扩展名的文件可以在浏览器中预览，其他文件只能作为附件下载。
var (
	textExts = setOf("txt", "md", "markdown", "log", "csv", "tsv", "json", "jsonl", "yaml", "yml", "toml", "ini",
		"conf", "cfg", "xml", "html", "htm", "css", "scss", "less", "js", "mjs", "cjs", "jsx", "ts", "tsx", "vue",
		"go", "mod", "sum", "py", "rb", "rs", "java", "kt", "swift", "c", "h", "cc", "cpp", "hpp", "cs", "php",
		"sh", "bash", "zsh", "bat", "cmd", "ps1", "sql", "lua", "r", "dart", "gradle", "properties", "env",
		"gitignore", "dockerfile", "makefile", "srt", "vtt", "tex", "rst", "adoc", "org")
	imageExts = setOf("jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "avif", "ico")
	videoExts = setOf("mp4", "m4v", "webm", "mov", "ogv")
)

// 文本预览最多读取的字节数，前端通过 Range 请求只取开头这一段。
const TextPreviewLimit = 1 << 20

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// fileKind 按扩展名（无扩展名时按文件名，如 Makefile）判断文件类型。
func fileKind(name string) string {
	lower := strings.ToLower(name)
	ext := strings.TrimPrefix(path.Ext(lower), ".")
	if ext == "" {
		ext = strings.TrimPrefix(lower, ".")
	}
	switch {
	case textExts[ext]:
		return "text"
	case imageExts[ext]:
		return "image"
	case videoExts[ext]:
		return "video"
	}
	return "binary"
}

type entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // 相对共享目录，以 / 分隔
	IsDir   bool   `json:"isDir"`
	Kind    string `json:"kind"` // dir / text / image / video / binary
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // Unix 毫秒
}

func newEntry(rel string, fi fs.FileInfo) entry {
	e := entry{Name: fi.Name(), Path: rel, IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime().UnixMilli()}
	if e.IsDir {
		e.Kind, e.Size = "dir", 0
	} else {
		e.Kind = fileKind(e.Name)
	}
	return e
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

// resolve 把客户端给出的相对路径解析为本机绝对路径，并保证结果（含符号链接解析后）仍在共享目录内。
func (s *Server) resolve(r *http.Request) (root, full, rel string, err error) {
	q := r.URL.Query()
	root, err = s.sharePath(r, q.Get("share"))
	if err != nil {
		return
	}
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(q.Get("path"), "\\", "/")), "/")
	full, err = filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		err = errStatus(http.StatusNotFound, "文件不存在")
		return
	}
	if !within(root, full) {
		err = errStatus(http.StatusForbidden, "不能访问共享目录以外的文件")
	}
	return
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func (s *Server) listDir(w http.ResponseWriter, r *http.Request) {
	root, full, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	dirEntries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusForbidden, "无法读取该目录")
		return
	}
	list := []entry{}
	for _, d := range dirEntries {
		if !showHidden && isHidden(d.Name()) {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		// 符号链接显示为目标的类型；指向共享目录以外的链接不显示
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(filepath.Join(full, d.Name()))
			if err != nil || !within(root, target) {
				continue
			}
			if fi, err = os.Stat(target); err != nil {
				continue
			}
		}
		list = append(list, newEntry(path.Join(rel, d.Name()), fi))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsDir != list[j].IsDir {
			return list[i].IsDir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": list})
}

// serveFile 预览只输出白名单类型的文件，其他类型只允许下载；用 ServeContent 流式输出并支持 Range，大文件不会读入内存。
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	_, full, _, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	kind := fileKind(filepath.Base(full))
	download := r.URL.Query().Get("download") == "1"
	// 不支持预览的文件只能作为附件下载，不能在浏览器中直接打开
	if kind == "binary" && !download {
		writeError(w, http.StatusForbidden, "不支持查看该类型的文件")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusForbidden, "无法读取该文件")
		return
	}
	defer f.Close()

	h := w.Header()
	if kind == "text" {
		// 一律按纯文本输出，避免 html/svg 等在本站域名下执行脚本
		h.Set("Content-Type", "text/plain; charset=utf-8")
	} else if kind == "binary" {
		h.Set("Content-Type", "application/octet-stream")
	} else if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(full))); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	// 文本和图片（尤其 svg）按沙箱输出，避免在本站域名下执行脚本。
	// 音视频不会执行脚本，而 iOS Safari 会拒绝播放带 CSP sandbox 的媒体资源。
	if kind == "text" || kind == "image" {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
	}
	h.Set("Cache-Control", "private, no-cache")
	if download {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fi.Name()}))
	}
	// 索引在文件尾（moov 在 mdat 之后）的视频，给播放器一个「索引在前」的虚拟视图：
	// 否则 iOS Safari 那种整段顺序下载的请求，必须读完整个文件才能开始播放。
	// 下载仍然给原文件，保证字节完全一致。
	if kind == "video" && !download {
		if plan, err := faststartPlan(full, fi, f); err == nil && plan != nil {
			h.Set("X-Faststart", "virtual")
			http.ServeContent(w, r, fi.Name(), fi.ModTime(), &faststartReader{f: f, p: plan})
			return
		}
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// MP4 sample entry 里的编码标识。av01 必须在 avc1 之前判断。
var videoCodecMarkers = []struct{ marker, name string }{
	{"av01", "AV1"},
	{"hvc1", "HEVC"},
	{"hev1", "HEVC"},
	{"vp09", "VP9"},
	{"avc1", "H.264"},
}

// videoCodec 读取 MP4 的文件头和文件尾判断视频编码（moov 可能位于任意一端）。
// 只在播放失败时用于说明原因，不参与正常播放。
func videoCodec(full string) string {
	f, err := os.Open(full)
	if err != nil {
		return ""
	}
	defer f.Close()

	var buf []byte
	head := make([]byte, 256<<10)
	if n, _ := io.ReadFull(f, head); n > 0 {
		buf = append(buf, head[:n]...)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 1<<20 {
		tail := make([]byte, 512<<10)
		if _, err := f.ReadAt(tail, fi.Size()-int64(len(tail))); err == nil {
			buf = append(buf, tail...)
		}
	}

	s := string(buf)
	for _, c := range videoCodecMarkers {
		if strings.Contains(s, c.marker) {
			return c.name
		}
	}
	return ""
}

// fileCodec 返回视频编码，供前端在播放失败时给出准确原因。
func (s *Server) fileCodec(w http.ResponseWriter, r *http.Request) {
	_, full, _, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"codec": videoCodec(full)})
}

const dirInfoTimeout = 15 * time.Second
// 同时进行的文件夹大小统计数，避免多人同时统计大目录拖慢磁盘
var dirInfoSem = make(chan struct{}, 2)

// dirInfo 统计文件夹的占用大小（文件大小之和，含隐藏文件）、文件数和子文件夹数。
// 不跟随符号链接；超时或客户端断开时停止，返回已统计的部分并标记 truncated。
func (s *Server) dirInfo(w http.ResponseWriter, r *http.Request) {
	_, full, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dirInfoTimeout)
	defer cancel()
	select {
	case dirInfoSem <- struct{}{}:
		defer func() { <-dirInfoSem }()
	case <-ctx.Done():
		writeError(w, http.StatusServiceUnavailable, "服务器忙，请稍后再试")
		return
	}

	var size, files, dirs int64
	truncated := false
	filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			truncated = true
			return filepath.SkipAll
		}
		if err != nil || p == full {
			return nil
		}
		switch {
		case d.IsDir():
			dirs++
		case d.Type().IsRegular():
			if info, err := d.Info(); err == nil {
				files++
				size += info.Size()
			}
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"name": fi.Name(), "path": rel, "modTime": fi.ModTime().UnixMilli(),
		"size": size, "files": files, "dirs": dirs, "truncated": truncated,
	})
}

const (
	searchLimit   = 200
	searchTimeout = 5 * time.Second
)

// search 在共享目录（或其子目录）中按文件名查找，不跟随符号链接。
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	_, full, rel, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeError(w, http.StatusBadRequest, "请输入搜索关键字")
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	list := []entry{}
	truncated := false
	filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil || len(list) >= searchLimit {
			truncated = true
			return filepath.SkipAll
		}
		if err != nil || p == full {
			return nil
		}
		if !showHidden && isHidden(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !strings.Contains(strings.ToLower(d.Name()), q) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		sub, err := filepath.Rel(full, p)
		if err != nil {
			return nil
		}
		list = append(list, newEntry(path.Join(rel, filepath.ToSlash(sub)), fi))
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"entries": list, "truncated": truncated})
}
