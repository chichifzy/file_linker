import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { api, errMsg, fileURL, hlsURL, thumbURL, transcodeURL, zipURL, type DirInfo, type Entry, type MediaInfo, type Share } from "../api";
import { useConfirm } from "../components/Confirm";
import Modal, { type ModalHandle } from "../components/Modal";
import { KindIcon, PlayBadge } from "../components/FileIcons";

const HIDDEN_KEY = "showHidden";

// 缩略图请求排队：一个目录里几十个视频时，浏览器会同时发出几十个请求把连接
// 占满，点开视频要排在它们后面。这里限制同时在途的缩略图请求数。
const THUMB_CONCURRENCY = 3;
let thumbRunning = 0;
const thumbWaiting: (() => void)[] = [];

function acquireThumb(): Promise<() => void> {
  return new Promise((resolve) => {
    const grant = () => {
      thumbRunning++;
      let released = false;
      resolve(() => {
        if (released) return;
        released = true;
        thumbRunning--;
        thumbWaiting.shift()?.();
      });
    };
    if (thumbRunning < THUMB_CONCURRENCY) grant();
    else thumbWaiting.push(grant);
  });
}

/** 视频缩略图：滚动到可见才加载，同一时刻最多 3 个在途；服务端抽帧失败时退回用 <video> 取首帧 */
function VideoThumb({ thumb, src }: { thumb: string; src: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const release = useRef<(() => void) | null>(null);
  const [visible, setVisible] = useState(false);
  const [ready, setReady] = useState(false); // 拿到请求名额，可以发请求了
  const [mode, setMode] = useState<"image" | "video">("image");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting) {
        setVisible(true);
        io.disconnect();
      }
    });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  useEffect(() => {
    if (!visible) return;
    let alive = true;
    void acquireThumb().then((rel) => {
      if (!alive) return rel();
      release.current = rel;
      setReady(true);
    });
    return () => {
      alive = false;
      release.current?.();
      release.current = null;
    };
  }, [visible]);

  // 加载完（无论成功失败）就释放名额，让后面的缩略图跟上
  const done = () => {
    release.current?.();
    release.current = null;
  };

  if (failed) return <KindIcon kind="video" />;
  return (
    <span ref={ref} className="video-thumb">
      {ready &&
        (mode === "image" ? (
          <img
            src={thumb}
            alt=""
            onLoad={done}
            onError={() => {
              done();
              setMode("video"); // 服务端没有 ffmpeg 等情况，退回浏览器取首帧
            }}
          />
        ) : (
          <video
            src={`${src}#t=0.1`}
            preload="metadata"
            muted
            playsInline
            onLoadedData={done}
            onError={() => {
              done();
              setFailed(true);
            }}
          />
        ))}
      <PlayBadge />
    </span>
  );
}

