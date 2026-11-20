package server

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type share struct {
	ID    int64       `json:"id"`
	Name  string      `json:"name"`
	Path  string      `json:"path,omitempty"` // 只返回给管理员
	Quick []quickLink `json:"quick"`          // 位于该共享目录内的常用文件夹
}

type quickLink struct {
	Name string `json:"name"`
	Path string `json:"path"` // 相对共享目录，以 / 分隔
}

// listShares 所有登录用户都能看到共享目录列表；本机路径只返回给管理员。
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	list, err := s.shares(r)
	if err != nil {
		internalError(w, r, err)
		return
	}
	folders := quickFolders()
	for i := range list {
		list[i].Quick = quickLinksIn(list[i].Path, folders)
		if !currentUser(r).IsAdmin() {
			list[i].Path = ""
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// quickFolders 返回本机存在的常用文件夹（已解析符号链接）：macOS 为下载、桌面、文稿，Windows 为桌面、下载。
func quickFolders() []quickLink {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var candidates []quickLink
	switch runtime.GOOS {
	case "darwin":
		candidates = []quickLink{{"下载", "Downloads"}, {"桌面", "Desktop"}, {"文稿", "Documents"}}
	case "windows":
		candidates = []quickLink{{"桌面", "Desktop"}, {"下载", "Downloads"}}
	}
	var list []quickLink
	for _, c := range candidates {
		p, err := filepath.EvalSymlinks(filepath.Join(home, c.Path))
		if err != nil {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			list = append(list, quickLink{c.Name, p})
		}
	}
	return list
}

// quickLinksIn 筛出位于共享目录 root 内的常用文件夹，路径转为相对 root。
func quickLinksIn(root string, folders []quickLink) []quickLink {
	list := []quickLink{}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return list
	}
	for _, f := range folders {
		if !within(root, f.Path) {
			continue
		}
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			continue
		}
		if rel = filepath.ToSlash(rel); rel == "." {
			rel = ""
		}
		list = append(list, quickLink{f.Name, rel})
	}
	return list
}

func (s *Server) shares(r *http.Request) ([]share, error) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, name, path FROM shares ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []share{}
	for rows.Next() {
		var v share
		if err := rows.Scan(&v.ID, &v.Name, &v.Path); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// sharePath 返回共享目录在本机上的绝对路径。
func (s *Server) sharePath(r *http.Request, id string) (string, error) {
	var p string
	err := s.db.QueryRowContext(r.Context(), `SELECT path FROM shares WHERE id = ?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errStatus(http.StatusNotFound, "共享目录不存在")
	}
	return p, err
}

func (s *Server) addShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := cleanHostDir(in.Path)
	if err != nil {
		fail(w, r, err)
		return
	}
	name := filepath.Base(p)
	if name == string(filepath.Separator) || name == "." || strings.HasSuffix(name, ":\\") || name == "" {
		name = p
	}
	res, err := s.db.ExecContext(r.Context(),
		`INSERT INTO shares (path, name) VALUES (?, ?) ON CONFLICT (path) DO NOTHING`, p, name)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusConflict, "该目录已共享")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) removeShare(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM shares WHERE id = ?`, id); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanHostDir 把路径转换为解析过符号链接的绝对路径，并确认它是目录。
func cleanHostDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return "", errStatus(http.StatusBadRequest, "请提供绝对路径")
	}
	p, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", errStatus(http.StatusBadRequest, "目录不存在或无法访问")
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		return "", errStatus(http.StatusBadRequest, "目录不存在或无法访问")
	}
	return p, nil
}

type hostDir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// hostDirs 供本机管理员挑选共享目录：path 为空时返回常用起点，否则返回其子目录。
func (s *Server) hostDirs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "parent": nil, "dirs": hostRoots()})
		return
	}
	p, err := cleanHostDir(p)
	if err != nil {
		fail(w, r, err)
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		writeError(w, http.StatusForbidden, "无法读取该目录")
		return
	}
	dirs := []hostDir{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(p, e.Name())
		if e.IsDir() {
			dirs = append(dirs, hostDir{e.Name(), full})
		} else if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(full); err == nil && fi.IsDir() {
				dirs = append(dirs, hostDir{e.Name(), full})
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	var parent *string
	if pp := filepath.Dir(p); pp != p {
		parent = &pp
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}

// hostRoots 主目录、系统根目录和外接磁盘。
func hostRoots() []hostDir {
	var out []hostDir
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, hostDir{"主目录", home})
	}
	switch runtime.GOOS {
	case "windows":
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				out = append(out, hostDir{d, d})
			}
		}
	case "darwin":
		out = append(out, hostDir{"/", "/"})
		if entries, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range entries {
				out = append(out, hostDir{e.Name(), filepath.Join("/Volumes", e.Name())})
			}
		}
	default:
		out = append(out, hostDir{"/", "/"})
	}
	return out
}
