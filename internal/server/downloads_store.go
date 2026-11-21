package server

// 下载记录的持久化：写在 SQLite 的 downloads 表里，只有用户主动删除才会消失。
// 运行中的任务进度仍以内存为准（数据库只在任务开始、结束时各写一次）；
// 服务重启时把残留的 running 记录标成「已中断」，避免列表里一直显示下载中。

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const downloadInfoColumns = `id, name, url, status, error, share_id, dir, done, total, started_at, finished_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanDownloadInfo(row rowScanner) (downloadInfo, error) {
	var d downloadInfo
	err := row.Scan(&d.ID, &d.Name, &d.URL, &d.Status, &d.Error, &d.ShareID, &d.Dir, &d.Done, &d.Total, &d.Started, &d.Finished)
	return d, err
}

// insertDownload 在任务开始下载前落库。失败时调用方要中止这次下载，
// 否则用户会看到一个「点过但没有记录」的任务。
func (s *Server) insertDownload(j *downloadJob) error {
	_, err := s.db.Exec(
		`INSERT INTO downloads (id, user_id, share_id, dir, url, name, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		j.id, j.userID, j.shareID, j.dirRel, j.url, j.name, j.status, j.started.UnixMilli())
	return err
}

// saveDownload 把任务当前的名字、状态、字节数写回数据库（结束时调用一次）。
func (s *Server) saveDownload(j *downloadJob) error {
	j.mu.Lock()
	name, status, msg := j.name, j.status, j.err
	finished := int64(0)
	if !j.finishedAt.IsZero() {
		finished = j.finishedAt.UnixMilli()
	}
	j.mu.Unlock()
	// 任务结束后临时文件已经不在了，顺便把路径清掉
	_, err := s.db.Exec(
		`UPDATE downloads SET name = ?, status = ?, error = ?, done = ?, total = ?, finished_at = ?, tmp = '' WHERE id = ?`,
		name, status, msg, j.done.Load(), j.total.Load(), finished, j.id)
	return err
}

// setDownloadTmp 记下正在写入的临时文件，进程被强杀后重启时好清理。
func (s *Server) setDownloadTmp(id, tmp string) error {
	_, err := s.db.Exec(`UPDATE downloads SET tmp = ? WHERE id = ?`, tmp, id)
	return err
}

// listDownloadRecords 按开始时间倒序返回某个用户的全部下载记录。
func (s *Server) listDownloadRecords(uid int64) ([]downloadInfo, error) {
	rows, err := s.db.Query(
		`SELECT `+downloadInfoColumns+` FROM downloads WHERE user_id = ? ORDER BY started_at DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]downloadInfo, 0, 16)
	for rows.Next() {
		d, err := scanDownloadInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Server) getDownloadRecord(id string, uid int64) (downloadInfo, error) {
	return scanDownloadInfo(s.db.QueryRow(
		`SELECT `+downloadInfoColumns+` FROM downloads WHERE id = ? AND user_id = ?`, id, uid))
}

func (s *Server) deleteDownloadRecord(id string, uid int64) error {
	_, err := s.db.Exec(`DELETE FROM downloads WHERE id = ? AND user_id = ?`, id, uid)
	return err
}

// deleteEndedDownloads 删掉已经结束的记录，正在下载的保留。
func (s *Server) deleteEndedDownloads(uid int64) (int, error) {
	res, err := s.db.Exec(`DELETE FROM downloads WHERE user_id = ? AND status <> 'running'`, uid)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// markInterruptedDownloads 在服务启动时调用：上次没下完的任务已经随进程一起没了，
// 把它留下的半个文件删掉，再把记录标成中断。
func (s *Server) markInterruptedDownloads() {
	// 只删自己创建的临时文件（文件名有固定前缀），避免数据库被改坏时误删别的文件
	rows, err := s.db.Query(`SELECT tmp FROM downloads WHERE status = 'running' AND tmp <> ''`)
	if err == nil {
		var tmps []string
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && strings.HasPrefix(filepath.Base(p), ".file_linker-dl-") {
				tmps = append(tmps, p)
			}
		}
		rows.Close()
		// 连接池只有一个连接，必须先把 rows 关掉再执行 UPDATE
		for _, p := range tmps {
			if err := os.Remove(p); err == nil {
				slog.Info("已清理上次未完成的临时文件", "path", p)
			}
		}
	}
	res, err := s.db.Exec(
		`UPDATE downloads SET status = 'error', error = ?, finished_at = ?, tmp = '' WHERE status = 'running'`,
		"服务重启，下载已中断", time.Now().UnixMilli())
	if err != nil {
		slog.Warn("mark interrupted downloads", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("已把上次未完成的下载标记为中断", "count", n)
	}
}
