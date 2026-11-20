package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "session"
	sessionTTL    = 7 * 24 * time.Hour
)

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

type User struct {
	ID       int64  `json:"-"`
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"`
}

func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

type ctxKey struct{}

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(ctxKey{}).(*User)
	return u
}

var uidPattern = regexp.MustCompile(`^[A-Za-z0-9]{3,20}$`)

// ValidateUID 校验用户 ID：3-20 位英文字母或数字。
func ValidateUID(uid string) string {
	if !uidPattern.MatchString(uid) {
		return "用户 ID 需为 3-20 位英文字母或数字"
	}
	return ""
}

func ValidateNickname(nickname string) string {
	if n := utf8.RuneCountInString(nickname); n < 1 || n > 32 {
		return "用户名需为 1-32 个字符"
	}
	return ""
}

func ValidatePassword(password string) string {
	if len(password) < 8 || len(password) > 72 { // bcrypt 最多处理 72 字节
		return "密码需为 8-72 个字符"
	}
	return ""
}

// 用户不存在时也做一次 bcrypt 比较，避免通过响应时间判断用户 ID 是否存在。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.DefaultCost)

// register 使用邀请码注册普通用户，邀请码在同一事务中被消耗。
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UID        string `json:"uid"`
		Nickname   string `json:"nickname"`
		Password   string `json:"password"`
		InviteCode string `json:"inviteCode"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.UID = strings.TrimSpace(in.UID)
	in.Nickname = strings.TrimSpace(in.Nickname)
	in.InviteCode = strings.ToUpper(strings.TrimSpace(in.InviteCode))
	for _, msg := range []string{ValidateUID(in.UID), ValidateNickname(in.Nickname), ValidatePassword(in.Password)} {
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	if in.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "请输入邀请码")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		internalError(w, r, err)
		return
	}
	var id int64
	err = s.withTx(r.Context(), func(tx *sql.Tx) error {
		var ok bool
		if err := tx.QueryRowContext(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM invites WHERE code = ? AND used_at IS NULL AND expires_at > ?)`,
			in.InviteCode, time.Now().Unix()).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errStatus(http.StatusBadRequest, "邀请码无效、已使用或已过期")
		}
		res, err := tx.ExecContext(r.Context(),
			`INSERT INTO users (uid, nickname, password_hash, role) VALUES (?, ?, ?, 'member')
			 ON CONFLICT (uid) DO NOTHING`, in.UID, in.Nickname, string(hash))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errStatus(http.StatusConflict, "用户 ID 已被使用")
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(r.Context(),
			`UPDATE invites SET used_by = ?, used_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE code = ?`,
			id, in.InviteCode)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UID      string `json:"uid"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var id int64
	var hash string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, password_hash FROM users WHERE uid = ?`,
		strings.TrimSpace(in.UID)).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
		writeError(w, http.StatusUnauthorized, "用户 ID 或密码错误")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "用户 ID 或密码错误")
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.db.ExecContext(r.Context(),
			`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value)); err != nil {
			internalError(w, r, err)
			return
		}
	}
	s.setCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// me 返回当前用户、请求是否来自运行服务的本机，以及能否修改共享目录。
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	canEdit, err := s.canEditShares(r)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*User
		IsLocal       bool `json:"isLocal"`
		CanEditShares bool `json:"canEditShares"`
	}{currentUser(r), isLocalRequest(r), canEdit})
}

// changePassword 校验原密码后修改密码。当前会话保持有效，其他设备上的会话全部失效。
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := ValidatePassword(in.NewPassword); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	u := currentUser(r)
	var hash string
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		internalError(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.OldPassword)) != nil {
		writeError(w, http.StatusBadRequest, "原密码错误")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		internalError(w, r, err)
		return
	}
	c, _ := r.Cookie(sessionCookie) // requireUser 已保证存在
	err = s.withTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE users SET password_hash = ? WHERE id = ?`, string(newHash), u.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(),
			`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, u.ID, hashToken(c.Value))
		return err
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	// 顺手清理过期会话
	if _, err := s.db.ExecContext(r.Context(),
		`DELETE FROM sessions WHERE expires_at < ?`, now.Unix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, now.Add(sessionTTL).Unix()); err != nil {
		return err
	}
	s.setCookie(w, token, int(sessionTTL.Seconds()))
	return nil
}

func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// requireUser 校验会话，把当前用户放进 context。
func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var u User
		err = s.db.QueryRowContext(r.Context(),
			`SELECT u.id, u.uid, u.nickname, u.role
			 FROM sessions s JOIN users u ON u.id = s.user_id
			 WHERE s.token_hash = ? AND s.expires_at > ?`,
			hashToken(c.Value), time.Now().Unix()).
			Scan(&u.ID, &u.UID, &u.Nickname, &u.Role)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, &u)))
	})
}

// requireAdmin 只允许管理员访问。
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			writeError(w, http.StatusForbidden, "没有权限执行此操作")
			return
		}
		next(w, r)
	})
}

// requireShareAdmin 只允许有权修改共享目录的管理员访问：本机，或本机已开启“允许在其它设备更改共享目录”。
func (s *Server) requireShareAdmin(next http.HandlerFunc) http.Handler {
	return s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.canEditShares(r)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, http.StatusForbidden, "只能在运行服务的电脑上修改共享目录")
			return
		}
		next(w, r)
	})
}

// requireLocalAdmin 只允许在运行服务的本机上登录的管理员访问。
func (s *Server) requireLocalAdmin(next http.HandlerFunc) http.Handler {
	return s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalRequest(r) {
			writeError(w, http.StatusForbidden, "只能在运行服务的电脑上修改共享目录")
			return
		}
		next(w, r)
	})
}
