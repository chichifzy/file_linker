// Package server 提供 JSON API 和前端静态文件。
package server

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

type Server struct {
	db           *sql.DB
	secureCookie bool
	port         string // 监听端口，用于生成分享地址
}

func New(db *sql.DB, static fs.FS, secureCookie bool, port string) http.Handler {
	s := &Server{db: db, secureCookie: secureCookie, port: port}
	m := http.NewServeMux()

	// 认证与个人信息
	m.HandleFunc("POST /api/auth/register", s.register)
	m.HandleFunc("POST /api/auth/login", s.login)
	m.HandleFunc("POST /api/auth/logout", s.logout)
	m.Handle("GET /api/me", s.requireUser(s.me))
	m.Handle("PUT /api/me/password", s.requireUser(s.changePassword))
	m.Handle("GET /api/netinfo", s.requireUser(s.netInfo))

	// 文件浏览（只读）
	m.Handle("GET /api/shares", s.requireUser(s.listShares))
	m.Handle("GET /api/fs/list", s.requireUser(s.listDir))
	m.Handle("GET /api/fs/file", s.requireUser(s.serveFile))
	m.Handle("GET /api/fs/search", s.requireUser(s.search))
	m.Handle("GET /api/fs/dirinfo", s.requireUser(s.dirInfo))
	initZip()
	m.Handle("POST /api/fs/zip", s.requireUser(s.createZip))
	m.Handle("GET /api/fs/zip/{id}", s.requireUser(s.zipStatus))
	m.Handle("GET /api/fs/zip/{id}/download", s.requireUser(s.downloadZip))
	m.Handle("DELETE /api/fs/zip/{id}", s.requireUser(s.deleteZip))

	// 管理员：邀请码
	m.Handle("GET /api/admin/invites", s.requireAdmin(s.listInvites))
	m.Handle("POST /api/admin/invites", s.requireAdmin(s.createInvite))
	m.Handle("DELETE /api/admin/invites/{code}", s.requireAdmin(s.deleteInvite))

	// 管理员：共享目录（本机，或本机允许后的其他设备）
	m.Handle("POST /api/admin/shares", s.requireShareAdmin(s.addShare))
	m.Handle("DELETE /api/admin/shares/{id}", s.requireShareAdmin(s.removeShare))
	m.Handle("GET /api/admin/host-dirs", s.requireShareAdmin(s.hostDirs))

	// 管理员（仅限本机）：是否允许在其他设备修改共享目录
	m.Handle("GET /api/admin/remote-share-edit", s.requireLocalAdmin(s.getRemoteShareEdit))
	m.Handle("PUT /api/admin/remote-share-edit", s.requireLocalAdmin(s.setRemoteShareEdit))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})
	m.Handle("/", spaHandler(static))

	return logRequests(m)
}

// spaHandler 提供前端构建产物；找不到的路径返回 index.html，交给 React Router 处理。
func spaHandler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(dist, p); err != nil {
			index, err := fs.ReadFile(dist, "index.html")
			if err != nil {
				http.Error(w, "前端尚未构建：请先运行 make web", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(index)
			return
		}
		// Vite 产物文件名带内容哈希，可以长期缓存
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap 让 http.ResponseController 等能拿到原始 ResponseWriter。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// 视频拖动会产生大量文件请求，只记录其他接口
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/fs/file" {
			slog.Info("request", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration", time.Since(start))
		}
	})
}
