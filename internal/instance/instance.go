// Package instance 保证同一个数据库只被一个服务进程使用，
// 并把运行中进程的地址和停止令牌写在 <数据库路径>.lock 里，供启动脚本复用。
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrBusy 表示数据库已被另一个进程占用。
var ErrBusy = errors.New("数据库已被另一个实例使用")

// Info 是写进锁文件的运行信息。
type Info struct {
	PID           int    `json:"pid"`
	Addr          string `json:"addr"`
	URL           string `json:"url"`           // 本机访问地址
	StartedAt     int64  `json:"startedAt"`     // Unix 毫秒
	ShutdownToken string `json:"shutdownToken"` // 停止服务用的随机令牌
	DB            string `json:"db"`
}

// Lock 是持有到进程退出为止的单实例锁。
type Lock struct {
	file *os.File
}

// LockPath 返回数据库对应的锁文件路径。
func LockPath(dbPath string) string {
	return filepath.Clean(dbPath) + ".lock"
}

// Acquire 尝试锁住数据库，已被其他进程持有时返回 ErrBusy。
func Acquire(dbPath string) (*Lock, error) {
	path := LockPath(dbPath)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, errLocked) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("锁定 %s 失败: %w", path, err)
	}
	// 拿到锁之后先清空上一轮残留的信息，避免新进程还没写就被读到旧地址。
	if err := f.Truncate(0); err != nil {
		unlockFile(f)
		f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}

// WriteInfo 把当前进程的地址和停止令牌写入锁文件。
func (l *Lock) WriteInfo(info Info) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	_, err = l.file.Write(data)
	return err
}

// Close 清空运行信息并释放锁。进程崩溃时系统也会自动释放。
func (l *Lock) Close() {
	_ = l.file.Truncate(0)
	_ = l.file.Sync()
	_ = unlockFile(l.file)
	_ = l.file.Close()
}

// Busy 报告数据库当前是否被其他进程占用，不修改锁文件内容。
func Busy(dbPath string) bool {
	f, err := os.OpenFile(LockPath(dbPath), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return true
	}
	_ = unlockFile(f)
	return false
}

// Probe 读取运行信息。服务刚启动、信息还没写完时会短暂重试。
func Probe(dbPath string) (Info, bool) {
	path := LockPath(dbPath)
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return Info{}, false
			}
			continue
		}
		var info Info
		if json.Unmarshal(data, &info) == nil && info.PID > 0 && info.URL != "" && info.ShutdownToken != "" {
			return info, true
		}
	}
	return Info{}, false
}
