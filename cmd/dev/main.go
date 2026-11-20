// dev 是跨平台（macOS / Linux / Windows）的开发启动器，供 Makefile 调用：
//
//	go run ./cmd/dev        同时启动 Go 服务（:8080）和 Vite（:5173）
//	go run ./cmd/dev api    只启动 Go 服务
//	go run ./cmd/dev web    只启动 Vite
//	go run ./cmd/dev clean  删除 bin 和 web/dist
//
// Ctrl+C 时两个进程一起停止，并以 0 退出，make 不会报 Error / Interrupt。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func main() {
	mode := "all"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	var err error
	switch mode {
	case "all", "api", "web":
		err = run(mode)
	case "clean":
		err = clean()
	default:
		err = fmt.Errorf("未知参数 %q，可用：api / web / clean", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
}

func run(mode string) error {
	// 先注册信号，避免构建期间按 Ctrl+C 时本进程被直接杀掉
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	var cmds []*exec.Cmd
	if mode != "web" {
		if err := ensurePlaceholder(); err != nil {
			return err
		}
		bin := filepath.Join("bin", "dev-server")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("构建 Go 服务失败：%w", err)
		}
		cmds = append(cmds, command("", bin, "-addr", ":8080", "-db", "data.db"))
	}
	if mode != "api" {
		vite := filepath.Join("node_modules", "vite", "bin", "vite.js")
		if _, err := os.Stat(filepath.Join("web", vite)); err != nil {
			return fmt.Errorf("未找到 Vite，请先执行：cd web && pnpm install")
		}
		// 直接用 node 运行 vite.js，绕开 .bin/vite 在 Windows 上是 .cmd 的差异
		cmds = append(cmds, command("web", "node", vite))
	}

	exited := make(chan error, len(cmds))
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			stopAll(cmds)
			return fmt.Errorf("启动 %s 失败：%w", c.Path, err)
		}
		go func(c *exec.Cmd) { exited <- c.Wait() }(c)
	}

	var result error
	select {
	case <-sig:
		// 终端里的 Ctrl+C 本来就会发给所有子进程；这里再转发一次，
		// 覆盖 SIGTERM 或只给本进程发信号的情况。
	case err := <-exited:
		result = fmt.Errorf("有进程提前退出：%v", err)
		cmds = running(cmds)
	}
	stopAll(cmds)

	// 等待剩余进程退出，超时强制结束
	remaining := len(running(cmds))
	timeout := time.After(10 * time.Second)
	for remaining > 0 {
		select {
		case <-exited:
			remaining--
		case <-sig:
		case <-timeout:
			for _, c := range cmds {
				_ = c.Process.Kill()
			}
			return result
		}
	}
	return result
}

func command(dir, name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c
}

func running(cmds []*exec.Cmd) []*exec.Cmd {
	var out []*exec.Cmd
	for _, c := range cmds {
		if c.Process != nil && c.ProcessState == nil {
			out = append(out, c)
		}
	}
	return out
}

// stopAll 请求子进程正常退出。Windows 不支持向其他进程发送 os.Interrupt，只能直接结束。
func stopAll(cmds []*exec.Cmd) {
	for _, c := range running(cmds) {
		if runtime.GOOS == "windows" {
			_ = c.Process.Kill()
		} else {
			_ = c.Process.Signal(os.Interrupt)
		}
	}
}

// ensurePlaceholder 保证 web/dist 存在：go:embed 要求该目录非空，开发时放一个占位页。
func ensurePlaceholder() error {
	index := filepath.Join("web", "dist", "index.html")
	if _, err := os.Stat(index); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		return err
	}
	page := `<p>前端开发请访问 <a href="http://localhost:5173">http://localhost:5173</a></p>` + "\n"
	return os.WriteFile(index, []byte(page), 0o644)
}

func clean() error {
	for _, dir := range []string{"bin", filepath.Join("web", "dist")} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
