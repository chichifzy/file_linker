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
        <p className="muted">
          已用 {fmtSize(data.used)}
          {limit > 0 ? ` / 上限 ${fmtSize(limit)}` : "（不限制容量，永久保留）"}
          {limit > 0 && "，超出上限时自动淘汰最久未使用的缓存"}
        </p>
      )}
      {data &&
        (data.entries.length === 0 ? (
          <p className="muted">还没有转码缓存。</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>文件</th>
                <th>来源</th>
                <th>占用</th>
                <th>状态</th>
                <th>创建时间</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {data.entries.map((e) => (
                <tr key={e.id}>
                  <td>
                    {e.name}
                    {e.duration > 0 && <span className="muted"> · {fmtClock(e.duration)}</span>}
                  </td>
                  <td className="code path-cell">{e.shareName ? `${e.shareName}/${e.rel}` : e.rel}</td>
                  <td>{fmtSize(e.bytes)}</td>
                  <td className={e.status === "error" ? "error" : undefined} title={e.error}>
                    {statusText(e)}
                  </td>
                  <td>{new Date(e.created * 1000).toLocaleString()}</td>
                  <td>
                    <button className="secondary" onClick={() => removeOne(e)} disabled={action.busy}>
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
    </>
  );
}

function statusText(e: MediaCacheItem) {
  if (e.status === "running") return `转换中 ${e.percent}%`;
  if (e.status === "error") return "转换失败";
  return "已缓存";
}
