package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// 是否允许管理员在其他设备上修改共享目录，默认不允许。
const settingRemoteShareEdit = "remote_share_edit"

func (s *Server) remoteShareEdit(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingRemoteShareEdit).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "1", err
}

// canEditShares 判断当前管理员能否修改共享目录：本机始终可以，其他设备需本机开启开关。
func (s *Server) canEditShares(r *http.Request) (bool, error) {
	if !currentUser(r).IsAdmin() {
		return false, nil
	}
	if isLocalRequest(r) {
		return true, nil
	}
	return s.remoteShareEdit(r.Context())
}

func (s *Server) getRemoteShareEdit(w http.ResponseWriter, r *http.Request) {
	on, err := s.remoteShareEdit(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (s *Server) setRemoteShareEdit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	v := "0"
	if in.Enabled {
		v = "1"
	}
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingRemoteShareEdit, v); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
