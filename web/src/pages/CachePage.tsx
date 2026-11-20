import { useCallback, useEffect } from "react";
import { api, type MediaCacheItem } from "../api";
import { useAction, useLoader } from "../hooks";
import { useConfirm } from "../components/Confirm";
import Messages from "../components/Messages";
import { fmtClock, fmtSize } from "./BrowserPage";

/** 转码缓存：浏览器放不了的视频转成 H.264 后留在这里，可以按条删除。 */
export default function CachePage() {
  const loader = useCallback(() => api.mediaList(), []);
  const { data, error, load } = useLoader(loader);
  const action = useAction(load);
  const confirm = useConfirm();

  // 有任务在转码时定期刷新，进度和占用会变化
  const running = data?.entries.some((e) => e.status === "running") ?? false;
  useEffect(() => {
    if (!running) return;
    const t = window.setInterval(load, 3000);
    return () => window.clearInterval(t);
  }, [running, load]);

  const removeOne = async (item: MediaCacheItem) => {
    const ok = await confirm({
      title: "删除转码缓存",
      message: `删除「${item.name}」的转码缓存（${fmtSize(item.bytes)}）？共享目录里的原文件不受影响。`,
      confirmText: "删除",
      danger: true,
    });
    if (ok) action.run(() => api.deleteMedia(item.id), "已删除缓存");
  };

  const clearAll = async () => {
    const ok = await confirm({
      title: "清空转码缓存",
      message: `将删除全部 ${data?.entries.length ?? 0} 个缓存，释放 ${fmtSize(data?.used ?? 0)}。共享目录里的原文件不受影响。`,
      confirmText: "全部清空",
      danger: true,
    });
    if (ok) action.run(() => api.clearMedia(), "已清空缓存");
  };

  const limit = data?.limit ?? 0;
  return (
    <>
      <div className="page-head">
        <h2>转码缓存</h2>
        {!!data?.entries.length && (
          <button className="secondary" onClick={clearAll} disabled={action.busy}>
            清空缓存
          </button>
        )}
      </div>
      <Messages error={error || action.error} notice={action.notice} />
      <p className="muted">
        手机等设备无法直接播放的视频（AV1、HEVC 等），转换编码后会在这里留一份 H.264 版本，下次打开直接播放。
        缓存放在运行服务的电脑上，默认一直保留，只有在这里删除才会释放空间；删除它不影响共享目录里的原文件。
      </p>
      {data && (
        <div className="cache-stats">
          <div className="cache-stat">
            <b>{data.entries.length}</b>
            <span>个视频</span>
          </div>
          <div className="cache-stat">
            <b>{fmtSize(data.used)}</b>
            <span>已用空间</span>
          </div>
          <div className="cache-stat">
            <b>{limit > 0 ? fmtSize(limit) : "不限"}</b>
            <span>{limit > 0 ? "容量上限" : "永久保留"}</span>
          </div>
        </div>
      )}
      {data &&
        (data.entries.length === 0 ? (
          <p className="muted">还没有转码缓存。</p>
        ) : (
          <div className="cache-list">
            <div className="cache-head" aria-hidden="true">
              <span>文件</span>
              <span>占用</span>
              <span>状态</span>
              <span>创建时间</span>
              <span />
            </div>
            {data.entries.map((e) => {
              const src = e.shareName ? `${e.shareName}/${e.rel}` : e.rel;
              return (
                <div className="cache-item" key={e.id}>
                  <div className="cache-main">
                    <div className="cache-name">
                      <span className="cache-title">{e.name}</span>
                      {e.duration > 0 && <span className="cache-dur">{fmtClock(e.duration)}</span>}
                    </div>
                    <div className="cache-src" title={src}>
                      {src}
                    </div>
                    {e.status === "running" && (
                      <div className="cache-bar">
                        <i style={{ width: `${Math.min(100, Math.max(0, e.percent))}%` }} />
                      </div>
                    )}
                  </div>
                  <div className="cache-size">{fmtSize(e.bytes)}</div>
                  <div className="cache-status">
                    <span className={`tag ${statusTone(e)}`} title={e.error}>
                      {statusText(e)}
                    </span>
                  </div>
                  <div className="cache-created" title={new Date(e.created * 1000).toLocaleString()}>
                    {fmtWhen(e.created)}
                  </div>
                  <div className="cache-act">
                    <button className="secondary" onClick={() => removeOne(e)} disabled={action.busy}>
                      删除
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        ))}
    </>
  );
}

function statusText(e: MediaCacheItem) {
  if (e.status === "running") return `转换中 ${e.percent}%`;
  if (e.status === "error") return "转换失败";
  return "已缓存";
}

function statusTone(e: MediaCacheItem) {
  if (e.status === "running") return "orange";
  if (e.status === "error") return "red";
  return "green";
}

/** 2026/11/20 19:42（秒没有意义，省下的宽度留给文件名） */
function fmtWhen(unixSec: number) {
  const d = new Date(unixSec * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
