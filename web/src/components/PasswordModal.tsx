import { useRef, useState, type FormEvent } from "react";
import { api, errMsg } from "../api";
import Modal, { type ModalHandle } from "./Modal";

export default function PasswordModal({ onClose }: { onClose: () => void }) {
  const ref = useRef<ModalHandle>(null);
  const [oldPassword, setOld] = useState("");
  const [newPassword, setNew] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    if (newPassword !== confirm) return setError("两次输入的新密码不一致");
    setBusy(true);
    try {
      await api.changePassword(oldPassword, newPassword);
      setDone(true);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  const close = () => (ref.current ? ref.current.close(onClose) : onClose());

  return (
    <Modal title="修改密码" onClose={onClose} ref={ref}>
      <form className="modal-body" onSubmit={submit}>
        {done ? (
          <p className="success">密码已修改。</p>
        ) : (
          <>
            <label>
              当前密码
              <input type="password" value={oldPassword} onChange={(e) => setOld(e.target.value)} autoComplete="current-password" required />
            </label>
            <label>
              新密码
              <input type="password" value={newPassword} onChange={(e) => setNew(e.target.value)} autoComplete="new-password" minLength={8} placeholder="至少 8 位" required />
            </label>
            <label>
              确认新密码
              <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" minLength={8} required />
            </label>
            {error && <p className="error">{error}</p>}
          </>
        )}
        <div className="modal-foot">
          <button type="button" className="secondary" onClick={close}>
            {done ? "关闭" : "取消"}
          </button>
          {!done && (
            <button type="submit" disabled={busy}>
              保存
            </button>
          )}
        </div>
      </form>
    </Modal>
  );
}
