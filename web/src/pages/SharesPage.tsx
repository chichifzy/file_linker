import { useCallback, useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useAction, useLoader } from "../hooks";
import { useConfirm } from "../components/Confirm";
import DirPicker from "../components/DirPicker";
import Messages from "../components/Messages";

/** 共享目录设置：管理员在运行服务的电脑上，或本机允许后在其他设备上可用。 */
export default function SharesPage({ isLocal }: { isLocal: boolean }) {
  const loader = useCallback(() => api.shares(), []);
  const { data, error, load } = useLoader(loader);
  const action = useAction(load);
  const confirm = useConfirm();
  const [picking, setPicking] = useState(false);

  const remove = async (id: number, name: string) => {
    if (await confirm({ message: `取消共享“${name}”？目录中的文件不会被删除。`, confirmText: "取消共享", danger: true }))
      action.run(() => api.removeShare(id), "已取消共享");
  };

  return (
    <>
      <div className="page-head">
        <h2>共享目录</h2>
        {!picking && <button onClick={() => setPicking(true)}>添加共享目录</button>}
      </div>
      <Messages error={error || action.error} notice={action.notice} />
      <p className="muted">已登录的用户可以浏览这些目录及其子目录中的文件（只读）。</p>
      {isLocal && <RemoteEditToggle />}
      {picking && (
        <DirPicker
          onCancel={() => setPicking(false)}
          onPick={async (path) => {
            if (await action.run(() => api.addShare(path), "已添加共享目录")) setPicking(false);
          }}
          busy={action.busy}
          pickLabel="共享当前目录"
        />
      )}
      {data &&
        (data.length === 0 ? (
          <p className="muted">还没有共享目录。</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>路径</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {data.map((s) => (
                <tr key={s.id}>
                  <td>{s.name}</td>
                  <td className="code path-cell">{s.path}</td>
                  <td>
                    <button className="secondary" onClick={() => remove(s.id, s.name)}>
                      取消共享
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

/** 仅本机可见：是否允许管理员在其他设备上修改共享目录。 */
function RemoteEditToggle() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api.remoteShareEdit().then((r) => setEnabled(r.enabled), (e) => setError(errMsg(e)));
  }, []);

  const toggle = async (on: boolean) => {
    setError("");
    try {
      await api.setRemoteShareEdit(on);
      setEnabled(on);
    } catch (e) {
      setError(errMsg(e));
    }
  };

  return (
    <div className="card">
      <label className="setting-row">
        <span className="setting-text">
          <span className="setting-title">允许在其它设备更改共享目录</span>
          <span className="setting-desc">开启后，在其他设备上登录管理员账号也能添加、取消共享并浏览本机目录。不用时建议关闭。</span>
        </span>
        <input
          type="checkbox"
          role="switch"
          className="switch"
          checked={enabled ?? false}
          disabled={enabled === null}
          onChange={(e) => toggle(e.target.checked)}
        />
      </label>
      {error && <p className="error">{error}</p>}
    </div>
  );
}

