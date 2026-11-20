package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"file_linker/internal/db"
	"file_linker/internal/server"
	"file_linker/web"
)

func main() {
	addr := flag.String("addr", ":8080", "监听地址")
	dbPath := flag.String("db", "data.db", "SQLite 数据库文件路径")
	secureCookie := flag.Bool("secure-cookie", false, "为会话 Cookie 设置 Secure 标记（HTTPS 部署时开启）")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `用法:
  %[1]s [flags]                                启动服务
  %[1]s [flags] create-admin <用户ID> <用户名>  创建唯一的管理员（随后输入密码）
  %[1]s [flags] has-admin                       已有管理员时退出码为 0，否则为 1（供启动脚本使用）

flags:
`, os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	database, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	switch flag.Arg(0) {
	case "":
		// 启动服务
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

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法监听 %s：%v\n端口可能已被占用，可以用 -addr :8081 换一个端口。\n", *addr, err)
		os.Exit(1)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	srv := &http.Server{
		Handler:           server.New(database, web.Dist(), *secureCookie, port),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("服务异常退出", "err", err)
			stop()
		}
	}()
	printURLs(port)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("关闭服务失败", "err", err)
	}
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
