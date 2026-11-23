package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"time"
)

// health 不需要登录：启动脚本用它确认端口上运行的确实是本项目。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"app": "file_linker"})
}

// shutdown 只接受本机请求，并校验启动时随机生成的令牌，
// 用于在另一个终端窗口按 Ctrl+C 时停止正在运行的服务。
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	if s.stop == nil || s.shutdownToken == "" {
		writeError(w, http.StatusNotImplemented, "当前版本不支持远程停止")
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	addr := net.ParseIP(ip)
	if err != nil || addr == nil || !addr.IsLoopback() {
		writeError(w, http.StatusForbidden, "只允许本机请求")
		return
	}
	got := r.Header.Get("X-File-Linker-Shutdown")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.shutdownToken)) != 1 {
		writeError(w, http.StatusForbidden, "令牌不正确")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
	// 等响应写完再停，避免客户端拿到连接重置
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.stop()
	}()
}
