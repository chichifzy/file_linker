package server

// 上传文件到当前目录。文件以原始请求体流式写入同目录的临时文件，
// 写完再原子落位；跨设备只要能勾选文件的浏览器都能用（iOS / Windows / macOS）。

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 上传请求带一个自定义头：跨站表单发不出它，配合 SameSite Cookie 防 CSRF。
const uploadHeader = "X-File-Linker-Upload"

// 单个文件的上限。比这更大的文件请放到共享目录里，或用创建下载任务。
const maxUploadBytes = 8 << 30 // 8 GiB

type uploadConflict struct {
	Name     string `json:"name"`
	Proposed string `json:"proposed"`
}

// uploadDir 解析上传目标目录；根目录（path 为空）也允许。
func (s *Server) uploadDir(r *http.Request) (dir string, err error) {
	_, dir, _, err = s.resolve(r)
	if err != nil {
		return "", err
	}
	fi, statErr := os.Stat(dir)
	if statErr != nil || !fi.IsDir() {
		return "", errStatus(http.StatusBadRequest, "上传位置不是文件夹")
	}
	return dir, nil
}

// findUploadConflict 返回重名时建议使用的名字。
func findUploadConflict(dir, name string) (string, bool) {
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); err == nil {
		return filepath.Base(uniquePath(dir, name)), true
	} else if !errors.Is(err, os.ErrNotExist) {
		// 读不到状态时也别继续写，免得覆盖用户看不到的东西
		return filepath.Base(uniquePath(dir, name)), true
	}
	return "", false
}

// checkUpload 在真正上传前先问一遍目标目录里有没有重名。
// 这样「取消」时不会已经传了一半文件；同一批里选了两个同名文件也算冲突。
func (s *Server) checkUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Names []string `json:"names"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Names) == 0 {
		writeError(w, http.StatusBadRequest, "请先选择文件")
		return
	}
	if len(in.Names) > 200 {
		writeError(w, http.StatusBadRequest, "一次最多上传 200 个文件")
		return
	}
	dir, err := s.uploadDir(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seen := map[string]bool{}
	// 一定要初始化成空数组：nil slice 会序列化成 null，前端读 length 会炸
	conflicts := []uploadConflict{}
	for _, raw := range in.Names {
		name, cerr := cleanEntryName(raw)
		if cerr != nil {
			fail(w, r, cerr)
			return
		}
		if seen[name] {
			conflicts = append(conflicts, uploadConflict{Name: name, Proposed: filepath.Base(uniquePath(dir, name))})
			continue
		}
		seen[name] = true
		if proposed, ok := findUploadConflict(dir, name); ok {
			conflicts = append(conflicts, uploadConflict{Name: name, Proposed: proposed})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": conflicts})
}

// uploadFile 接收原始请求体并写到当前目录。rename=1 时自动加序号，不覆盖已有文件。
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(uploadHeader) != "1" {
		writeError(w, http.StatusForbidden, "上传请求无效")
		return
	}
	if n := r.ContentLength; n > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "单个文件不能超过 8 GB")
		return
	}
	name, err := cleanEntryName(r.URL.Query().Get("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	dir, err := s.uploadDir(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	rename := r.URL.Query().Get("rename") == "1"
	target := filepath.Join(dir, name)
	if !rename {
		if proposed, ok := findUploadConflict(dir, name); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "这里已经有同名文件了", "code": "name_conflict", "name": name, "proposed": proposed,
			})
			return
		}
	} else {
		target = uniquePath(dir, name)
	}

	// 临时文件放在目标目录里，落位时是同一文件系统内的原子操作。
	tmp, err := os.CreateTemp(dir, ".file_linker-up-*")
	if err != nil {
		internalError(w, r, err)
		return
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		_ = os.Remove(tmpName)
	}()
	_ = os.Chmod(tmpName, 0o644)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if _, err := io.Copy(tmp, r.Body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "单个文件不能超过 8 GB")
		} else {
			internalError(w, r, err)
		}
		return
	}
	if err := tmp.Sync(); err != nil {
		internalError(w, r, err)
		return
	}
	if err := tmp.Close(); err != nil {
		internalError(w, r, err)
		return
	}

	// 传输过程中别人可能刚放了同名文件进来，落位前再看一眼。
	if !rename {
		if proposed, ok := findUploadConflict(dir, name); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "这里已经有同名文件了", "code": "name_conflict", "name": name, "proposed": proposed,
			})
			return
		}
	} else {
		target = uniquePath(dir, name)
	}
	if err := installUpload(tmpName, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "这里已经有同名文件了", "code": "name_conflict", "name": name,
				"proposed": filepath.Base(uniquePath(dir, name)),
			})
			return
		}
		internalError(w, r, err)
		return
	}

	rel := filepath.ToSlash(strings.TrimPrefix(filepath.Dir(target), dir))
	final := filepath.Base(target)
	var size int64
	if fi, serr := os.Stat(target); serr == nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name": final,
		"path": strings.TrimPrefix(rel+"/"+final, "/"),
		"size": size,
	})
}

// installUpload 把临时文件放到 target，绝不覆盖已有文件。
// 先试硬链接（Unix / NTFS 都支持，且是原子的），不支持的 filesystem 退回 O_EXCL 复制。
func installUpload(tmp, target string) error {
	if err := os.Link(tmp, target); err == nil {
		_ = os.Remove(tmp)
		return nil
	} else if !errors.Is(err, os.ErrExist) && !isLinkUnsupported(err) {
		return err
	}
	in, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	_ = os.Remove(tmp)
	return nil
}

func isLinkUnsupported(err error) bool {
	// Windows 上部分网络盘不支持硬链接；Go 没有统一的错误码，按文本兜底。
	msg := err.Error()
	return strings.Contains(msg, "not supported") || strings.Contains(msg, "不支持") || strings.Contains(msg, "Invalid function")
}
