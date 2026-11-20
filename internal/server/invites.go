package server

import (
	"crypto/rand"
	"database/sql"
	"net/http"
	"time"
)

const inviteTTL = 7 * 24 * time.Hour

// 去掉容易混淆的 0/O、1/I/L
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

type invite struct {
	Code      string  `json:"code"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt int64   `json:"expiresAt"`
	UsedBy    *string `json:"usedBy"` // 使用者的用户 ID
	UsedAt    *string `json:"usedAt"`
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT i.code, i.created_at, i.expires_at, u.uid, i.used_at
		FROM invites i LEFT JOIN users u ON u.id = i.used_by
		ORDER BY i.created_at DESC LIMIT 200`)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer rows.Close()
	list := []invite{}
	for rows.Next() {
		var v invite
		var usedBy, usedAt sql.NullString
		if err := rows.Scan(&v.Code, &v.CreatedAt, &v.ExpiresAt, &usedBy, &usedAt); err != nil {
			internalError(w, r, err)
			return
		}
		if usedBy.Valid {
			v.UsedBy = &usedBy.String
		}
		if usedAt.Valid {
			v.UsedAt = &usedAt.String
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	code := make([]byte, 8)
	for i := range code {
		code[i] = inviteAlphabet[randIntn(len(inviteAlphabet))]
	}
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO invites (code, expires_at) VALUES (?, ?)`,
		string(code), time.Now().Add(inviteTTL).Unix()); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"code": string(code)})
}

// deleteInvite 只能删除未使用的邀请码；已使用的保留作记录。
func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM invites WHERE code = ? AND used_at IS NULL`, r.PathValue("code"))
	if err != nil {
		internalError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "邀请码不存在或已使用")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// randIntn 用 crypto/rand 生成 [0, n) 的随机数（拒绝采样避免取模偏差）。
func randIntn(n int) int {
	limit := 256 - 256%n
	b := make([]byte, 1)
	for {
		rand.Read(b)
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}
