// Package server 提供 JSON API 和前端静态文件。
package server

import (
	"database/sql"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type Server struct {
	db            *sql.DB
	secureCookie  bool
	port          string // 监听端口，用于生成分享地址
	shutdownToken string // 停止服务的令牌，为空时禁用 /api/shutdown
	stop          func() // 停止服务的回调，由 main 提供
}

// Options 是 New 的配置。
type Options struct {
	SecureCookie  bool
	Port          string // 监听端口，用于生成分享地址
	ShutdownToken string // 非空时允许本机用该令牌停止服务
	Shutdown      func()
}

func New(db *sql.DB, static fs.FS, opt Options) http.Handler {
	s := &Server{db: db, secureCookie: opt.SecureCookie, port: opt.Port,
		shutdownToken: opt.ShutdownToken, stop: opt.Shutdown}
	m := http.NewServeMux()

	// 启动脚本用 health 确认端口上是本项目，用 shutdown 在新窗口按 Ctrl+C 时停止服务
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("POST /api/shutdown", s.shutdown)

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
	// 列表里的视频缩略图：服务端抽帧成小图并缓存，避免几十个 <video> 抢连接
	initThumbs()
	m.Handle("GET /api/fs/thumb", s.requireUser(s.serveThumb))
	m.Handle("GET /api/fs/codec", s.requireUser(s.fileCodec))
	m.Handle("GET /api/fs/search", s.requireUser(s.search))
	m.Handle("GET /api/fs/dirinfo", s.requireUser(s.dirInfo))
	// 写入共享目录（仅管理员）：新建文件夹、把链接指向的文件下载到当前目录、上传本机的文件
	m.Handle("POST /api/fs/mkdir", s.requireAdmin(s.createDir))
	// 上传前预检重名，真正上传时流式接收原始请求体
	m.Handle("POST /api/fs/upload/check", s.requireAdmin(s.checkUpload))
	m.Handle("POST /api/fs/upload", s.requireAdmin(s.uploadFile))
	// 重命名、删除（删除是移到系统回收站 / 废纸篓）
	m.Handle("POST /api/fs/rename", s.requireAdmin(s.renameEntry))
	m.Handle("POST /api/fs/delete", s.requireAdmin(s.deleteEntry))
	// 粘贴：把之前拷贝 / 剪切的那一项复制或移动到当前目录
	m.Handle("POST /api/fs/paste", s.requireAdmin(s.pasteEntry))
	// 解压 zip / 7z 到压缩包所在的当前目录
	initExtract()
	m.Handle("POST /api/fs/extract", s.requireAdmin(s.createExtract))
	m.Handle("GET /api/fs/extract/{id}", s.requireAdmin(s.extractStatus))
	m.Handle("DELETE /api/fs/extract/{id}", s.requireAdmin(s.cancelExtract))
	initDownloads()
	// 上次运行中没下完的任务已经随进程一起没了，标记成中断，免得列表里一直显示「下载中」
	s.markInterruptedDownloads()
	m.Handle("POST /api/fs/download", s.requireAdmin(s.createDownload))
	// 任务列表：侧边栏「任务列表」页，关掉弹窗后也能回来看进度
	m.Handle("GET /api/fs/downloads", s.requireAdmin(s.listDownloads))
	m.Handle("DELETE /api/fs/downloads", s.requireAdmin(s.clearDownloads))
	m.Handle("GET /api/fs/download/{id}", s.requireAdmin(s.downloadStatus))
	m.Handle("DELETE /api/fs/download/{id}", s.requireAdmin(s.cancelDownload))
	initZip()
	m.Handle("POST /api/fs/zip", s.requireUser(s.createZip))
	m.Handle("GET /api/fs/zip/{id}", s.requireUser(s.zipStatus))
	m.Handle("GET /api/fs/zip/{id}/download", s.requireUser(s.downloadZip))
	m.Handle("DELETE /api/fs/zip/{id}", s.requireUser(s.deleteZip))

	// 视频转码（浏览器解不了的编码转成 H.264）。缓存持久保留，边转边播
	s.initTranscode()
	m.Handle("POST /api/fs/transcode", s.requireUser(s.createTranscode))
	// 播放前只查一次有没有现成缓存（不创建任务），命中就直接播缓存
	m.Handle("GET /api/fs/transcode/lookup", s.requireUser(s.lookupTranscode))
	m.Handle("GET /api/fs/transcode/{id}", s.requireUser(s.transcodeStatus))
	m.Handle("GET /api/fs/transcode/{id}/file", s.requireUser(s.serveTranscode))
	m.Handle("GET /api/fs/transcode/{id}/hls/{name}", s.requireUser(s.serveHLSFile))
	m.Handle("GET /api/fs/media", s.requireUser(s.listMedia))
	m.Handle("DELETE /api/fs/media/{id}", s.requireUser(s.deleteMedia))
	m.Handle("DELETE /api/fs/media", s.requireUser(s.clearMedia))
	// 缓存目录可以改到别的盘（比如 C 盘空间不够），已有缓存会一起迁过去
	m.Handle("POST /api/admin/media-dir", s.requireMediaDirAdmin(s.setMediaDir))

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
	n      int64 // 已经写出的字节数，用于发现「客户端在整段下载」这类异常
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.n += int64(n)
	return n, err
}

// ReadFrom 保留底层 ResponseWriter 的零拷贝路径（文件→socket），
// 包一层 Write 统计字节数而不牺牲大文件吞吐。
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	n, err := io.Copy(r.ResponseWriter, src)
	r.n += n
	return n, err
}

// Unwrap 让 http.ResponseController 等能拿到原始 ResponseWriter。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		// 视频播放和 HLS 分段会产生大量请求，只记录其他接口；这些请求仅在失败时记录，便于排查播放问题
		if strings.HasPrefix(r.URL.Path, "/api/") && (!quietPath(r.URL.Path) || rec.status >= 400) {
			slog.Info("request", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration", dur)
		}
		// 正常开播只要 20~30MB，但客户端一旦退化成「整段下载」或卡住，
		// 这个请求会拉走整个文件、持续几十秒。留一行摘要，方便对照手机端的表现
		if r.URL.Path == "/api/fs/file" && (rec.n >= 20<<20 || dur >= 3*time.Second) {
			slog.Info("media",
				"file", filepath.Base(r.URL.Query().Get("path")),
				"range", r.Header.Get("Range"),
				"status", rec.status,
				"bytes", fmtBytes(rec.n),
				"duration", dur.Round(time.Millisecond),
				"ua", shortUA(r.UserAgent()))
		}
	})
}

// shortUA 从 User-Agent 里摘出设备和浏览器，整串太长。
func shortUA(ua string) string {
	var parts []string
	for _, k := range []string{"iPhone", "iPad", "Android", "Macintosh", "Windows"} {
		if strings.Contains(ua, k) {
			parts = append(parts, k)
			break
		}
	}
	for _, k := range []string{"Edg", "Chrome", "Safari", "Firefox"} {
		if strings.Contains(ua, k) {
			parts = append(parts, k)
			break
		}
	}
	if len(parts) == 0 {
		if len(ua) > 40 {
			return ua[:40]
		}
		return ua
	}
	return strings.Join(parts, "/")
}

// quietPath 标记高频的媒体请求，成功时不写日志。
func quietPath(p string) bool {
	return p == "/api/health" || p == "/api/fs/file" || p == "/api/fs/thumb" ||
		strings.HasPrefix(p, "/api/fs/transcode/")
}
