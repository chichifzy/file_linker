package db

// migrations 按顺序执行，每条只执行一次。
// 修改表结构时在末尾追加新的一条，不要改动已发布的旧条目。
var migrations = []string{
	// #1 初始表结构
	`
CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	uid           TEXT NOT NULL UNIQUE COLLATE NOCASE, -- 用户 ID：字母和数字，不区分大小写
	nickname      TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
	created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
-- 系统中最多只有一个管理员
CREATE UNIQUE INDEX users_single_admin ON users(role) WHERE role = 'admin';

-- 只保存 token 的 SHA-256，数据库泄露也无法直接冒用会话
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL -- Unix 秒
);
CREATE INDEX sessions_user_id ON sessions(user_id);

-- 邀请码：管理员生成，注册时使用一次
CREATE TABLE invites (
	code       TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	expires_at INTEGER NOT NULL, -- Unix 秒
	used_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	used_at    TEXT
);

-- 共享目录：保存清理后的绝对路径
CREATE TABLE shares (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	path       TEXT NOT NULL UNIQUE,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
`,
	// #2 系统设置（键值对）
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
	// #3 下载任务记录：持久保存，只有用户主动删除才消失
	`
CREATE TABLE downloads (
	id          TEXT PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	share_id    INTEGER NOT NULL,
	dir         TEXT NOT NULL DEFAULT '',              -- 目标目录，相对共享目录
	url         TEXT NOT NULL,
	name        TEXT NOT NULL DEFAULT '',              -- 最终文件名
	status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'error', 'cancelled')),
	error       TEXT NOT NULL DEFAULT '',
	done        INTEGER NOT NULL DEFAULT 0,            -- 已下载字节数
	total       INTEGER NOT NULL DEFAULT 0,            -- 总字节数，0 表示服务器没给 Content-Length
	started_at  INTEGER NOT NULL,                      -- Unix 毫秒
	finished_at INTEGER NOT NULL DEFAULT 0             -- Unix 毫秒，0 表示还没结束
);
CREATE INDEX downloads_user_started ON downloads(user_id, started_at DESC);
`,
	// #4 下载中的临时文件名：进程被强杀时用它清理留在共享目录里的半个文件
	`
ALTER TABLE downloads ADD COLUMN tmp TEXT NOT NULL DEFAULT '';
`,
}
