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
  /** 运行服务的系统：windows / darwin / linux，删除时据此说「回收站」还是「废纸篓」 */
  platform: string;
};

export type Share = {
  id: number;
  name: string;
  /** 仅管理员可见 */
  path?: string;
  /** 位于该共享目录内的常用文件夹（下载、桌面等），path 相对共享目录 */
  quick: { name: string; path: string }[];
};

export type Kind = "dir" | "text" | "image" | "video" | "archive" | "binary";

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
    /** 业务错误码，如 need_password / wrong_password / name_conflict */
    public code = "",
    /** 后端附带的额外字段（例如重名冲突时建议的新文件名） */
    public data: Record<string, unknown> | undefined = undefined,
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

/** 服务端把链接指向的文件下载到本机的任务进度 */
export type DownloadStatus = {
  status: DownloadJob["status"];
  error: string;
  /** 最终文件名；还没确定下来时为空 */
  name: string;
  /** 已下载字节数 */
  done: number;
  /** 总字节数，服务器没有返回 Content-Length 时为 0 */
  total: number;
};

/** 任务列表里的一条下载记录。记录只存在服务端内存里，重启服务后清空 */
export type DownloadJob = {
  id: string;
  /** 最终文件名；还没解析出来时为空 */
  name: string;
  url: string;
  status: "running" | "done" | "error" | "cancelled";
  error: string;
  /** 目标共享目录 id */
  share: number;
  /** 目标目录，相对共享目录；空字符串表示共享目录根 */
  dir: string;
  done: number;
  total: number;
  /** Unix 毫秒 */
  startedAt: number;
  /** Unix 毫秒，0 表示还在下载 */
  finishedAt: number;
};

