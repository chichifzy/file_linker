package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"file_linker/internal/db"
	"file_linker/internal/instance"
	"file_linker/internal/server"
	"file_linker/web"
)

func main() {
	addr := flag.String("addr", ":8080", "监听地址")
	dbPath := flag.String("db", "data.db", "SQLite 数据库文件路径")
	secureCookie := flag.Bool("secure-cookie", false, "为会话 Cookie 设置 Secure 标记（HTTPS 部署时开启）")
	cacheGB := flag.Int64("cache-gb", 0, "视频转码缓存容量上限（GB），0 表示不限制、缓存永久保留")
	cacheDir := flag.String("cache-dir", "", "视频转码缓存目录，默认系统缓存目录；在网页上改过则用网页里的设置")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `用法:
  %[1]s [flags]                                启动服务
  %[1]s [flags] status [--attach] [--open]       查看是否有实例在运行（--attach 驻留，按 Ctrl+C 停止它）
  %[1]s [flags] create-admin <用户ID> <用户名>  创建唯一的管理员（随后输入密码）
  %[1]s [flags] has-admin                       已有管理员时退出码为 0，否则为 1（供启动脚本使用）

flags:
`, os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	// 锁文件按数据库的绝对路径命名：脚本从不同目录启动也能识别同一个数据库。
	dbAbs, err := filepath.Abs(*dbPath)
	if err != nil {
		slog.Error("解析数据库路径失败", "err", err)
		os.Exit(1)
	}

	if flag.Arg(0) == "status" {
		os.Exit(runStatus(dbAbs, flag.Args()[1:]))
	}

	if flag.Arg(0) != "" {
		database, err := db.Open(*dbPath)
		if err != nil {
			slog.Error("打开数据库失败", "err", err)
			os.Exit(1)
		}
		defer database.Close()

		switch flag.Arg(0) {
		case "create-admin":
			if flag.NArg() != 3 {
				flag.Usage()
				os.Exit(2)
			}
			if err := createAdmin(database, flag.Arg(1), flag.Arg(2)); err != nil {
				fmt.Fprintln(os.Stderr, "创建管理员失败:", err)
				os.Exit(1)
			}
			fmt.Printf("管理员 %s 已创建\n", flag.Arg(1))
			return
		case "has-admin":
			var exists bool
			if err := database.QueryRow(`SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin')`).Scan(&exists); err != nil || !exists {
				database.Close()
				os.Exit(1)
			}
			return
		default:
			flag.Usage()
			os.Exit(2)
		}
	}

	// 同一个数据库只允许一个服务进程，先上锁再打开数据库。
	lock, err := instance.Acquire(dbAbs)
	if err != nil {
		if errors.Is(err, instance.ErrBusy) {
			reportBusy(dbAbs)
			os.Exit(1)
		}
		slog.Error("锁定数据库失败", "err", err)
		os.Exit(1)
	}
	defer lock.Close()

	database, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		reportListenError(*addr, err)
		os.Exit(1)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := randomToken()

	// 转码缓存默认放在系统缓存目录（网页上可以改，改过之后以网页设置为准）。
	// 容量超过上限时按最久未使用淘汰；-cache-dir 显式指定时优先于网页设置。
	server.SetMediaCache(*cacheDir, *cacheGB<<30)
	srv := &http.Server{
		Handler: server.New(database, web.Dist(), server.Options{
			SecureCookie:  *secureCookie,
			Port:          port,
			ShutdownToken: token,
			Shutdown:      stop,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 写运行信息，启动脚本据此复用本实例；写失败只影响复用，不影响服务本身。
	info := instance.Info{
		PID:           os.Getpid(),
		Addr:          ln.Addr().String(),
		URL:           fmt.Sprintf("http://localhost:%s", port),
		StartedAt:     time.Now().UnixMilli(),
		ShutdownToken: token,
		DB:            dbAbs,
	}
	if err := lock.WriteInfo(info); err != nil {
		slog.Warn("写入运行信息失败，启动脚本将无法复用此实例", "err", err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("服务异常退出", "err", err)
			stop()
		}
	}()
	printURLs(port)

	<-ctx.Done()
	// 先停掉在跑的 ffmpeg，再关服务，避免子进程变成孤儿
	server.StopTranscodes()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("关闭服务失败", "err", err)
	}
}

// reportBusy 说明数据库已被谁占用，引导用户复用而不是换端口。
func reportBusy(dbAbs string) {
	info, ok := instance.Probe(dbAbs)
	if ok {
		fmt.Fprintf(os.Stderr, "数据库 %s 已由另一个进程使用（PID %d）。\n", dbAbs, info.PID)
		fmt.Fprintf(os.Stderr, "  访问地址: %s\n", info.URL)
		fmt.Fprintln(os.Stderr, "  直接用这个地址访问，或关闭那个服务窗口后再启动。")
		return
	}
	fmt.Fprintf(os.Stderr, "数据库 %s 已由另一个进程使用（可能是旧版本，未写入运行信息）。\n", dbAbs)
	fmt.Fprintln(os.Stderr, "  请关闭之前启动的服务窗口后再试。")
}

// reportListenError 区分「端口被其他程序占用」「另一个数据库的本项目实例」和「旧版本实例」。
func reportListenError(addr string, err error) {
	fmt.Fprintf(os.Stderr, "无法监听 %s：%v\n", addr, err)
	if _, p, e := net.SplitHostPort(addr); e == nil && p != "" {
		base := "http://localhost:" + p
		if healthOK(base) {
			fmt.Fprintf(os.Stderr, "端口 %s 上已有 file_linker 在运行（可能是用 -db 指定的另一个数据库）。\n", p)
			fmt.Fprintln(os.Stderr, "请直接使用那个服务，或用 -addr 换一个端口。")
			return
		}
		if legacyHealth(base) {
			fmt.Fprintf(os.Stderr, "端口 %s 上已有旧版本的 file_linker 在运行，请关闭那个服务窗口后再启动。\n", p)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "端口可能已被其他程序占用，可以关闭占用它的程序，或用 -addr :8081 换一个端口。")
}

// legacyHealth 识别旧版本（没有 /api/health 接口，命中 /api/ 的兜底 404）的响应。
func legacyHealth(base string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(base + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	return err == nil && strings.Contains(string(body), "接口不存在")
}

// randomToken 生成停止服务的随机令牌，失败时返回空串（远程停止随之关闭）。
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Warn("生成停止令牌失败，将禁用远程停止", "err", err)
		return ""
	}
	return hex.EncodeToString(b)
}

func printURLs(port string) {
	fmt.Println()
	fmt.Println("服务已启动，按 Ctrl+C 停止。")
	fmt.Printf("  本机访问:   http://localhost:%s\n", port)
	ips := server.LANAddrs()
	if len(ips) == 0 {
		fmt.Println("  未检测到局域网地址，请确认电脑已连接网络。")
	}
	for _, ip := range ips {
		fmt.Printf("  局域网访问: http://%s:%s\n", ip, port)
	}
	fmt.Println()
}
