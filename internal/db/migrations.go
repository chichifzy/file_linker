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
}
