//go:build windows

package server

// 删除 = 放进当前用户的 Windows 回收站。用的是资源管理器按 Delete 键同一套
// Shell 文件操作（SHFileOperation + FOF_ALLOWUNDO），所以之后可以在回收站里「还原」。

import (
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	foDelete          = 0x0003
	fofSilent         = 0x0004 // 不显示进度和声音
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040 // 放进回收站，而不是直接删除
	fofNoErrorUI      = 0x0400
)

// 与 SHFILEOPSTRUCTW 一一对应，字段顺序和类型不能改
type shFileOp struct {
	hwnd                 uintptr
	wFunc                uint32
	pFrom                *uint16
	pTo                  *uint16
	flags                uint16
	anyOperationsAborted int32
	nameMappings         uintptr
	progressTitle        *uint16
}

var (
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procSHFileOperationW = shell32.NewProc("SHFileOperationW")
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
)

// systemTrash 把条目放进回收站。
func systemTrash(full string) (string, error) {
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	from, err := syscall.UTF16FromString(abs)
	if err != nil {
		return "", err
	}
	from = append(from, 0) // pFrom 是以两个 0 结尾的文件列表

	// SHFileOperation 要求调用线程初始化过 COM；goroutine 可能换线程执行，
	// 所以这里把这个 goroutine 钉在当前线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, 2 /* COINIT_APARTMENTTHREADED */)
	if hr == 0 || hr == 1 {
		defer procCoUninitialize.Call()
	}

	op := shFileOp{
		wFunc: foDelete,
		pFrom: &from[0],
		flags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	if r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op))); r != 0 {
		return "", fmt.Errorf("SHFileOperation 失败：错误码 0x%X", r)
	}
	runtime.KeepAlive(from)
	if op.anyOperationsAborted != 0 {
		return "", fmt.Errorf("删除被取消")
	}
	return "回收站", nil
}
