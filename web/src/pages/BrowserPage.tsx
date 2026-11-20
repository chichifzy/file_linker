import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { api, errMsg, fileURL, zipURL, type DirInfo, type Entry, type Share } from "../api";
import { useConfirm } from "../components/Confirm";
import Modal, { type ModalHandle } from "../components/Modal";
import { KindIcon, PlayBadge } from "../components/FileIcons";

const HIDDEN_KEY = "showHidden";

/** 视频缩略图：滚动到可见时才加载，#t=0.1 让浏览器（尤其 Safari）显示第一帧 */
function VideoThumb({ src }: { src: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [visible, setVisible] = useState(false);
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
  if (failed) return <KindIcon kind="video" />;
  return (
    <span ref={ref} className="video-thumb">
      {visible && <video src={`${src}#t=0.1`} preload="metadata" muted playsInline onError={() => setFailed(true)} />}
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
                    <VideoThumb src={fileURL(share.id, e.path)} />
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
        {zip?.started && <p className="muted text-note">打包完成，已开始下载。下载完成后服务器上的压缩包会自动删除。</p>}
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
  const idx = siblings.findIndex((s) => s.path === entry.path);
  const prev = idx > 0 ? siblings[idx - 1] : null;
  const next = idx >= 0 && idx < siblings.length - 1 ? siblings[idx + 1] : null;

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
  return (
    <Modal title={entry.name} className="image-viewer" onClose={onClose} ref={ref}>
      <div className="modal-body">
        {entry.kind === "image" && <img className="viewer-img" src={src} alt={entry.name} />}
        {entry.kind === "video" && <video className="viewer-img" src={src} controls autoPlay playsInline />}
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
