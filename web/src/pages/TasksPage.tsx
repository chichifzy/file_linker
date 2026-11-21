import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { api, type DownloadJob } from "../api";
import { useAction, useLoader } from "../hooks";
import { useConfirm } from "../components/Confirm";
import Messages from "../components/Messages";
import { fmtSize } from "../format";

/** 任务列表：按链接下载的任务，关掉弹窗后还能回到这里看进度、打开所在目录、重试。 */
export default function TasksPage() {
  const loadJobs = useCallback(() => api.downloads(), []);
  const { data, error, load } = useLoader(loadJobs);
  const { data: shares } = useLoader(useCallback(() => api.shares(), []));
  const action = useAction(load);
  const confirm = useConfirm();
  const nav = useNavigate();

  const jobs = data?.jobs ?? [];
  const running = jobs.filter((j) => j.status === "running").length;

  // 有任务在下载时频繁刷新，进度才不会一顿一顿的
  useEffect(() => {
    if (running === 0) return;
    const t = window.setInterval(load, 1200);
    return () => window.clearInterval(t);
  }, [running, load]);

  // 速度按两次采样的增量估算，和下载弹窗里的保持一致
  const samples = useRef<Record<string, { t: number; bytes: number }>>({});
  const [speeds, setSpeeds] = useState<Record<string, number>>({});
  useEffect(() => {
    if (!data) return;
    const now = performance.now();
    const next: Record<string, number> = {};
    for (const j of data.jobs) {
      if (j.status !== "running") continue;
      const prev = samples.current[j.id];
      if (prev) {
        const dt = now - prev.t;
        next[j.id] = dt > 0 ? Math.max(0, ((j.done - prev.bytes) / dt) * 1000) : 0;
      }
      samples.current[j.id] = { t: now, bytes: j.done };
    }
    setSpeeds(next);
  }, [data]);

  const shareName = (id: number) => shares?.find((s) => s.id === id)?.name ?? "";
  const location = (j: DownloadJob) => {
    const name = shareName(j.share);
    const dir = j.dir ? `/${j.dir}` : "";
    return `${name}${dir || "/"}`;
  };

  const cancel = async (j: DownloadJob) => {
    const ok = await confirm({
      title: "取消下载",
      message: `取消下载「${j.name || "未命名文件"}」？已经下载的部分会被删除，任务记录会留在列表里。`,
      confirmText: "取消下载",
      danger: true,
    });
    if (ok) action.run(() => api.cancelDownload(j.id), "已取消下载");
  };

  const remove = (j: DownloadJob) =>
    action.run(() => api.cancelDownload(j.id), "已移除这条记录");

  const retry = (j: DownloadJob) =>
    action.run(() => api.startDownload(j.share, j.dir, j.url), "已重新开始下载");

  const clearEnded = async () => {
    const n = jobs.filter((j) => j.status !== "running").length;
    const ok = await confirm({
      title: "清除记录",
      message: `清除 ${n} 条已结束的记录？已经下载好的文件不会被删除。`,
      confirmText: "清除",
      danger: true,
    });
    if (ok) action.run(() => api.clearDownloads(), "已清除记录");
  };

  return (
    <>
      <div className="page-head">
        <h2>任务列表</h2>
        {jobs.some((j) => j.status !== "running") && (
          <button className="secondary" onClick={clearEnded} disabled={action.busy}>
            清除已结束
          </button>
        )}
      </div>
      <Messages error={error || action.error} notice={action.notice} />
      {data &&
        (jobs.length === 0 ? (
          <p className="muted">还没有下载任务。</p>
        ) : (
          <div className="task-list">
            <div className="task-head" aria-hidden="true">
              <span>任务</span>
              <span>大小</span>
              <span>状态</span>
              <span>开始时间</span>
              <span />
            </div>
            {jobs.map((j) => {
              const percent = j.total > 0 ? Math.min(100, Math.floor((j.done / j.total) * 100)) : 0;
              const speed = speeds[j.id] ?? 0;
              return (
                <div className="task-item" key={j.id}>
                  <div className="task-main">
                    <div className="task-name" title={j.name || j.url}>
                      {j.status === "running" && !j.name ? "正在解析文件名…" : j.name || "未命名文件"}
                    </div>
                    <div className="task-src" title={j.url}>
                      {location(j)}
                    </div>
                    {j.status === "running" && (
                      <div className="task-bar">
                        <i
                          className={j.total > 0 ? undefined : "indeterminate"}
                          style={j.total > 0 ? { width: `${percent}%` } : undefined}
                        />
                      </div>
                    )}
                    {j.error && j.status !== "cancelled" && <div className="task-error">{j.error}</div>}
                  </div>
                  <div className="task-size">
                    {j.status === "running" && j.total > 0
                      ? `${fmtSize(j.done)} / ${fmtSize(j.total)}`
                      : j.done > 0
                        ? fmtSize(j.done)
                        : "—"}
                    {speed > 0 && <span className="task-speed">{fmtSize(speed)}/s</span>}
                  </div>
                  <div className="task-status">
                    <span className={`tag ${tone(j)}`}>{statusText(j, percent)}</span>
                  </div>
                  <div className="task-time" title={new Date(j.startedAt).toLocaleString()}>
                    {fmtWhen(j.startedAt)}
                  </div>
                  <div className="task-act">
                    {j.status === "running" ? (
                      <button className="secondary" onClick={() => cancel(j)} disabled={action.busy}>
                        取消
                      </button>
                    ) : (
                      <>
                        {j.status === "done" && (
                          <button
                            className="secondary"
                            onClick={() => nav(`/?share=${j.share}${j.dir ? `&path=${encodeURIComponent(j.dir)}` : ""}`)}
                          >
                            打开目录
                          </button>
                        )}
                        {(j.status === "error" || j.status === "cancelled") && (
                          <button className="secondary" onClick={() => retry(j)} disabled={action.busy}>
                            重试
                          </button>
                        )}
                        <button className="secondary" onClick={() => remove(j)} disabled={action.busy}>
                          移除
                        </button>
                      </>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        ))}
    </>
  );
}

function statusText(j: DownloadJob, percent: number) {
  switch (j.status) {
    case "running":
      return j.total > 0 ? `下载中 ${percent}%` : "下载中";
    case "done":
      return "已完成";
    case "cancelled":
      return "已取消";
    default:
      return "失败";
  }
}

function tone(j: DownloadJob) {
  switch (j.status) {
    case "running":
      return "orange";
    case "done":
      return "green";
    case "cancelled":
      return "gray";
    default:
      return "red";
  }
}

/** 2026/11/21 12:42（秒没有意义，省下的宽度留给文件名） */
function fmtWhen(ms: number) {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
