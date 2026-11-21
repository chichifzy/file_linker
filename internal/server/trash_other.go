//go:build !windows && !darwin && !linux

package server

import "errors"

// systemTrash 在这个系统上没有对应的回收站实现，返回错误让调用方退回
// 共享目录里的隐藏回收站（文件不会被直接删掉）。
func systemTrash(string) (string, error) {
	return "", errors.New("当前系统没有可用的回收站")
}
