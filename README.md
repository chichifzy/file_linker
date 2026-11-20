# 文件浏览（file_linker）

在电脑上运行一个服务，同一局域网内的手机、平板、其他电脑用浏览器就能浏览这台电脑上共享的文件（只读）。

- 支持预览纯文本（txt、md、代码等，大文件只显示前 1MB）、图片和视频（支持拖动进度）。
- 其他类型的文件只显示名称、大小、修改时间，服务端不会读取它们的内容。
- 支持按文件名搜索、显示/隐藏以 `.` 开头的隐藏文件。

## 启动

需要先安装 [Go](https://go.dev/dl/)、[Node.js](https://nodejs.org/) 20+ 和 pnpm（`npm install -g pnpm`）。

| 系统 | 构建并启动 | 只构建可执行文件 |
| --- | --- | --- |
| macOS | 双击 `start.command`，或终端运行 `./start.sh` | `./start.sh build` |
| Linux | `./start.sh` | `./start.sh build` |
| Windows | 双击 `start.bat` | `start.bat build` |

首次启动会提示创建管理员账号。启动后命令行窗口会列出访问地址：

```text
服务已启动，按 Ctrl+C 停止。
  本机访问:   http://localhost:8080
  局域网访问: http://192.168.1.23:8080
```

只构建时生成 `bin/file_linker`（Windows 为 `bin\file_linker.exe`），前端已打包在里面，可以单独复制到别处运行。数据库默认是当前目录下的 `data.db`。

常用参数：

```text
file_linker -addr :8081                      换端口
file_linker -db /path/to/data.db             指定数据库位置
file_linker create-admin <用户ID> <用户名>    创建管理员（只能有一个）
```

## 使用

1. 管理员在**运行服务的这台电脑上**打开 `http://localhost:8080` 登录，在“共享目录”中添加要共享的文件夹。其他设备上登录管理员账号看不到这个入口。
2. 管理员在“邀请码”中生成邀请码，发给其他人。每个邀请码只能注册一个普通用户。
3. 点击左下角头像 →“分享二维码”，其他设备扫码即可打开页面，用邀请码注册后浏览文件。

## 安全说明

- 只有登录用户能访问文件，只能访问共享目录内的文件，指向共享目录以外的符号链接会被忽略。
- 服务使用 HTTP，适合在家庭或办公室等可信局域网中使用，不要直接暴露到公网。
- 首次运行时，系统防火墙可能询问是否允许接收网络连接，需要允许，否则其他设备无法访问。

## 开发

```bash
make dev      # 同时启动后端和前端开发服务器，访问 http://localhost:5173
make build    # 生产构建，输出 bin/file_linker
```
