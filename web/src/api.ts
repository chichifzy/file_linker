// 对后端 JSON API 的薄封装。Cookie 由浏览器自动携带。

export type Role = "admin" | "member";

export type Me = {
  uid: string;
  nickname: string;
  role: Role;
  /** 当前页面是否在运行服务的电脑上打开 */
  isLocal: boolean;
};

export type Share = {
  id: number;
  name: string;
  /** 仅管理员可见 */
  path?: string;
  /** 位于该共享目录内的常用文件夹（下载、桌面等），path 相对共享目录 */
  quick: { name: string; path: string }[];
};

export type Kind = "dir" | "text" | "image" | "video" | "binary";

export type Entry = {
  name: string;
  path: string;
  isDir: boolean;
  kind: Kind;
  size: number;
  modTime: number;
};

export type Invite = {
  code: string;
  createdAt: string;
  /** Unix 秒 */
  expiresAt: number;
  usedBy: string | null;
  usedAt: string | null;
};

export type HostDirs = { path: string; parent: string | null; dirs: { name: string; path: string }[] };

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => null);
    throw new ApiError(res.status, data?.error ?? `请求失败（${res.status}）`);
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export function errMsg(e: unknown) {
  return e instanceof Error ? e.message : String(e);
}

const qs = (params: Record<string, string | number | boolean | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === false || v === "") continue;
    p.set(k, v === true ? "1" : String(v));
  }
  return p.toString();
};

/** 文件内容地址，可直接用于 img / video / a 标签 */
export const fileURL = (share: number, path: string, download = false) =>
  `/api/fs/file?${qs({ share, path, download })}`;

/** 文本预览最多读取的字节数，与后端一致 */
export const TEXT_PREVIEW_LIMIT = 1 << 20;

export const api = {
  login: (uid: string, password: string) => request<void>("POST", "/auth/login", { uid, password }),
  register: (uid: string, nickname: string, password: string, inviteCode: string) =>
    request<void>("POST", "/auth/register", { uid, nickname, password, inviteCode }),
  logout: () => request<void>("POST", "/auth/logout"),
  me: () => request<Me>("GET", "/me"),
  changePassword: (oldPassword: string, newPassword: string) =>
    request<void>("PUT", "/me/password", { oldPassword, newPassword }),
  netInfo: () => request<{ ips: string[]; port: string }>("GET", "/netinfo"),

  shares: () => request<Share[]>("GET", "/shares"),
  list: (share: number, path: string, hidden: boolean) =>
    request<{ path: string; entries: Entry[] }>("GET", `/fs/list?${qs({ share, path, hidden })}`),
  search: (share: number, path: string, q: string, hidden: boolean) =>
    request<{ entries: Entry[]; truncated: boolean }>("GET", `/fs/search?${qs({ share, path, q, hidden })}`),
  /** 读取文本文件开头（最多 1MB），返回内容和是否被截断 */
  readText: async (share: number, path: string) => {
    const res = await fetch(fileURL(share, path), { headers: { Range: `bytes=0-${TEXT_PREVIEW_LIMIT - 1}` } });
    if (!res.ok && res.status !== 416) {
      const data = await res.json().catch(() => null);
      throw new ApiError(res.status, data?.error ?? `读取失败（${res.status}）`);
    }
    if (res.status === 416) return { text: "", truncated: false };
    const buf = await res.arrayBuffer();
    // Content-Range: bytes 0-1048575/总大小
    const total = Number(res.headers.get("Content-Range")?.split("/")[1] ?? buf.byteLength);
    // 截断处可能切断多字节字符，fatal: false 会把残缺字节替换为 �
    return { text: new TextDecoder("utf-8").decode(buf), truncated: total > buf.byteLength };
  },

  invites: () => request<Invite[]>("GET", "/admin/invites"),
  createInvite: () => request<{ code: string }>("POST", "/admin/invites"),
  deleteInvite: (code: string) => request<void>("DELETE", `/admin/invites/${encodeURIComponent(code)}`),

  addShare: (path: string) => request<unknown>("POST", "/admin/shares", { path }),
  removeShare: (id: number) => request<void>("DELETE", `/admin/shares/${id}`),
  hostDirs: (path: string) => request<HostDirs>("GET", `/admin/host-dirs?${qs({ path })}`),
};
