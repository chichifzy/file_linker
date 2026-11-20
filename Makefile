.PHONY: dev dev-api dev-web dev-tool web build build-linux build-windows clean

# 开发：make dev 同时启动前后端（或两个终端分别运行 make dev-api 和 make dev-web），然后访问 http://localhost:5173
# 同一局域网的手机访问 http://<电脑IP>:5173（vite.config.ts 中 host: true，Vite 启动时会打印 Network 地址）
# 启动、Ctrl+C 停止和占位页都由 cmd/dev 处理，macOS / Linux / Windows 通用
# 先编译再运行，不用 go run：go run 在 Ctrl+C 时返回非 0，make 会报 Error
ifeq ($(OS),Windows_NT)
DEV := bin\dev.exe
else
DEV := ./bin/dev
endif

dev: dev-tool
	$(DEV)

dev-api: dev-tool
	$(DEV) api

dev-web: dev-tool
	$(DEV) web

dev-tool:
	go build -o $(DEV) ./cmd/dev

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

# 环境变量用 make 的 export 设置，不写成 `VAR=x go build`，这样 Windows 的 cmd 也能执行
export CGO_ENABLED := 0
GOBUILD := go build -trimpath -ldflags="-s -w"

# 生产构建：前端产物嵌入可执行文件，输出 bin/file_linker（Windows 上 go 会自动生成 .exe）
build: web
	$(GOBUILD) -o bin/file_linker$(if $(filter Windows_NT,$(OS)),.exe) .

# 交叉编译 Linux 服务器版本
build-linux: export GOOS := linux
build-linux: export GOARCH := amd64
build-linux: web
	$(GOBUILD) -o bin/file_linker-linux-amd64 .

# 交叉编译 Windows 版本
build-windows: export GOOS := windows
build-windows: export GOARCH := amd64
build-windows: web
	$(GOBUILD) -o bin/file_linker-windows-amd64.exe .

# 用 go run：Windows 上不能删除正在运行的 bin/dev.exe
clean:
	go run ./cmd/dev clean
