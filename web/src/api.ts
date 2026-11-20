// 对后端 JSON API 的薄封装。Cookie 由浏览器自动携带。

export type Role = "admin" | "member";

export type Me = {
  uid: string;
  nickname: string;
  role: Role;
  /** 当前页面是否在运行服务的电脑上打开 */
  isLocal: boolean;
  /** 管理员能否修改共享目录：本机，或本机开启了“允许在其它设备更改共享目录” */
  canEditShares: boolean;
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

export type DirInfo = {
  name: string;
  path: string;
  modTime: number;
  size: number;
  files: number;
  dirs: number;
  /** 统计超时，只统计了一部分 */
  truncated: boolean;
};

export type ZipStatus = {
  status: "running" | "done" | "error";
  error: string;
  /** 已打包的源文件字节数 */
  done: number;
  total: number;
};

/** 打包好的 zip 下载地址，完整下载后服务器会删除该压缩包 */
export const zipURL = (id: string) => `/api/fs/zip/${id}/download`;

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    signal,
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

/** 转码后的视频地址，支持 Range 可拖动进度条 */
export const transcodeURL = (id: string) => `/api/fs/transcode/${id}/file`;

/** 视频缩略图（服务端抽帧成的小图），列表里用它代替 <video> 预加载 */
export const thumbURL = (share: number, path: string) => `/api/fs/thumb?${qs({ share, path })}`;

/** 边转边播用的 HLS 播放列表地址，Safari 可直接播放 */
export const hlsURL = (id: string) => `/api/fs/transcode/${id}/hls/index.m3u8`;

/** 视频的真实容器与编码信息。扩展名不可信，以文件头为准 */
export type MediaInfo = {
  /** mp4 / mov / webm / matroska / avi / flv / mpegts / unknown */
  container: string;
  /** H.264 / HEVC / AV1 / VP9 / … */
  codec: string;
  /** 音频编码，如 AAC / MP3 / Opus */
  audio: string;
  duration: number;
  /** 浏览器能否直接播放这个容器 */
  playable: boolean;
  /** 只换封装（不重新编码）就能变成可播放的 mp4 */
  remuxable: boolean;
};

/** 转码方式：copy 只换封装，几秒完成且画质无损；encode 重新编码，慢 */
export type TranscodeMode = "copy" | "encode";

export type TranscodeStatus = {
  status: "running" | "done" | "error";
  error?: string;
  mode?: TranscodeMode;
  /** 0-100 */
  percent: number;
  /** 首个分段已生成，现在就能开始播放 */
  ready: boolean;
  /** 源视频总时长（秒） */
  duration: number;
  /** 已经转码到的位置（秒） */
  readySeconds: number;
  /** 成品字节数，前端用「已缓冲比例 × 大小」估算下载速度 */
  bytes: number;
};

export type MediaCacheItem = {
  id: string;
  name: string;
  shareId: number;
  shareName: string;
  /** 相对共享目录的路径 */
  rel: string;
  srcSize: number;
  duration: number;
  status: "running" | "done" | "error";
  mode?: TranscodeMode;
  error?: string;
  /** 缓存占用字节数 */
  bytes: number;
  percent: number;
  created: number;
  accessed: number;
};

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
  dirInfo: (share: number, path: string, signal: AbortSignal) =>
    request<DirInfo>("GET", `/fs/dirinfo?${qs({ share, path })}`, undefined, signal),
  createZip: (share: number, path: string) => request<{ id: string }>("POST", `/fs/zip?${qs({ share, path })}`),
  /** 判断视频的真实容器与编码，用于播放失败时说明原因、决定走封装还是重编码 */
  codec: (share: number, path: string) => request<MediaInfo>("GET", `/fs/codec?${qs({ share, path })}`),
  /** 请求把视频转成可播放的 mp4；同一文件只会转一次，已转过时直接返回 done */
  transcode: (share: number, path: string) =>
    request<{ id: string; status: string; mode?: TranscodeMode; ready: boolean; bytes: number }>(
      "POST",
      `/fs/transcode?${qs({ share, path })}`,
    ),
  transcodeStatus: (id: string) => request<TranscodeStatus>("GET", `/fs/transcode/${id}`),
  mediaList: () => request<{ entries: MediaCacheItem[]; used: number; limit: number }>("GET", "/fs/media"),
  deleteMedia: (id: string) => request<void>("DELETE", `/fs/media/${id}`),
  clearMedia: () => request<{ ok: boolean; count: number }>("DELETE", "/fs/media"),
  zipStatus: (id: string) => request<ZipStatus>("GET", `/fs/zip/${id}`),
  deleteZip: (id: string) => request<void>("DELETE", `/fs/zip/${id}`),
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
  remoteShareEdit: () => request<{ enabled: boolean }>("GET", "/admin/remote-share-edit"),
  setRemoteShareEdit: (enabled: boolean) => request<void>("PUT", "/admin/remote-share-edit", { enabled }),
};
