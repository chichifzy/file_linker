package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"file_linker/internal/instance"
)

// appName 是 /api/health 返回的标识，用来确认端口上运行的是本项目。
const appName = "file_linker"

// runStatus 处理 `file_linker status`，退出码约定：
//
//	0 已处理（已有实例：已打开浏览器 / 已驻留并停止）
//	1 没有实例在运行（启动脚本据此继续构建启动）
//	2 用法错误（旧版本的可执行文件不认识 status，也会返回 2）
//	3 status 的参数不正确
//	4 有实例占用数据库，但无法确认（正在启动 / 不响应）
func runStatus(dbPath string, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	attach := fs.Bool("attach", false, "打开浏览器后驻留，按 Ctrl+C 停止正在运行的服务")
	open := fs.Bool("open", false, "在浏览器中打开服务地址")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "status 不接受额外参数")
		return 3
	}

	if !instance.Busy(dbPath) {
		return 1
	}
	info, ok := instance.Probe(dbPath)
	if !ok {
		fmt.Fprintln(os.Stderr, "已有实例占用数据库，但读不到运行信息（可能是正在启动）。")
		return 4
	}
	if !healthOK(info.URL) {
		fmt.Fprintf(os.Stderr, "检测到实例信息（PID %d，%s），但它没有响应，可能是正在启动。\n",
			info.PID, info.URL)
		return 4
	}

	fmt.Printf("服务已在运行：%s\n", info.URL)
	fmt.Printf("  局域网地址请用网页左下角头像里的「分享二维码」查看。\n")
	if *open {
		openBrowser(info.URL)
	}
	if !*attach {
		return 0
	}
	attachService(info)
	return 0
}

// attachService 驻留在当前窗口：Ctrl+C 通过本机接口停止服务；
// 直接关闭窗口不影响服务；服务被别处停掉时自动退出。
func attachService(info instance.Info) {
	fmt.Println("按 Ctrl+C 停止服务；直接关闭这个窗口不影响服务。")
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	stopping := false
	deadline := time.Time{}
	fails := 0
	for {
		select {
		case <-sig:
			if stopping {
				fmt.Println("强制退出，服务仍在后台运行。")
				return
			}
			stopping = true
			fmt.Println("正在停止服务…")
			if err := requestShutdown(info); err != nil {
				fmt.Fprintf(os.Stderr, "请求停止失败：%v\n", err)
				return
			}
			deadline = time.Now().Add(15 * time.Second)
		case <-ticker.C:
			alive := healthOK(info.URL)
			switch {
			case !stopping && !alive:
				if fails++; fails >= 3 {
					fmt.Println("服务已停止。")
					return
				}
			case !stopping:
				fails = 0
			case !alive:
				fmt.Println("服务已停止。")
				return
			case time.Now().After(deadline):
				fmt.Println("服务还在关闭中，请稍后确认。")
				return
			}
		}
	}
}

// requestShutdown 请求正在运行的服务优雅退出。只对本机地址发请求。
func requestShutdown(info instance.Info) error {
	u, err := url.Parse(info.URL)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("运行信息里的地址不是本机")
	}
	req, err := http.NewRequest(http.MethodPost, info.URL+"/api/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-File-Linker-Shutdown", info.ShutdownToken)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("服务返回 %s", resp.Status)
	}
	return nil
}

// healthOK 确认地址上运行的是本项目。
func healthOK(base string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(base + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var v struct {
		App string `json:"app"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return false
	}
	return v.App == appName
}

// openBrowser 在后台打开浏览器，不等待也不影响当前窗口。
func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "打开浏览器失败，请手动访问 %s\n", target)
		return
	}
	_ = cmd.Process.Release()
}
