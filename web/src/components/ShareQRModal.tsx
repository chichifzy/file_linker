import { useEffect, useState } from "react";
import QRCode from "qrcode";
import { api, errMsg } from "../api";
import Modal from "./Modal";

const loopbackHosts = ["localhost", "127.0.0.1", "[::1]", "::1"];

/** 显示当前页面地址的二维码，同一局域网的设备扫码即可在浏览器打开。 */
export default function ShareQRModal({ onClose }: { onClose: () => void }) {
  const { protocol, hostname, port, pathname, search } = window.location;
  const isLoopback = loopbackHosts.includes(hostname);
  // 通过 localhost 打开时，别的设备无法访问这个地址，改用服务所在电脑的局域网 IP
  const [ips, setIps] = useState<string[] | null>(isLoopback ? null : [hostname]);
  const [ip, setIp] = useState(isLoopback ? "" : hostname);
  const [error, setError] = useState("");
  const [img, setImg] = useState("");

  useEffect(() => {
    if (!isLoopback) return;
    api
      .netInfo()
      .then((info) => {
        setIps(info.ips);
        setIp(info.ips[0] ?? "");
      })
      .catch((e) => setError(errMsg(e)));
  }, [isLoopback]);

  const url = ip ? `${protocol}//${ip.includes(":") ? `[${ip}]` : ip}${port ? `:${port}` : ""}${pathname}${search}` : "";

  useEffect(() => {
    if (!url) return;
    QRCode.toDataURL(url, { width: 260, margin: 1 }).then(setImg, (e) => setError(errMsg(e)));
  }, [url]);

  return (
    <Modal title="分享二维码" onClose={onClose}>
      <div className="modal-body qr-body">
        {error && <p className="error">{error}</p>}
        {ips === null && !error && <p className="muted">加载中…</p>}
        {ips && ips.length === 0 && <p className="error">未检测到局域网地址，请确认电脑已连接网络。</p>}
        {ips && ips.length > 1 && (
          <label>
            局域网地址
            <select value={ip} onChange={(e) => setIp(e.target.value)}>
              {ips.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
        )}
        {url && img && (
          <>
            <img className="qr-img" src={img} alt="页面地址二维码" />
            <a className="qr-url" href={url}>
              {url}
            </a>
            <p className="muted">同一局域网内的设备扫码即可打开，登录后浏览文件。</p>
          </>
        )}
      </div>
    </Modal>
  );
}
