// Package unpack 读取 zip / 7z 压缩包，支持带密码的压缩包。
// 逐个条目流式解压，不会把整个文件读进内存。
package unpack

import (
	"errors"
	"io"
	"path"
	"strings"
	"time"
)

// Entry 描述压缩包里的一个条目。Name 是压缩包内的原始路径。
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	// Symlink 是符号链接：指向哪里不可预知，解压时跳过，不照搬到共享目录里
	Symlink bool
}

// Archive 是只读的压缩包。Open 需要按条目顺序调用，用完要 Close。
type Archive interface {
	Entries() []Entry
	Open(i int) (io.ReadCloser, error)
	Close() error
}

var (
	// ErrNeedPassword 压缩包有密码，但还没提供
	ErrNeedPassword = errors.New("这个压缩包有密码")
	// ErrWrongPassword 提供了密码，但不对
	ErrWrongPassword = errors.New("密码不正确")
	// ErrBadData 数据损坏；加密包在密码不对时也可能报这个
	ErrBadData = errors.New("压缩包已损坏，或密码不正确")
	// ErrUnsupportedFormat 不是 zip / 7z，或者用了暂不支持的压缩算法
	ErrUnsupportedFormat = errors.New("不支持的压缩包格式")
)

// IsArchive 按扩展名判断是不是本项目支持的压缩包。
func IsArchive(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".7z":
		return true
	}
	return false
}

// Open 按扩展名打开压缩包并校验密码。password 为空表示还没提供密码：
// 遇到加密包会返回 ErrNeedPassword，不会动磁盘。
func Open(full, password string) (Archive, error) {
	switch strings.ToLower(path.Ext(full)) {
	case ".zip":
		return openZip(full, password)
	case ".7z":
		return open7z(full, password)
	}
	return nil, ErrUnsupportedFormat
}
