import { useCallback, useEffect, useState } from "react";
import { api, type MediaCacheItem } from "../api";
import { useAction, useLoader } from "../hooks";
import { useConfirm } from "../components/Confirm";
import DirPicker from "../components/DirPicker";
import Messages from "../components/Messages";
import { fmtClock, fmtSize } from "./BrowserPage";

/** 转码缓存：浏览器放不了的视频转成 H.264 后留在这里，可以按条删除。 */
export default function CachePage() {
  const loader = useCallback(() => api.mediaList(), []);
  const { data, error, load } = useLoader(loader);
  const action = useAction(load);
  const confirm = useConfirm();
  // 修改缓存目录：编辑中、输入框内容、是否在浏览目录
  const [editing, setEditing] = useState(false);
  const [dirInput, setDirInput] = useState("");
  const [picking, setPicking] = useState(false);

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

  const startEdit = () => {
    setDirInput(data?.dir ?? "");
    setPicking(false);
    setEditing(true);
  };

  const saveDir = async () => {
    const dir = dirInput.trim();
    if (!dir || !data) return;
    const n = data.entries.length;
    const ok = await confirm({
      title: "修改缓存目录",
      message:
        n > 0
          ? `把 ${n} 个缓存（${fmtSize(data.used)}）迁移到「${dir}」，完成后原目录会被清空。迁移期间请不要关闭服务。`
          : `把缓存目录改为「${dir}」。`,
      confirmText: n > 0 ? "开始迁移" : "确定修改",
    });
    if (!ok) return;
    if (await action.run(() => api.setMediaDir(dir), n > 0 ? `已迁移 ${n} 个缓存` : "缓存目录已修改")) {
      setEditing(false);
    }
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
      {data?.dir && (
        <div className="card cache-dir">
          <div className="cache-dir-head">
            <div className="cache-dir-text">
              <span className="cache-dir-label">缓存位置</span>
              <code className="cache-dir-path" title={data.dir}>
                {data.dir}
              </code>
            </div>
            {data.editable && !editing && (
              <button className="secondary" onClick={startEdit} disabled={action.busy}>
                修改目录
              </button>
            )}
          </div>
          {editing && (
            <div className="cache-dir-form">
              <input
                value={dirInput}
                onChange={(e) => setDirInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") void saveDir();
                  if (e.key === "Escape") setEditing(false);
                }}
                placeholder="例如 D:\\video-cache（请选一个空目录）"
                spellCheck={false}
                autoFocus
                disabled={action.busy}
              />
              <div className="row actions">
                <button className="secondary" onClick={() => setPicking(true)} disabled={action.busy}>
                  浏览本机目录
                </button>
                {data.defaultDir && dirInput !== data.defaultDir && (
                  <button className="secondary" onClick={() => setDirInput(data.defaultDir ?? "")} disabled={action.busy}>
                    用默认目录
                  </button>
                )}
                <button className="secondary" onClick={() => setEditing(false)} disabled={action.busy}>
                  取消
                </button>
                <button onClick={saveDir} disabled={action.busy || !dirInput.trim()}>
                  {action.busy ? "正在迁移…" : "保存并迁移"}
                </button>
              </div>
              <p className="muted cache-dir-hint">
                换到空间更大的磁盘时用这里。已有的缓存会自动搬到新目录，原目录随后清空，不用重新转换；
                正在转换的视频需要等它完成或在下面删除后再修改。
              </p>
              {picking && (
                <DirPicker
                  busy={action.busy}
                  pickLabel="用这个目录"
                  onCancel={() => setPicking(false)}
                  onPick={(p) => {
                    setDirInput(p);
                    setPicking(false);
                  }}
                />
              )}
            </div>
          )}
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
  if (e.status === "running") return e.mode === "copy" ? `封装中 ${e.percent}%` : `转换中 ${e.percent}%`;
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
