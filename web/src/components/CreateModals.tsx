import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError, errMsg } from "../api";
import Modal, { type ModalHandle } from "./Modal";
import { fmtSize } from "../format";

type Base = { shareId: number; path: string; dir: string };

/** 在当前目录新建文件夹 */
export function NewFolderModal({
  shareId,
  path,
  dir,
  onClose,
  onCreated,
}: Base & { onClose: () => void; onCreated: (name: string) => void }) {
  const ref = useRef<ModalHandle>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // 成功后先播收起动画，动画结束再卸载
  const close = (then: () => void) => (ref.current ? ref.current.close(then) : then());

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const n = name.trim();
    if (!n) {
      setError("请输入文件夹名称");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const r = await api.mkdir(shareId, path, n);
      close(() => onCreated(r.name));
    } catch (e) {
      setError(errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="新建文件夹" onClose={onClose} ref={ref}>
      <form className="modal-body" onSubmit={submit}>
        <label>
          文件夹名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：2026-11 照片"
            maxLength={200}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <p className="muted text-note">将创建在：{dir}</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-foot">
          <button type="button" className="secondary" onClick={() => close(onClose)}>
            取消
          </button>
          <button type="submit" disabled={busy || !name.trim()}>
            {busy ? "创建中…" : "创建"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/** 重命名：输入框里是完整文件名（含扩展名），默认只选中主文件名，避免误改后缀 */
export function RenameModal({
  shareId,
  path,
  name,
  dir,
  onClose,
  onRenamed,
}: Base & {
  /** 当前文件名，path 是它的相对路径 */
  name: string;
  onClose: () => void;
  onRenamed: (name: string) => void;
}) {
  const ref = useRef<ModalHandle>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState(name);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const close = (then: () => void) => (ref.current ? ref.current.close(then) : then());

  // Modal 的 effect 已经聚焦了输入框，这里再选中主文件名（不含最后一个点之后的部分）
  useEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    const dot = name.lastIndexOf(".");
    el.setSelectionRange(0, dot > 0 ? dot : name.length);
  }, [name]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const next = value.trim();
    if (!next) {
      setError("请输入名称");
      return;
    }
    if (next === name) {
      close(onClose);
      return;
    }
    setBusy(true);
    setError("");
    try {
      const r = await api.rename(shareId, path, next);
      close(() => onRenamed(r.name));
    } catch (e) {
      setError(errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="重命名" onClose={onClose} ref={ref}>
      <form className="modal-body" onSubmit={submit}>
        <label>
          名称
          <input
            ref={inputRef}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            maxLength={200}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <p className="muted text-note">位置：{dir}。</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-foot">
          <button type="button" className="secondary" onClick={() => close(onClose)}>
            取消
          </button>
          <button type="submit" disabled={busy || !value.trim()}>
            {busy ? "保存中…" : "保存"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

type Task = { id: string; done: number; total: number; name: string };
/** 让运行服务的电脑把链接指向的文件下载到当前目录，并显示进度 */
export function DownloadModal({
  shareId,
  path,
  dir,
  onClose,
  onFinished,
}: Base & { onClose: () => void; onFinished: (name: string) => void }) {
  const ref = useRef<ModalHandle>(null);
  const [url, setUrl] = useState("");
  const [task, setTask] = useState<Task | null>(null);
  const [error, setError] = useState("");
  const [doneName, setDoneName] = useState("");
  const [speed, setSpeed] = useState(0);
  // 任务 id 记在 ref 里，关窗时（或组件卸载时）还要用它取消任务
  const idRef = useRef("");
  const sample = useRef({ t: 0, bytes: 0 });

  const finished = useRef(onFinished);
  finished.current = onFinished;

  const close = (then: () => void) => (ref.current ? ref.current.close(then) : then());

  const id = task?.id ?? "";
  useEffect(() => {
    if (!id) return;
    let stop = false;
    let timer = 0;
    const tick = async () => {
      try {
        const s = await api.downloadStatus(id);
        if (stop) return;
        if (s.status === "error" || s.status === "cancelled") {
          // 也可能是在另一个标签页的任务列表里点了取消
          idRef.current = "";
          setTask(null);
          setSpeed(0);
          setError(s.error || (s.status === "cancelled" ? "下载已取消" : "下载失败"));
          return;
        }
        // 速度按两次采样的增量估算，波动大时用指数平均压一压
        const now = performance.now();
        const prev = sample.current;
        if (prev.t > 0 && s.done > prev.bytes) {
          const inst = ((s.done - prev.bytes) / (now - prev.t)) * 1000;
          setSpeed((v) => (v > 0 ? v * 0.6 + inst * 0.4 : inst));
        }
        sample.current = { t: now, bytes: s.done };
        setTask({ id, done: s.done, total: s.total, name: s.name });
        if (s.status === "done") {
          idRef.current = "";
          setDoneName(s.name || "文件");
          setSpeed(0);
          finished.current(s.name);
          return;
        }
        timer = window.setTimeout(tick, 500);
      } catch (e) {
        if (stop) return;
        idRef.current = "";
        setTask(null);
        setError(errMsg(e));
      }
    };
    timer = window.setTimeout(tick, 300);
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [id]);

  const start = async (e: FormEvent) => {
    e.preventDefault();
    if (task) return;
    const link = url.trim();
    if (!link) {
      setError("请填写下载链接");
      return;
    }
    if (!/^https?:\/\//i.test(link)) {
      setError("链接需要以 http:// 或 https:// 开头");
      return;
    }
    setError("");
    setDoneName("");
    sample.current = { t: 0, bytes: 0 };
    setSpeed(0);
    try {
      const r = await api.startDownload(shareId, path, link);
      idRef.current = r.id;
      setTask({ id: r.id, done: 0, total: 0, name: r.name });
    } catch (e) {
      setError(errMsg(e));
    }
  };

  /** 取消任务；已经下完的文件不会删除 */
  const cancel = async () => {
    if (idRef.current) await api.cancelDownload(idRef.current).catch(() => {});
    idRef.current = "";
    setTask(null);
    setSpeed(0);
  };

  const closeAndKeep = () => {
    // 关窗不中断下载（服务器会继续下完），但要让列表重新加载一次
    finished.current("已转入后台下载，可在左侧「任务列表」查看进度");
    onClose();
  };

  /** 「后台继续」：先播收起动画再关 */
  const keepRunning = () => close(closeAndKeep);

  const percent = task && task.total > 0 ? Math.min(100, Math.floor((task.done / task.total) * 100)) : 0;
  const running = !!task && !doneName;
  // 下载中关窗（Esc、点遮罩）等于「后台继续」，其余情况就是普通关闭
  const handleClose = () => (running ? close(closeAndKeep) : onClose());

  return (
    <Modal title="下载到当前目录" onClose={handleClose} ref={ref}>
      <div className="modal-body">
        {doneName ? (
          <>
            <p className="success">已下载完成：{doneName}</p>
            <p className="muted text-note">
              保存位置：{dir || "/"}
            </p>
            <div className="modal-foot">
              <button type="button" onClick={() => close(onClose)}>
                完成
              </button>
            </div>
          </>
        ) : running ? (
          <>
            <p className="muted text-note">
              正在由运行服务的电脑下载。关闭窗口不会中断，可以随时在左侧「任务列表」回来看进度。
            </p>
            <div
              className="zip-progress"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent}
            >
              <div className="zip-progress-label">
                <span className="dl-name">{task.name || "正在解析文件名…"}</span>
                <span className="muted">
                  {task.total > 0
                    ? `${percent}%（${fmtSize(task.done)} / ${fmtSize(task.total)}）`
                    : fmtSize(task.done)}
                  {speed > 0 && ` · ${fmtSize(speed)}/s`}
                </span>
              </div>
              <div className="zip-progress-track">
                <div
                  className={task.total > 0 ? "zip-progress-bar" : "zip-progress-bar indeterminate"}
                  style={task.total > 0 ? { width: `${percent}%` } : undefined}
                />
              </div>
            </div>
            <div className="modal-foot">
              <button type="button" className="secondary" onClick={keepRunning}>
                后台继续
              </button>
              <button type="button" className="danger" onClick={cancel}>
                取消下载
              </button>
            </div>
          </>
        ) : (
          <form className="dl-form" onSubmit={start}>
            <label>
              下载链接
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://example.com/file.zip"
                inputMode="url"
                autoComplete="off"
                spellCheck={false}
              />
            </label>
            <p className="muted text-note">
              文件会保存到：{dir || "/"}；文件名按响应头或链接自动确定，重名时自动加序号。
            </p>
            {error && <p className="error">{error}</p>}
            <div className="modal-foot">
              <button type="button" className="secondary" onClick={() => close(onClose)}>
                取消
              </button>
              <button type="submit" disabled={!url.trim()}>
                开始下载
              </button>
            </div>
          </form>
        )}
      </div>
    </Modal>
  );
}

type ExtractTask = { id: string; done: number; total: number };

/** 解压到压缩包所在的当前文件夹：没密码就直接开始，有密码时先让用户输入 */
export function ExtractModal({
  shareId,
  path,
  name,
  dir,
  onClose,
  onExtracted,
}: Base & {
  /** 压缩包文件名（path 是它的相对路径） */
  name: string;
  onClose: () => void;
  /** 完成或取消时通知列表刷新；msg 为空表示只刷新 */
  onExtracted: (msg: string) => void;
}) {
  const ref = useRef<ModalHandle>(null);
  const [phase, setPhase] = useState<"starting" | "password" | "running" | "done" | "error">("starting");
  const [pw, setPw] = useState("");
  const [error, setError] = useState("");
  const [task, setTask] = useState<ExtractTask | null>(null);
  const [result, setResult] = useState<{ files: number; skipped: number } | null>(null);
  const [speed, setSpeed] = useState(0);
  const idRef = useRef("");
  const sample = useRef({ t: 0, bytes: 0 });

  const extracted = useRef(onExtracted);
  extracted.current = onExtracted;

  const close = (then: () => void) => (ref.current ? ref.current.close(then) : then());

  const start = async (password: string) => {
    setError("");
    setPhase("starting");
    try {
      const r = await api.startExtract(shareId, path, password);
      idRef.current = r.id;
      sample.current = { t: 0, bytes: 0 };
      setSpeed(0);
      setTask({ id: r.id, done: 0, total: r.total });
      setPhase("running");
    } catch (e) {
      // 有密码的包：不用先把任务跑起来，直接请用户输入
      if (e instanceof ApiError && e.code === "need_password") {
        setPhase("password");
        return;
      }
      if (e instanceof ApiError && e.code === "wrong_password") {
        setPhase("password");
        setError("密码不正确，请重新输入");
        return;
      }
      setPhase("error");
      setError(errMsg(e));
    }
  };

  // 打开就试一次：没有密码的包直接开始解压
  useEffect(() => {
    void start("");
    // start 只用 props 与 setState，挂载时跑一次即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const id = task?.id ?? "";
  useEffect(() => {
    if (!id) return;
    let stop = false;
    let timer = 0;
    const tick = async () => {
      try {
        const s = await api.extractStatus(id);
        if (stop) return;
        if (s.status === "running") {
          // 速度按两次采样的增量估算，和下载弹窗一致
          const now = performance.now();
          const prev = sample.current;
          if (prev.t > 0 && s.done > prev.bytes) {
            const inst = ((s.done - prev.bytes) / (now - prev.t)) * 1000;
            setSpeed((v) => (v > 0 ? v * 0.6 + inst * 0.4 : inst));
          }
          sample.current = { t: now, bytes: s.done };
          setTask({ id, done: s.done, total: s.total });
          timer = window.setTimeout(tick, 400);
          return;
        }
        idRef.current = "";
        setTask(null);
        setSpeed(0);
        if (s.status === "done") {
          setResult({ files: s.files, skipped: s.skipped });
          setPhase("done");
          extracted.current(`已把「${name}」解压到${dir || "/"}`);
          return;
        }
        if (s.code === "need_password" || s.code === "wrong_password") {
          setPhase("password");
          setError(s.error || "密码不正确");
          return;
        }
        setPhase("error");
        setError(s.error || (s.status === "cancelled" ? "解压已取消" : "解压失败"));
      } catch (e) {
        if (stop) return;
        idRef.current = "";
        setTask(null);
        setPhase("error");
        setError(errMsg(e));
      }
    };
    timer = window.setTimeout(tick, 300);
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [id, name, dir]);

  const cancel = async () => {
    if (idRef.current) await api.cancelExtract(idRef.current).catch(() => {});
    idRef.current = "";
  };

  // 解压中关窗（Esc、点遮罩）等于取消：解压没有「后台继续」的去处
  const handleClose = () => {
    if (phase === "running") void cancel();
    onClose();
  };

  const percent = task && task.total > 0 ? Math.min(100, Math.floor((task.done / task.total) * 100)) : 0;
  const where = dir || "/";

  return (
    <Modal title="解压到当前文件夹" onClose={handleClose} ref={ref}>
      <div className="modal-body">
        {phase === "done" && result ? (
          <>
            <p className="success">已解压 {result.files} 个文件</p>
            <p className="muted text-note">
              位置：{where}
              {result.skipped > 0 && `；跳过 ${result.skipped} 个符号链接或无法安全解压的条目`}
            </p>
            <div className="modal-foot">
              <button type="button" onClick={() => close(onClose)}>
                完成
              </button>
            </div>
          </>
        ) : phase === "running" && task ? (
          <>
            <div
              className="zip-progress"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent}
            >
              <div className="zip-progress-label">
                <span className="dl-name">{name}</span>
                <span className="muted">
                  {task.total > 0
                    ? `${percent}%（${fmtSize(task.done)} / ${fmtSize(task.total)}）`
                    : fmtSize(task.done)}
                  {speed > 0 && ` · ${fmtSize(speed)}/s`}
                </span>
              </div>
              <div className="zip-progress-track">
                <div
                  className={task.total > 0 ? "zip-progress-bar" : "zip-progress-bar indeterminate"}
                  style={task.total > 0 ? { width: `${percent}%` } : undefined}
                />
              </div>
            </div>
            <p className="muted text-note">解压到：{where}；重名的文件会自动加序号，不会覆盖已有文件。</p>
            <div className="modal-foot">
              <button
                type="button"
                className="danger"
                onClick={() => {
                  void cancel();
                  onClose();
                }}
              >
                取消解压
              </button>
            </div>
          </>
        ) : phase === "error" ? (
          <>
            <p className="error">{error}</p>
            <div className="modal-foot">
              <button type="button" onClick={() => close(onClose)}>
                关闭
              </button>
            </div>
          </>
        ) : phase === "password" ? (
          <form
            className="dl-form"
            onSubmit={(e) => {
              e.preventDefault();
              if (pw) void start(pw);
            }}
          >
            <p className="muted text-note">
              「{name}」有密码，输入后解压到：{where}
            </p>
            <label>
              密码
              <input
                type="password"
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </label>
            {error && <p className="error">{error}</p>}
            <div className="modal-foot">
              <button type="button" className="secondary" onClick={() => close(onClose)}>
                取消
              </button>
              <button type="submit" disabled={!pw}>
                解压
              </button>
            </div>
          </form>
        ) : (
          <>
            <p className="muted">正在打开压缩包…</p>
            <div className="modal-foot">
              <button type="button" className="secondary" onClick={() => close(onClose)}>
                取消
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

type UpItem = {
  file: File;
  status: "waiting" | "uploading" | "done" | "error";
  /** 已上传字节数（不是百分比） */
  loaded: number;
  /** 指数平均后的瞬时速度，字节/秒 */
  speed: number;
  error?: string;
  savedName?: string;
};

/** 上传本机文件到当前目录。多选和拖拽都支持；手机上点「选择文件」调起系统文件选择器。 */
export function UploadModal({
  shareId,
  path,
  dir,
  onClose,
  onUploaded,
}: Base & { onClose: () => void; onUploaded: () => void }) {
  const ref = useRef<ModalHandle>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [items, setItems] = useState<UpItem[]>([]);
  const [conflicts, setConflicts] = useState<{ name: string; proposed: string }[] | null>(null);
  const [phase, setPhase] = useState<"pick" | "running" | "done">("pick");
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");
  const [uploaded, setUploaded] = useState(0);
  const controller = useRef<AbortController | null>(null);
  const alive = useRef(true);
  // 每个文件的上一次进度采样，用来估算上传速度
  const samples = useRef<{ t: number; bytes: number }[]>([]);

  useEffect(
    () => () => {
      alive.current = false;
      controller.current?.abort();
    },
    [],
  );

  // Modal 自己的 × / Esc / 点遮罩关闭后只会调 finishClose；
  // 我们自己的按钮则先请 Modal 播动画，动画结束再走 finishClose。
  // 不能在 finishClose 里再调一次 Modal.close：那会被它的防重入标志吞掉，弹窗就关不掉了。
  const finishClose = () => {
    controller.current?.abort();
    onClose();
  };
  const requestClose = () => (ref.current ? ref.current.close(finishClose) : finishClose());

  const addFiles = (files: FileList | File[]) => {
    const list = Array.from(files);
    if (!list.length) return;
    setError("");
    setConflicts(null);
    setPhase("pick");
    setItems((prev) => [
      ...prev,
      ...list.map((file) => ({ file, status: "waiting" as const, loaded: 0, speed: 0 })),
    ]);
  };

  const patch = (i: number, change: Partial<UpItem>) =>
    setItems((prev) => prev.map((it, idx) => (idx === i ? { ...it, ...change } : it)));

  // 真正开始传。rename 由冲突面板决定；不再用嵌套 confirm 弹窗（iOS 上会卡住）
  const runUpload = async (rename: boolean) => {
    setConflicts(null);
    setError("");
    setPhase("running");
    const ctrl = new AbortController();
    controller.current = ctrl;
    let done = 0;
    for (let i = 0; i < items.length; i++) {
      if (ctrl.signal.aborted || !alive.current) break;
      const item = items[i];
      samples.current[i] = { t: 0, bytes: 0 };
      patch(i, { status: "uploading", loaded: 0, speed: 0 });
      try {
        let result: { name: string };
        try {
          result = await api.uploadFile(
            shareId,
            path,
            item.file,
            rename,
            (loaded) => {
              if (!alive.current) return;
              const now = performance.now();
              const prev = samples.current[i];
              setItems((list) => {
                const cur = list[i];
                if (!cur) return list;
                let speed = cur.speed;
                // 两次进度事件可能落在同一毫秒内（本地传小文件时很常见），
                // 间隔为 0 会除出 Infinity，必须要求至少过了 1 毫秒
                if (prev && prev.t > 0 && loaded > prev.bytes && now > prev.t) {
                  const inst = ((loaded - prev.bytes) / (now - prev.t)) * 1000;
                  // 波动大时用指数平均压一压，和下载任务一个口径
                  if (Number.isFinite(inst)) {
                    speed = speed > 0 ? speed * 0.6 + inst * 0.4 : inst;
                  }
                }
                const next = [...list];
                next[i] = { ...cur, loaded, speed };
                return next;
              });
              samples.current[i] = { t: now, bytes: loaded };
            },
            ctrl.signal,
          );
        } catch (e) {
          // 预检之后刚好有人放了同名文件进来：不再追问，跳过这一个，其余继续
          if (e instanceof ApiError && e.code === "name_conflict") {
            patch(i, { status: "error", error: "已存在同名文件，已跳过" });
            continue;
          }
          throw e;
        }
        if (!alive.current) break;
        patch(i, { status: "done", loaded: item.file.size, speed: 0, savedName: result.name });
        done++;
      } catch (e) {
        if (!alive.current) break;
        if (e instanceof DOMException && e.name === "AbortError") break;
        patch(i, { status: "error", error: errMsg(e) });
      }
    }
    if (!alive.current) return;
    controller.current = null;
    setUploaded(done);
    setPhase("done");
    if (done > 0) onUploaded();
  };

  const start = async () => {
    if (!items.length || phase !== "pick") return;
    setError("");
    let found: { name: string; proposed: string }[] = [];
    try {
      const r = await api.checkUpload(shareId, path, items.map((it) => it.file.name));
      found = r.conflicts ?? [];
    } catch (e) {
      setError(errMsg(e));
      return;
    }
    // 同一批里选了两个同名文件，服务端预检时都还不存在，也要算冲突
    const picked = new Set<string>();
    for (const it of items) {
      if (picked.has(it.file.name)) {
        found = [
          ...found,
          { name: it.file.name, proposed: it.file.name.replace(/(\.[^.]*)?$/, " (1)$1") },
        ];
      }
      picked.add(it.file.name);
    }
    if (found.length) {
      setConflicts(found); // 在弹窗里就地询问，取消就什么都不传
      return;
    }
    await runUpload(false);
  };

  const totalSize = items.reduce((sum, it) => sum + it.file.size, 0);
  const failed = items.filter((it) => it.status === "error").length;

  return (
    <Modal title="上传文件" onClose={finishClose} ref={ref}>
      <div className="modal-body up-body">
        <p className="muted text-note">上传到：{dir}</p>

        {phase === "pick" ? (
          <>
            <div
              className={`up-drop${dragging ? " dragging" : ""}`}
              onDragOver={(e) => {
                e.preventDefault();
                setDragging(true);
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={(e) => {
                e.preventDefault();
                setDragging(false);
                addFiles(e.dataTransfer.files);
              }}
            >
              <div className="up-drop-icon" aria-hidden="true">
                ↑
              </div>
              <strong>把文件拖到这里，或点下面按钮选择</strong>
              <small>iPhone / iPad 从「文件」或照片里选，Windows / Mac 可多选或拖拽；最多 200 个，单个不超过 8 GB</small>
              <input
                ref={inputRef}
                type="file"
                multiple
                className="up-file-input"
                onChange={(e) => {
                  if (e.target.files) addFiles(e.target.files);
                  e.target.value = ""; // 允许再次选同一个文件
                }}
              />
              <button type="button" className="secondary" onClick={() => inputRef.current?.click()}>
                选择文件
              </button>
            </div>

            {items.length > 0 ? (
              <ul className="up-list">
                {items.map((it, i) => (
                  <li key={`${it.file.name}-${i}`}>
                    <span className="up-name" title={it.file.name}>
                      {it.file.name}
                    </span>
                    <small>{fmtSize(it.file.size)}</small>
                    <button
                      type="button"
                      className="up-remove"
                      aria-label={`移除 ${it.file.name}`}
                      onClick={() => setItems((prev) => prev.filter((_, idx) => idx !== i))}
                    >
                      ×
                    </button>
                  </li>
                ))}
              </ul>
            ) : null}
            {items.length > 0 ? <p className="muted up-total">共 {items.length} 个文件，{fmtSize(totalSize)}</p> : null}
            {error ? <p className="error">{error}</p> : null}
            {conflicts ? (
              <div className="up-conflict">
                <p>当前文件夹已经有同名文件。取消这次操作就不会上传任何文件，已选的文件还会留在这里。</p>
                <ul className="up-conflict-list">
                  {conflicts.slice(0, 5).map((c, i) => (
                    <li key={`${c.name}-${i}`}>
                      「{c.name}」→「<b className="up-conflict-name">{c.proposed}</b>」
                    </li>
                  ))}
                </ul>
                {conflicts.length > 5 ? (
                  <p className="muted">还有 {conflicts.length - 5} 个同名文件也会加序号。</p>
                ) : null}
                <div className="up-conflict-actions">
                  <button type="button" className="secondary" onClick={() => setConflicts(null)}>
                    取消
                  </button>
                  <button type="button" onClick={() => void runUpload(true)}>
                    继续上传
                  </button>
                </div>
              </div>
            ) : (
              <div className="modal-foot">
                <button type="button" className="secondary" onClick={requestClose}>
                  取消
                </button>
                <button type="button" disabled={!items.length} onClick={start}>
                  开始上传
                </button>
              </div>
            )}
          </>
        ) : (
          <>
            <ul className="up-list up-progress-list">
              {items.map((it, i) => (
                <li key={`${it.file.name}-${i}`}>
                  <div className="up-row">
                    <span className="up-name" title={it.savedName ?? it.file.name}>
                      {it.savedName ?? it.file.name}
                    </span>
                    <small>
                      {it.status === "done"
                        ? "完成"
                        : it.status === "error"
                          ? it.error
                          : it.status === "uploading"
                            ? `${fmtSize(it.loaded)} / ${fmtSize(it.file.size)}${it.speed > 0 ? ` · ${fmtSize(it.speed)}/s` : ""}`
                            : "等待中"}
                    </small>
                  </div>
                  {it.status === "uploading" || it.status === "done" ? (
                    <div className="up-bar">
                      <span
                        style={{
                          width: `${
                            it.file.size > 0 ? Math.min(100, (it.loaded / it.file.size) * 100) : 100
                          }%`,
                        }}
                      />
                    </div>
                  ) : null}
                </li>
              ))}
            </ul>
            <div className="modal-foot">
              {phase === "running" ? (
                <button type="button" className="secondary" onClick={requestClose}>
                  取消上传
                </button>
              ) : (
                <>
                  <p className="up-summary">
                    已上传 {uploaded} 个文件{failed ? `，${failed} 个失败` : ""}
                  </p>
                  <button type="button" onClick={requestClose}>
                    完成
                  </button>
                </>
              )}
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
