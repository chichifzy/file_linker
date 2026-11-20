package server

import (
	"context"
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

// 只有这些扩展名的文件可以读取内容，其他文件只显示基础信息，服务端永远不打开它们。
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

// serveFile 只输出白名单类型的文件；用 ServeContent 流式输出并支持 Range，大文件不会读入内存。
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	_, full, _, err := s.resolve(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	kind := fileKind(filepath.Base(full))
	if kind == "binary" {
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
	} else if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(full))); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
	h.Set("Cache-Control", "private, no-cache")
	if r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fi.Name()}))
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
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
