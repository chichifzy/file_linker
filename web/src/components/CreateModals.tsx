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
