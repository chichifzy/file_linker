import { useCallback } from "react";
import { api } from "../api";
import { useAction, useLoader } from "../hooks";
import { useConfirm } from "../components/Confirm";
import Messages from "../components/Messages";

const fmtTime = (unixSec: number) => new Date(unixSec * 1000).toLocaleString();

export default function InvitesPage() {
  const loader = useCallback(() => api.invites(), []);
  const { data, error, load } = useLoader(loader);
  const action = useAction(load);
  const confirm = useConfirm();

  const create = () =>
    action.run(async () => {
      const { code } = await api.createInvite();
      action.setNotice(`已生成邀请码 ${code}`);
    });

  const copy = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code);
      action.setNotice(`已复制 ${code}`);
    } catch {
      // 非 HTTPS 的局域网地址上剪贴板不可用
      confirm({ title: "邀请码", message: code, alert: true });
    }
  };

  const remove = async (code: string) => {
    if (await confirm({ message: `删除邀请码 ${code}？`, danger: true, confirmText: "删除" }))
      action.run(() => api.deleteInvite(code), "已删除");
  };

  const now = Date.now() / 1000;
  return (
    <>
      <div className="page-head">
        <h2>邀请码</h2>
        <button onClick={create} disabled={action.busy}>
          生成邀请码
        </button>
      </div>
      <Messages error={error || action.error} notice={action.notice} />
      <p className="muted">每个邀请码只能注册一个普通用户账号。</p>
      {data &&
        (data.length === 0 ? (
          <p className="muted">还没有邀请码。</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>邀请码</th>
                <th>状态</th>
                <th>有效期至</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {data.map((inv) => (
                <tr key={inv.code}>
                  <td className="code">{inv.code}</td>
                  <td>
                    {inv.usedBy ? (
                      <span className="tag gray">已被 {inv.usedBy} 使用</span>
                    ) : inv.expiresAt < now ? (
                      <span className="tag red">已过期</span>
                    ) : (
                      <span className="tag green">可用</span>
                    )}
                  </td>
                  <td>{fmtTime(inv.expiresAt)}</td>
                  <td className="row">
                    {!inv.usedBy && inv.expiresAt >= now && (
                      <button className="secondary" onClick={() => copy(inv.code)}>
                        复制
                      </button>
                    )}
                    <button className="secondary" onClick={() => remove(inv.code)}>
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