export function fmtSize(n: number) {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

const fmtTime = (ms: number) => new Date(ms).toLocaleString();

/** 字节/秒 → 8.4 MB/s */
export function fmtSpeed(bytesPerSec: number) {
  const mb = bytesPerSec / 1048576;
  if (mb >= 1) return `${mb.toFixed(mb < 10 ? 1 : 0)} MB/s`;
  return `${Math.max(1, Math.round(bytesPerSec / 1024))} KB/s`;
}

/** 把秒数格式化成 12:34 或 1:02:03，用于显示已转码到的位置 */
export function fmtClock(sec: number) {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const t = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${t}` : `${m}:${t}`;
}

export default function BrowserPage() {
  const [params, setParams] = useSearchParams();
  const [shares, setShares] = useState<Share[] | null>(null);
  const [error, setError] = useState("");
  const [showHidden, setShowHidden] = useState(() => localStorage.getItem(HIDDEN_KEY) === "1");

  useEffect(() => {
    api.shares().then(setShares, (e) => setError(errMsg(e)));
  }, []);

  const toggleHidden = (v: boolean) => {
    setShowHidden(v);
    localStorage.setItem(HIDDEN_KEY, v ? "1" : "0");
  };

  if (error) return <p className="error">{error}</p>;
  if (!shares) return <p className="loading">加载中…</p>;
  if (shares.length === 0)
    return (
      <>
        <h2>文件浏览</h2>
        <p className="muted">管理员还没有设置共享目录。管理员需要在运行服务的电脑上打开本页面，在“共享目录”中添加。</p>
      </>
    );

  const shareId = Number(params.get("share")) || shares[0].id;
  const share = shares.find((s) => s.id === shareId) ?? shares[0];
  const path = params.get("path") ?? "";
  const q = params.get("q") ?? "";

  const go = (next: { share?: number; path?: string; q?: string }) => {
    const p = new URLSearchParams();
    p.set("share", String(next.share ?? share.id));
    const np = next.path ?? path;
    if (np) p.set("path", np);
    if (next.q) p.set("q", next.q);
    setParams(p);
  };

  return (
    <>
      {shares.length > 1 && (
        <div className="filters">
          {shares.map((s) => (
            <button key={s.id} className={s.id === share.id ? "on" : undefined} onClick={() => go({ share: s.id, path: "" })}>
              {s.name}
            </button>
          ))}
        </div>
      )}
      <Toolbar
        key={`${share.id}/${path}/${q}`}
        share={share}
        path={path}
        q={q}
        showHidden={showHidden}
        onHidden={toggleHidden}
        go={go}
      />
      <Listing share={share} path={path} q={q} showHidden={showHidden} go={go} />
    </>
  );
}

type Go = (next: { share?: number; path?: string; q?: string }) => void;

function Toolbar({
  share,
  path,
  q,
  showHidden,
  onHidden,
  go,
}: {
  share: Share;
  path: string;
  q: string;
  showHidden: boolean;
  onHidden: (v: boolean) => void;
  go: Go;
}) {
  const [input, setInput] = useState(q);
  const parts = path ? path.split("/") : [];
  const submit = (e: FormEvent) => {
    e.preventDefault();
    go({ q: input.trim() });
  };
  return (
    <div className="browser-head">
      <nav className="crumbs" aria-label="当前位置">
        <button className="link" onClick={() => go({ path: "" })}>
          {share.name}
        </button>
        {parts.map((name, i) => (
          <span key={i}>
            <span className="muted"> / </span>
            <button className="link" onClick={() => go({ path: parts.slice(0, i + 1).join("/") })}>
              {name}
            </button>
          </span>
        ))}
      </nav>
      <div className="filters quick-links">
        <button className={path === "" && !q ? "on" : undefined} onClick={() => go({ path: "", q: "" })}>
          ⤒ 顶层目录
        </button>
        {share.quick.map((f) => (
          <button key={f.path} className={path === f.path && !q ? "on" : undefined} onClick={() => go({ path: f.path, q: "" })}>
            {f.name}
          </button>
        ))}
      </div>
      <form className="row" onSubmit={submit}>
        <input
          className="grow"
          type="search"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="在当前目录及子目录中按文件名搜索"
        />
        <button type="submit">搜索</button>
        {q && (
          <button type="button" className="secondary" onClick={() => go({ q: "" })}>
            清除
          </button>
        )}
        <label className="inline">
          <input type="checkbox" checked={showHidden} onChange={(e) => onHidden(e.target.checked)} />
          显示隐藏文件
        </label>
      </form>
    </div>
  );
}

function Listing({ share, path, q, showHidden, go }: { share: Share; path: string; q: string; showHidden: boolean; go: Go }) {
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Entry | null>(null);
  const [detail, setDetail] = useState<Entry | null>(null);

  const load = useCallback(() => {
    setEntries(null);
    setError("");
    const req = q
      ? api.search(share.id, path, q, showHidden)
      : api.list(share.id, path, showHidden).then((r) => ({ entries: r.entries, truncated: false }));
    let stale = false;
    req.then(
      (r) => {
        if (stale) return;
        setEntries(r.entries);
        setTruncated(r.truncated);
      },
      (e) => !stale && setError(errMsg(e)),
    );
    return () => {
      stale = true;
    };
  }, [share.id, path, q, showHidden]);
  useEffect(load, [load]);

  if (error) return <p className="error">{error}</p>;
  if (!entries) return <p className="loading">{q ? "搜索中…" : "加载中…"}</p>;

  const open = (e: Entry) => (e.isDir ? go({ path: e.path, q: "" }) : setPreview(e));
  // 预览弹窗中左右切换只在可预览的文件之间进行
  const viewable = entries.filter((e) => !e.isDir && e.kind !== "binary");

  return (
    <>
      {q && (
        <p className="muted">
          找到 {entries.length} 项{truncated && "（结果过多或搜索超时，只显示部分，请换个更精确的关键字）"}
        </p>
      )}
      {entries.length === 0 ? (
        <p className="muted">{q ? "没有匹配的文件。" : "空目录。"}</p>
      ) : (
        <ul className="file-list">
          {entries.map((e) => (
            <li key={e.path}>
              <button className={e.kind === "binary" ? "file-row binary" : "file-row"} onClick={() => open(e)}>
                <span className="file-icon">
                  {e.kind === "image" ? (
                    <img src={fileURL(share.id, e.path)} alt="" loading="lazy" />
                  ) : e.kind === "video" ? (
                    <VideoThumb thumb={thumbURL(share.id, e.path)} src={fileURL(share.id, e.path)} />
                  ) : (
                    <KindIcon kind={e.kind} />
                  )}
                </span>
                <span className="file-name">
                  {e.name}
                  {q && <small className="muted file-dir">{e.path.slice(0, -e.name.length - 1) || "/"}</small>}
                </span>
                <span className="file-size muted">{e.isDir ? <span className="dir-info-slot" /> : fmtSize(e.size)}</span>
                <span className="file-time muted">{fmtTime(e.modTime)}</span>
              </button>
              {/* 不能嵌套在行按钮里，用绝对定位放到大小一栏 */}
              {e.isDir && (
                <button className="secondary dir-info-btn" onClick={() => setDetail(e)}>
                  文件夹详情
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {preview && (
        <Preview
          key={preview.path}
          share={share}
          entry={preview}
          siblings={viewable}
          onNavigate={setPreview}
          onClose={() => setPreview(null)}
        />
      )}
      {detail && <DirInfoModal share={share} entry={detail} onClose={() => setDetail(null)} />}
    </>
  );
}

/** 文件夹详情：打开时才统计大小，关闭时取消请求 */
function DirInfoModal({ share, entry, onClose }: { share: Share; entry: Entry; onClose: () => void }) {
  const [info, setInfo] = useState<DirInfo | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const ac = new AbortController();
    api.dirInfo(share.id, entry.path, ac.signal).then(setInfo, (e) => {
      if (!ac.signal.aborted) setError(errMsg(e));
    });
    return () => ac.abort();
  }, [share.id, entry.path]);

  const confirm = useConfirm();
  // 打包任务：id 为空表示未开始；started 表示已触发浏览器下载
  const [zip, setZip] = useState<{ id: string; done: number; total: number; started?: boolean } | null>(null);
  const [zipError, setZipError] = useState("");
  const zipIdRef = useRef("");

  // 每 500ms 查询打包进度，完成后触发下载
  const zipId = zip?.id ?? "";
  const zipStarted = !!zip?.started;
  useEffect(() => {
    if (!zipId || zipStarted) return;
    let stop = false;
    const tick = async () => {
      try {
        const s = await api.zipStatus(zipId);
        if (stop) return;
        if (s.status === "error") throw new Error(s.error || "打包失败");
        if (s.status === "done") {
          // 下载开始后服务器发送完会自行删除压缩包，这里不再取消任务
          zipIdRef.current = "";
          const a = document.createElement("a");
          a.href = zipURL(zipId);
          a.download = `${entry.name}.zip`;
          document.body.appendChild(a);
          a.click();
          a.remove();
          setZip({ id: zipId, done: s.total, total: s.total, started: true });
          return;
        }
        setZip({ id: zipId, done: s.done, total: s.total });
        timer = window.setTimeout(tick, 500);
      } catch (e) {
        if (stop) return;
        zipIdRef.current = "";
        setZip(null);
        setZipError(errMsg(e));
      }
    };
    let timer = window.setTimeout(tick, 300);
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [zipId, zipStarted, entry.name]);

  // 关闭弹窗时取消还没打包完的任务，服务器会删除临时文件
  useEffect(
    () => () => {
      if (zipIdRef.current) api.deleteZip(zipIdRef.current).catch(() => {});
    },
    [],
  );

  const startZip = async () => {
    const ok = await confirm({
      title: "即将下载文件夹",
      message: `即将下载文件夹「${entry.name}」（${info?.truncated ? "超过 " : ""}${fmtSize(info?.size ?? 0)}）。`,
      confirmText: "打包并下载",
    });
    if (!ok) return;
    setZipError("");
    try {
      const { id } = await api.createZip(share.id, entry.path);
      zipIdRef.current = id;
      setZip({ id, done: 0, total: 0 });
    } catch (e) {
      setZipError(errMsg(e));
    }
  };

  const cancelZip = () => {
    if (zipIdRef.current) api.deleteZip(zipIdRef.current).catch(() => {});
    zipIdRef.current = "";
    setZip(null);
  };

  const pending = error ? "统计失败" : "计算中…";
  const more = info?.truncated ? "超过 " : "";
  const percent = zip && zip.total > 0 ? Math.min(100, Math.floor((zip.done / zip.total) * 100)) : 0;
  return (
    <Modal title="文件夹详情" className="dir-info" onClose={onClose}>
      <div className="modal-body">
        <dl className="info-list">
          <dt>名称</dt>
          <dd>{entry.name}</dd>
          <dt>位置</dt>
          <dd>
            {share.name}/{entry.path}
          </dd>
          <dt>修改时间</dt>
          <dd>{fmtTime(entry.modTime)}</dd>
          <dt>占用大小</dt>
          <dd>{info ? `${more}${fmtSize(info.size)}（${info.size.toLocaleString()} 字节）` : pending}</dd>
          <dt>包含</dt>
          <dd>{info ? `${more}${info.files.toLocaleString()} 个文件，${info.dirs.toLocaleString()} 个文件夹` : pending}</dd>
        </dl>
        {info?.truncated && <p className="muted text-note">文件夹内容过多，统计超时，以上为已统计的部分。</p>}
        {error && <p className="error">{error}</p>}
        {zipError && <p className="error">{zipError}</p>}
        {zip && !zip.started && (
          <div className="zip-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
            <div className="zip-progress-label">
              <span>正在打包…</span>
              <span className="muted">
                {zip.total > 0 ? `${percent}%（${fmtSize(zip.done)} / ${fmtSize(zip.total)}）` : "准备中"}
              </span>
            </div>
            <div className="zip-progress-track">
              <div className="zip-progress-bar" style={{ width: `${percent}%` }} />
            </div>
          </div>
        )}
        {zip?.started && <p className="muted text-note">打包完成，已开始下载。</p>}
        {info && (
          <div className="modal-foot">
            {zip && !zip.started ? (
              <button type="button" className="secondary" onClick={cancelZip}>
                取消打包
              </button>
            ) : (
              <button type="button" onClick={startZip}>
                {zip?.started ? "重新下载" : "下载"}
              </button>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}

/** 把 MediaError.code 翻译成可读原因，方便判断是网络、解码还是格式问题 */
function mediaHint(code: number): string {
  switch (code) {
    case 1:
      return "加载被中断，请重试";
    case 2:
      return "网络请求失败，可能是登录状态已过期或连接中断";
    case 3:
      return "浏览器无法解码该视频，编码格式不受支持";
    case 4:
      return "资源无法加载，或服务器返回的格式不被支持";
    default:
      return "浏览器无法播放该视频";
  }
}

const containerName = (c: string) =>
  c === "matroska" ? "MKV" : c === "mpegts" ? "MPEG-TS" : c.toUpperCase();

/** 已知编码兼容性差时给出具体原因，比通用的错误码提示更有用 */
function codecHint(info: MediaInfo | null, code: number): string {
  // 容器不对时说明原因：换封装不重新编码就能播，和「编码不支持」是两回事
  if (info && !info.playable) {
    const c = containerName(info.container);
    if (info.remuxable) return `该文件是 ${c} 封装，浏览器无法直接播放。换封装不重新编码，几秒就能转换`;
    if (info.codec) return `该文件是 ${c} 封装、${info.codec} 编码，浏览器解不了，需要重新转码（会比较慢）`;
    return `该文件是 ${c} 封装，浏览器无法直接播放，需要转换后才能观看`;
  }
  const codec = info?.codec ?? "";
  switch (codec) {
    case "AV1":
      return "该视频是 AV1 编码，多数手机浏览器解不了（iPhone 需 iOS 17 以上且为 iPhone 15 Pro 及更新机型）。点「转换编码」可在线观看，或下载后用 VLC 等播放器打开";
    case "HEVC":
      return "该视频是 HEVC(H.265) 编码，当前浏览器不支持。点「转换编码」可在线观看，或下载后播放";
    case "VP9":
      return "该视频是 VP9 编码，Safari 支持有限。点「转换编码」可在线观看，或下载后播放";
    default:
      return mediaHint(code);
  }
}

/** 编码名 → 浏览器能识别的 MIME codecs 参数 */
const codecMime: Record<string, string> = {
  "H.264": 'video/mp4; codecs="avc1.42E01E"',
  HEVC: 'video/mp4; codecs="hvc1.1.6.L93.B0"',
  AV1: 'video/mp4; codecs="av01.0.05M.08"',
  VP9: 'video/webm; codecs="vp9"',
  VP8: 'video/webm; codecs="vp8"',
  "MPEG-4": 'video/mp4; codecs="mp4v.20.8"',
};

/**
 * 浏览器能不能解这个编码。false 表示明确不支持 —— 可以立刻提示，
 * 不用等它把数据拉下来、试着解码很久才报错；true 表示支持或判断不了，交给浏览器自己试。
 */
function canPlayCodec(codec: string): boolean {
  const mime = codecMime[codec];
  if (!mime) return true;
  return document.createElement("video").canPlayType(mime) !== "";
}

/** Safari 原生支持 HLS，Chrome/Firefox 不支持，用来决定能不能边转边播 */
let hlsSupport: boolean | null = null;
function canPlayHLS() {
  if (hlsSupport === null)
    hlsSupport = document.createElement("video").canPlayType("application/vnd.apple.mpegurl") !== "";
  return hlsSupport;
}

function Preview({
  share,
  entry,
  siblings,
  onNavigate,
  onClose,
}: {
  share: Share;
  entry: Entry;
  siblings: Entry[];
  onNavigate: (e: Entry) => void;
  onClose: () => void;
}) {
  const ref = useRef<ModalHandle>(null);
  const [mediaError, setMediaError] = useState<{ code: number; message: string } | null>(null);
  const [info, setInfo] = useState<MediaInfo | null>(null);
  const [playSrc, setPlaySrc] = useState(""); // 非空时播放转码结果
  // 开始播放前先问一次有没有现成缓存，问完才给 <video> 挂 src，
  // 免得先播原文件失败、闪一下「需要转码」的提示
  const [srcChecked, setSrcChecked] = useState(false);
  const [transcode, setTranscode] = useState<{
    phase: "idle" | "starting" | "streaming" | "done" | "error";
    mode: "copy" | "encode";
    percent: number;
    readySec: number;
    error: string;
  }>({ phase: "idle", mode: "encode", percent: 0, readySec: 0, error: "" });
  // 容器不对但能无损封装时自动开始转换，只自动触发一次
  const autoRemux = useRef(false);
  const timer = useRef<number | undefined>(undefined);
  const streaming = useRef(false);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  // 缓冲状态与下载速度（字节/秒）。片源是单个文件时按「已缓冲比例 × 文件大小」估算
  const [buffering, setBuffering] = useState(true);
  // 移动端浏览器可能拦截自动播放（没有静音时 iOS 要求用户手势）。
  // 这种情况下不能一直显示「正在加载」，要给一个明确的「点击播放」
  const [needTap, setNeedTap] = useState(false);
  const [netSpeed, setNetSpeed] = useState(0);
  const [bufferedPct, setBufferedPct] = useState(0);
  const speedRef = useRef({ t: 0, bytes: 0, ema: 0 });
  const mediaSize = useRef(entry.size);
  // 从 HLS 切到 mp4 时记下播放位置，切完接着播，不从头开始
  const resumeAt = useRef<{ time: number; playing: boolean } | null>(null);
  const idx = siblings.findIndex((s) => s.path === entry.path);
  const prev = idx > 0 ? siblings[idx - 1] : null;
  const next = idx >= 0 && idx < siblings.length - 1 ? siblings[idx + 1] : null;

  // 切换到另一个文件时清掉上一个的播放与转码状态
  useEffect(() => {
    setMediaError(null);
    setInfo(null);
    setPlaySrc("");
    setTranscode({ phase: "idle", mode: "encode", percent: 0, readySec: 0, error: "" });
    autoRemux.current = false;
    streaming.current = false;
    resumeAt.current = null;
    mediaSize.current = entry.size;
    speedRef.current = { t: 0, bytes: 0, ema: 0 };
    setBuffering(true);
    setNetSpeed(0);
    setBufferedPct(0);
    setNeedTap(false);
    setSrcChecked(false);
    if (timer.current) window.clearInterval(timer.current);
  }, [entry.path, entry.size]);

  // 播放前查一次缓存：命中就直接播转码好的 mp4，没有才去播原文件。
  // 这样「已经转过的视频」再打开是秒开，也不会先失败一次再提示转码。
  useEffect(() => {
    if (entry.kind !== "video") {
      setSrcChecked(true);
      return;
    }
    let alive = true;
    api
      .transcodeLookup(share.id, entry.path)
      .then((r) => {
        if (!alive) return;
        if (r.cached) {
          if (r.status === "running") {
            // 还在转：走正常流程，可以边转边播
            void startTranscode();
            return;
          }
          mediaSize.current = r.bytes || entry.size;
          speedRef.current = { t: 0, bytes: 0, ema: 0 };
          setPlaySrc(transcodeURL(r.id));
          setTranscode({
            phase: "done",
            mode: r.mode === "copy" ? "copy" : "encode",
            percent: 100,
            readySec: 0,
            error: "",
          });
          return;
        }
        // 没有缓存：借用顺带返回的编码信息，在浏览器自己失败之前就把原因说清楚。
        // AV1/HEVC 这类解不了的编码，原来要等播放器拉一段数据、试解码很久才报错
        const info = r.encoding;
        if (!info) return;
        setInfo(info);
        if (!info.playable) {
          // 容器不对且只能换封装的（MKV 里装 H.264）直接开始无损封装
          if (info.remuxable) void startTranscode();
          else setMediaError({ code: 4, message: "" });
          return;
        }
        if (info.codec && !canPlayCodec(info.codec)) {
          setMediaError({ code: 4, message: "" });
        }
      })
      .catch(() => {
        // 查询失败不影响正常播放，按原样试原文件
      })
      .finally(() => {
        if (alive) setSrcChecked(true);
      });
    return () => {
      alive = false;
    };
    // 只在换文件时查一次；startTranscode 每次渲染都是新函数，放进依赖会反复触发
  }, [share.id, entry.path, entry.kind]);

  useEffect(
    () => () => {
      if (timer.current) window.clearInterval(timer.current);
    },
    [],
  );

  const stopPolling = () => {
    if (timer.current) {
      window.clearInterval(timer.current);
      timer.current = undefined;
    }
  };

  /**
   * 估算下载速度：浏览器的 progress 事件只给「已缓冲到第几秒」，没有字节数，
   * 所以用「已缓冲时长 ÷ 总时长 × 文件大小」换算。mp4 是顺序下载，误差不大。
   */
  const sampleBuffer = useCallback(() => {
    const v = videoRef.current;
    if (!v || !v.buffered.length) return;
    const dur = v.duration;
    if (!(dur > 0)) return;
    const end = v.buffered.end(v.buffered.length - 1);
    const ratio = Math.min(end / dur, 1);
    setBufferedPct(Math.round(ratio * 100));

    const size = mediaSize.current;
    if (!size) return; // HLS 分片没有确定的总大小，只显示缓冲比例
    const bytes = ratio * size;
    const now = performance.now();
    const s = speedRef.current;
    if (s.t > 0 && now - s.t >= 300 && bytes > s.bytes) {
      const inst = ((bytes - s.bytes) / (now - s.t)) * 1000;
      s.ema = s.ema > 0 ? s.ema * 0.55 + inst * 0.45 : inst;
      setNetSpeed(s.ema);
    }
    s.t = now;
    s.bytes = bytes;
  }, []);

  // 缓冲期间每 500ms 采一次样；有数据到达（progress）时也采
  useEffect(() => {
    if (!buffering) return;
    const id = window.setInterval(sampleBuffer, 500);
    return () => window.clearInterval(id);
  }, [buffering, sampleBuffer]);

  /** 转码已完成，切成合成好的 mp4：只有一个成品文件，进度条可以随意拖动 */
  const finishTranscode = (id: string, bytes: number) => {
    stopPolling();
    setMediaError(null);
    // 边转边播用的是 HLS「直播」语义（时长还在增长、不能往前拖），
    // 转完必须换成 mp4 才是正常视频。先记下看到哪儿，换完接着播。
    const v = videoRef.current;
    if (streaming.current && v) {
      resumeAt.current = { time: v.currentTime || 0, playing: !v.paused && !v.ended };
    }
    streaming.current = false;
    mediaSize.current = bytes;
    speedRef.current = { t: 0, bytes: 0, ema: 0 };
    setPlaySrc(transcodeURL(id));
    setTranscode((t) => ({ ...t, phase: "done", percent: 100, readySec: 0, error: "" }));
  };

  /** 首个分段已就绪，切到 HLS 边转边播 */
  const beginStream = (id: string, readySec: number) => {
    streaming.current = true;
    mediaSize.current = 0; // 分片没有固定总大小
    setMediaError(null);
    setPlaySrc(hlsURL(id));
    setTranscode((t) => ({ ...t, phase: "streaming", readySec }));
  };

  const startTranscode = async () => {
    setTranscode((t) => ({ ...t, phase: "starting", percent: 0, readySec: 0, error: "" }));
    setMediaError(null);
    try {
      const r = await api.transcode(share.id, entry.path);
      const mode = r.mode === "copy" ? "copy" : "encode";
      setTranscode((t) => ({ ...t, mode }));
      if (r.status === "error") {
        setTranscode({ phase: "error", mode, percent: 0, readySec: 0, error: "转码失败" });
        return;
      }
      // 缓存里已有成品，直接播完整文件
      if (r.status === "done") return finishTranscode(r.id, r.bytes);

      const canStream = canPlayHLS();
      if (canStream && r.ready) beginStream(r.id, 0);

      timer.current = window.setInterval(async () => {
        try {
          const st = await api.transcodeStatus(r.id);
          if (st.status === "error") {
            stopPolling();
            setTranscode((t) => ({
              ...t,
              phase: "error",
              percent: 0,
              readySec: 0,
              error: st.error || "转码失败",
            }));
            return;
          }
          // 转码结束：不论之前是否在边转边播，都切成完整 mp4（可自由拖动）
          if (st.status === "done") return finishTranscode(r.id, st.bytes);
          if (canStream && st.ready) {
            // 首个分段生成即可开播，转码在后面追着跑
            if (!streaming.current) beginStream(r.id, st.readySeconds);
            setTranscode((t) => ({
              ...t,
              mode: st.mode === "copy" ? "copy" : t.mode,
              phase: "streaming",
              percent: st.percent,
              readySec: st.readySeconds,
            }));
            return;
          }
          // 不支持 HLS 的浏览器只能等完整文件
          setTranscode((t) => ({
            ...t,
            mode: st.mode === "copy" ? "copy" : t.mode,
            percent: st.percent,
            readySec: st.readySeconds,
          }));
        } catch {
          // 单次查询失败不影响，下一轮继续
        }
      }, 800);
    } catch (e) {
      setTranscode((t) => ({ ...t, phase: "error", percent: 0, readySec: 0, error: errMsg(e) }));
    }
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).tagName === "VIDEO") return;
      if (e.key === "ArrowLeft" && prev) onNavigate(prev);
      if (e.key === "ArrowRight" && next) onNavigate(next);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [prev, next, onNavigate]);

  const src = fileURL(share.id, entry.path);
  // 只是容器不对（mkv/avi 改名成 .mp4）时走无损封装，文案上要说清「不重新编码」
  const remuxing = transcode.mode === "copy" || (!!info && !info.playable && info.remuxable);
  return (
    <Modal title={entry.name} className="image-viewer" onClose={onClose} ref={ref}>
      <div className="modal-body">
        {entry.kind === "image" && <img className="viewer-img" src={src} alt={entry.name} />}
        {entry.kind === "video" &&
          (transcode.phase === "starting" ? (
            <div className="viewer-broken">
              <span>{remuxing ? "正在无损封装…" : "正在转换编码…"}</span>
              <small>
                {remuxing && info
                  ? `文件实际是 ${containerName(info.container)} 封装，只换封装不重新编码，几秒就好`
                  : "正在生成第一段视频，马上开始播放"}
              </small>
            </div>
          ) : transcode.phase === "error" ? (
            <div className="viewer-broken">
              <span>转换失败</span>
              <small>{transcode.error}</small>
              <span className="row">
                <button type="button" className="secondary" onClick={startTranscode}>
                  重试
                </button>
                <a className="viewer-download" href={src} target="_blank" rel="noreferrer">
                  在新窗口直接打开
                </a>
              </span>
            </div>
          ) : mediaError ? (
            <div className="viewer-broken">
              <span>无法播放该视频</span>
              <small>
                {playSrc
                  ? "转码后的视频仍然无法播放，建议下载后用本地播放器打开"
                  : codecHint(info, mediaError.code)}
              </small>
              {info?.codec && !playSrc && (
                <small className="muted">
                  容器 {containerName(info.container)} · 视频 {info.codec}
                  {info.audio ? ` · 音频 ${info.audio}` : ""}
                </small>
              )}
              <span className="row">
                {!playSrc && (
                  <button type="button" className="secondary" onClick={startTranscode}>
                    {remuxing ? "无损转换" : "转换编码"}
                  </button>
                )}
                <a className="viewer-download" href={src} target="_blank" rel="noreferrer">
                  在新窗口直接打开
                </a>
              </span>
            </div>
          ) : (
            <>
              <div className="viewer-media">
                <video
                  ref={videoRef}
                  className="viewer-img"
                  src={playSrc || (srcChecked ? src : undefined)}
                  controls
                  autoPlay
                  playsInline
                  preload="auto"
                  onLoadStart={() => {
                    speedRef.current = { t: 0, bytes: 0, ema: 0 };
                    setNetSpeed(0);
                    setBufferedPct(0);
                    setBuffering(true);
                    setNeedTap(false);
                  }}
                  onProgress={sampleBuffer}
                  onWaiting={() => setBuffering(true)}
                  onPlaying={() => {
                    setBuffering(false);
                    setNeedTap(false);
                    setNetSpeed(0);
                  }}
                  onCanPlay={(ev) => {
                    // 自动播放被拦截时不会触发 playing，只会在 paused 上停住。
                    // 这里主动补一次 play()，被拒就说明需要用户手势，切成「点击播放」
                    const v = ev.currentTarget;
                    if (!v.paused) return;
                    void v.play().catch((err: DOMException) => {
                      if (err?.name === "NotAllowedError") setNeedTap(true);
                    });
                  }}
                  onTimeUpdate={() => {
                    // 已经在走了就把「缓冲中 / 点击播放」收起来：Safari 的 stalled 事件很吵，
                    // 只看时间有没有前进更可靠
                    setBuffering((b) => (b ? false : b));
                    setNeedTap((t) => (t ? false : t));
                  }}
                  onLoadedMetadata={(ev) => {
                    sampleBuffer();
                    // 换源后回到原来的位置，观看不中断
                    const r = resumeAt.current;
                    if (!r) return;
                    resumeAt.current = null;
                    const v = ev.currentTarget;
                    const end = Number.isFinite(v.duration) ? v.duration : 0;
                    if (r.time > 1 && (end === 0 || r.time < end - 1)) v.currentTime = r.time;
                    if (r.playing) void v.play().catch(() => {});
                  }}
                  onError={(e) => {
                    const err = e.currentTarget.error;
                    setMediaError({ code: err?.code ?? -1, message: err?.message ?? "" });
                    // 原始文件失败时才查真实容器与编码；转码文件失败说明转换没解决问题
                    if (!playSrc) {
                      api.codec(share.id, entry.path).then((d) => {
                        setInfo(d);
                        // 容器不对但编码是 H.264（mkv/avi 改名成 .mp4）：换封装几秒就好，
                        // 不必等用户再点一次按钮
                        if (!d.playable && d.remuxable && !autoRemux.current) {
                          autoRemux.current = true;
                          void startTranscode();
                        }
                      }, () => {});
                    }
                  }}
                />
                {buffering && !needTap && !mediaError && (
                  <div className="video-buffer">
                    <span className="video-buffer-ring" aria-hidden="true" />
                    <span>{netSpeed > 0 ? `缓冲中 · ${fmtSpeed(netSpeed)}` : "正在加载…"}</span>
                    {bufferedPct > 0 && <span className="video-buffer-pct">已缓冲 {bufferedPct}%</span>}
                  </div>
                )}
                {needTap && !mediaError && (
                  <button
                    type="button"
                    className="video-tap-play"
                    onClick={() => {
                      const v = videoRef.current;
                      if (!v) return;
                      void v
                        .play()
                        .then(() => setNeedTap(false))
                        .catch(() => {});
                    }}
                  >
                    点击播放
                  </button>
                )}
              </div>
              {transcode.phase === "streaming" && (
                <div className="transcode-bar">
                  <span>
                    {transcode.mode === "copy" ? "正在无损封装" : "正在转换"} {transcode.percent}%
                  </span>
                  <span className="muted">可播放至 {fmtClock(transcode.readySec)}</span>
                </div>
              )}
            </>
          ))}
        {entry.kind === "text" && <TextView share={share} entry={entry} />}
        {entry.kind === "binary" && (
          <div className="viewer-broken">
            <span>不支持预览该类型的文件</span>
            <small>可以下载后在本机打开</small>
          </div>
        )}
        <div className="row viewer-foot">
          <span className="muted">
            {fmtSize(entry.size)} · {fmtTime(entry.modTime)}
          </span>
          <span className="row">
            {siblings.length > 1 && idx >= 0 && (
              <>
                <button className="secondary" disabled={!prev} onClick={() => prev && onNavigate(prev)}>
                  上一个
                </button>
                <button className="secondary" disabled={!next} onClick={() => next && onNavigate(next)}>
                  下一个
                </button>
              </>
            )}
            <a className="viewer-download" href={fileURL(share.id, entry.path, true)}>
              下载
            </a>
          </span>
        </div>
      </div>
    </Modal>
  );
}

function TextView({ share, entry }: { share: Share; entry: Entry }) {
  const [data, setData] = useState<{ text: string; truncated: boolean } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let stale = false;
    api.readText(share.id, entry.path).then(
      (d) => !stale && setData(d),
      (e) => !stale && setError(errMsg(e)),
    );
    return () => {
      stale = true;
    };
  }, [share.id, entry.path]);

  if (error) return <p className="error">{error}</p>;
  if (!data) return <p className="muted">加载中…</p>;
  return (
    <>
      {data.truncated && <p className="muted text-note">文件较大，仅显示前 1MB，完整内容请下载查看。</p>}
      <pre className="text-view">{data.text || "（空文件）"}</pre>
    </>
  );
}
