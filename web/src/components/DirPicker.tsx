import { useEffect, useState } from "react";
import { api, errMsg, type HostDirs } from "../api";

/** 浏览运行服务的电脑上的目录，用于选共享目录、缓存目录等。 */
export default function DirPicker({
  onPick,
  onCancel,
  busy,
  pickLabel = "选这个目录",
}: {
  onPick: (path: string) => void;
  onCancel: () => void;
  busy: boolean;
  /** 确认按钮的文案 */
  pickLabel?: string;
}) {
  const [dirs, setDirs] = useState<HostDirs | null>(null);
  const [error, setError] = useState("");

  const open = (path: string) => {
    setError("");
    api.hostDirs(path).then(setDirs, (e) => setError(errMsg(e)));
  };
  useEffect(() => open(""), []);

  return (
    <div className="card">
      <div className="card-head">
        <h3>选择目录</h3>
        <span className="code path-cell grow">{dirs?.path || "常用位置"}</span>
      </div>
      {error && <p className="error">{error}</p>}
      <div className="dir-list">
        {dirs && dirs.path && (
          <button className="dir-item" onClick={() => open(dirs.parent ?? "")}>
            ⬆ 上一级
          </button>
        )}
        {dirs?.dirs.map((d) => (
          <button key={d.path} className="dir-item" onClick={() => open(d.path)}>
            📁 {d.name}
          </button>
        ))}
        {dirs && dirs.dirs.length === 0 && <p className="muted">没有子目录</p>}
      </div>
      <div className="row actions">
        <button className="secondary" onClick={onCancel}>
          取消
        </button>
        <button disabled={!dirs?.path || busy} onClick={() => dirs && onPick(dirs.path)}>
          {pickLabel}
        </button>
      </div>
    </div>
  );
}
