//go:build !unix && !windows

package instance

import (
	"errors"
	"os"
)

var errLocked = errors.New("已被锁定")

// 其他平台（如 js/wasm）不支持文件锁，退化为不限制单实例。
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