/** 解压任务：把 zip / 7z 解到压缩包所在的目录 */
export type ExtractStatus = {
  status: "running" | "done" | "error" | "cancelled";
  error: string;
  /** need_password / wrong_password：压缩包有密码或密码不对 */
  code: string;
  /** 压缩包文件名 */
  name: string;
  /** 已解压字节数 */
  done: number;
  /** 解压后的总字节数 */
  total: number;
  /** 已解压出的文件数 */
  files: number;
  /** 因符号链接或路径不安全而跳过的条目数 */
  skipped: number;
  /** 解压到的目录，相对共享目录 */
  dir: string;
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
    throw new ApiError(res.status, data?.error ?? `请求失败（${res.status}）`, data?.code ?? "", data ?? undefined);
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

/** 转码缓存列表，附带缓存目录信息 */
export type MediaCacheList = {
  entries: MediaCacheItem[];
  /** 已占用字节数 */
  used: number;
  /** 容量上限，0 表示不限制 */
  limit: number;
  /** 缓存目录（运行服务的电脑上的绝对路径），仅管理员可见 */
  dir?: string;
  /** 默认缓存目录，方便「恢复默认」，仅管理员可见 */
  defaultDir?: string;
  /** 当前账号能否修改：管理员，且在本机或已允许远程修改 */
  editable: boolean;
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
  /** 在共享目录的 path 下新建文件夹（仅管理员） */
  mkdir: (share: number, path: string, name: string) =>
    request<{ name: string; path: string }>("POST", `/fs/mkdir?${qs({ share, path })}`, { name }),
  /** 上传前预检重名（仅管理员）。返回当前已经存在的名字和建议的新名字 */
  checkUpload: (share: number, path: string, names: string[]) =>
    request<{ conflicts: { name: string; proposed: string }[] }>(
      "POST",
      `/fs/upload/check?${qs({ share, path })}`,
      { names },
    ),
  /** 上传一个文件到当前目录（仅管理员）。用 XHR 才能拿到真实的上传进度 */
  uploadFile: (
    share: number,
    path: string,
    file: File,
    rename: boolean,
    onProgress?: (loaded: number, total: number) => void,
    signal?: AbortSignal,
  ) =>
    new Promise<{ name: string; path: string; size: number }>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", `/api/fs/upload?${qs({ share, path, name: file.name, rename: rename ? 1 : 0 })}`);
      // 跨站表单发不出自定义头，和 SameSite Cookie 一起防 CSRF
      xhr.setRequestHeader("X-File-Linker-Upload", "1");
      if (onProgress) {
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress(e.loaded, e.total);
        };
      }
      const onAbort = () => xhr.abort();
      if (signal) {
        if (signal.aborted) return reject(new DOMException("已取消", "AbortError"));
        signal.addEventListener("abort", onAbort, { once: true });
      }
      const cleanup = () => signal?.removeEventListener("abort", onAbort);
      xhr.onerror = () => {
        cleanup();
        reject(new Error("网络错误，上传失败"));
      };
      xhr.onabort = () => {
        cleanup();
        reject(new DOMException("已取消", "AbortError"));
      };
      xhr.onload = () => {
        cleanup();
        let data: Record<string, unknown> | undefined;
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          // 后端没返回 JSON 时也按普通错误处理
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(data as { name: string; path: string; size: number });
        } else {
          reject(
            new ApiError(
              xhr.status,
              (data?.error as string) ?? `上传失败（${xhr.status}）`,
              (data?.code as string) ?? "",
              data,
            ),
          );
        }
      };
      xhr.send(file);
    }),
  /** 重命名（仅管理员），name 是完整的新文件名，可以改扩展名 */
  rename: (share: number, path: string, name: string) =>
    request<{ name: string; path: string }>("POST", `/fs/rename?${qs({ share, path })}`, { name }),
  /** 删除（仅管理员）：移到系统回收站 / 废纸篓，返回一句说明去了哪里 */
  remove: (share: number, path: string) =>
    request<{ where: string }>("POST", `/fs/delete?${qs({ share, path })}`),
  /** 粘贴（仅管理员）：把 srcPath 复制或移动到当前目录。
   *  重名时返回 code=name_conflict 和 data.proposed，让用户决定取消还是继续；
   *  rename=true 表示用户选了继续，此时由服务端加 (1)、(2) 后缀。
   *  sameDir 表示剪切到原目录，什么也没做 */
  paste: (share: number, path: string, srcShare: number, srcPath: string, mode: "copy" | "move", rename = false) =>
    request<{ name: string; path: string; mode: string; sameDir?: boolean }>(
      "POST",
      `/fs/paste?${qs({ share, path })}`,
      { srcShare, srcPath, mode, rename },
    ),
  /** 解压 zip / 7z 到压缩包所在的目录（仅管理员），立即返回任务 id。
   *  压缩包有密码时会返回 code=need_password / wrong_password，前端据此弹密码框 */
  startExtract: (share: number, path: string, password: string) =>
    request<{ id: string; name: string; total: number }>("POST", `/fs/extract?${qs({ share, path })}`, { password }),
  extractStatus: (id: string) => request<ExtractStatus>("GET", `/fs/extract/${id}`),
  /** 取消解压；已经结束的任务则是把记录去掉 */
  cancelExtract: (id: string) => request<void>("DELETE", `/fs/extract/${id}`),
  /** 让服务器把链接指向的文件下载到 path 目录（仅管理员），立即返回任务 id */
  startDownload: (share: number, path: string, url: string) =>
    request<{ id: string; name: string }>("POST", `/fs/download?${qs({ share, path })}`, { url }),
  downloadStatus: (id: string) => request<DownloadStatus>("GET", `/fs/download/${id}`),
  /** 当前账号的全部下载任务（最新的在前），用于侧边栏「任务列表」 */
  downloads: () => request<{ jobs: DownloadJob[] }>("GET", "/fs/downloads"),
  /** 清掉已经结束的记录，正在下载的任务保留 */
  clearDownloads: () => request<{ removed: number }>("DELETE", "/fs/downloads"),
  /** 取消正在下载的任务并删除没下完的临时文件；对已结束的任务则是移除记录 */
  cancelDownload: (id: string) => request<void>("DELETE", `/fs/download/${id}`),
  /** 判断视频的真实容器与编码，用于播放失败时说明原因、决定走封装还是重编码 */
  codec: (share: number, path: string) => request<MediaInfo>("GET", `/fs/codec?${qs({ share, path })}`),
  /** 请求把视频转成可播放的 mp4；同一文件只会转一次，已转过时直接返回 done */
  transcode: (share: number, path: string) =>
    request<{ id: string; status: string; mode?: TranscodeMode; ready: boolean; bytes: number }>(
      "POST",
      `/fs/transcode?${qs({ share, path })}`,
    ),
  transcodeStatus: (id: string) => request<TranscodeStatus>("GET", `/fs/transcode/${id}`),
  /** 播放前查一下有没有现成的转码缓存：有就直接播缓存，不必先播原文件再失败。
   *  没有缓存时会顺带返回容器与编码（encoding），用于在浏览器报错前就给出结论 */
  transcodeLookup: (share: number, path: string) =>
    request<{
      cached: boolean;
      id: string;
      status: string;
      mode?: TranscodeMode;
      bytes: number;
      encoding?: MediaInfo;
    }>("GET", `/fs/transcode/lookup?${qs({ share, path })}`),
  mediaList: () => request<MediaCacheList>("GET", "/fs/media"),
  /** 修改转码缓存目录，已有缓存会迁移过去（调用返回时迁移已经完成） */
  setMediaDir: (dir: string) =>
    request<{ dir: string; moved: number; bytes: number }>("POST", "/admin/media-dir", { dir }),
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
