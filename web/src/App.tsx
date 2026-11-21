import { useEffect, useRef, useState } from "react";
import { Navigate, NavLink, Route, Routes, useLocation } from "react-router";
import { api, type Me } from "./api";
import { useAuth } from "./auth";
import LoginPage from "./pages/LoginPage";
import BrowserPage from "./pages/BrowserPage";
import CachePage from "./pages/CachePage";
import InvitesPage from "./pages/InvitesPage";
import SharesPage from "./pages/SharesPage";
import TasksPage from "./pages/TasksPage";
import ShareQRModal from "./components/ShareQRModal";
import PasswordModal from "./components/PasswordModal";

type NavItem = { to: string; label: string; element: React.ReactNode };

// 前端只控制展示，真正的权限校验在后端。
function navFor(me: Me): NavItem[] {
  const isAdmin = me.role === "admin";
  const items: NavItem[] = [{ to: "/", label: "文件浏览", element: <BrowserPage /> }];
  // 下载任务也只由管理员发起，普通用户没有这一页
  if (isAdmin) items.push({ to: "/tasks", label: "任务列表", element: <TasksPage /> });
  items.push({ to: "/cache", label: "转码缓存", element: <CachePage /> });
  if (isAdmin) {
    items.push({ to: "/invites", label: "邀请码", element: <InvitesPage /> });
    // 本机，或本机允许在其他设备修改时，管理员才能设置共享目录
    if (me.canEditShares) items.push({ to: "/shares", label: "共享目录", element: <SharesPage isLocal={me.isLocal} /> });
  }
  return items;
}

/** 侧边栏底部的头像和弹出菜单。点击外部或按 Esc 关闭。 */
function UserMenu({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const [open, setOpen] = useState(false);
  const [modal, setModal] = useState<"qr" | "password" | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const openModal = (m: "qr" | "password") => {
    setOpen(false);
    setModal(m);
  };

  return (
    <div className="user-menu" ref={ref}>
      {open && (
        <div className="popover" role="menu">
          <button role="menuitem" onClick={() => openModal("qr")}>
            分享二维码
          </button>
          <button role="menuitem" onClick={() => openModal("password")}>
            修改密码
          </button>
          <button role="menuitem" className="danger-text" onClick={onLogout}>
            退出登录
          </button>
        </div>
      )}
      <button className="user-chip" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="avatar">{Array.from(me.nickname)[0] ?? "?"}</span>
        <span className="user-text">
          <span className="user-name">{me.nickname}</span>
          <span className="user-role">
            {me.role === "admin" ? "管理员" : "普通用户"} · {me.uid}
          </span>
        </span>
      </button>
      {modal === "qr" && <ShareQRModal onClose={() => setModal(null)} />}
      {modal === "password" && <PasswordModal onClose={() => setModal(null)} />}
    </div>
  );
}

export default function App() {
  const { user, loading, logout } = useAuth();
  if (loading) return <p className="loading">加载中…</p>;

  if (!user) {
    return (
      <main className="auth-page">
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="*" element={<Navigate to="/login" replace />} />
        </Routes>
      </main>
    );
  }

  return <Layout user={user} logout={logout} />;
}

function Layout({ user, logout }: { user: Me; logout: () => void }) {
  // 手机端侧边栏变成抽屉，切换页面后自动收起
  const [navOpen, setNavOpen] = useState(false);
  // 正在下载的任务数，显示在「任务列表」旁边，方便关掉弹窗后知道还在下
  const [running, setRunning] = useState(0);
  const { pathname, search } = useLocation();
  useEffect(() => setNavOpen(false), [pathname, search]);

  useEffect(() => {
    if (user.role !== "admin") return;
    let stop = false;
    let timer = 0;
    const tick = async () => {
      let n = 0;
      try {
        const r = await api.downloads();
        n = r.jobs.filter((j) => j.status === "running").length;
        if (!stop) setRunning(n);
      } catch {
        // 角标只是提示，查询失败时保持原样
      }
      // 没有任务时慢一点轮询，避免一直打扰服务
      if (!stop) timer = window.setTimeout(tick, n > 0 ? 3000 : 15000);
    };
    timer = window.setTimeout(tick, 800);
    return () => {
      stop = true;
      window.clearTimeout(timer);
    };
  }, [user.role]);

  const nav = navFor(user);
  return (
    <div className="layout">
      <header className="topbar">
        <button className="menu-btn" aria-label="打开菜单" onClick={() => setNavOpen(true)}>
          ☰
        </button>
        <div className="brand">文件浏览</div>
      </header>
      {navOpen && <div className="drawer-mask" onClick={() => setNavOpen(false)} />}
      <aside className={navOpen ? "sidebar open" : "sidebar"}>
        <div className="brand">文件浏览</div>
        <nav className="side-nav">
          {nav.map((item) => (
            <NavLink key={item.to} to={item.to} end>
              <span>{item.label}</span>
              {item.to === "/tasks" && running > 0 && (
                <span className="badge" title={`${running} 个任务正在下载`}>
                  {running}
                </span>
              )}
            </NavLink>
          ))}
        </nav>
        <UserMenu me={user} onLogout={logout} />
      </aside>
      <main className="content">
        <div className="container">
          <Routes>
            {nav.map((item) => (
              <Route key={item.to} path={item.to} element={item.element} />
            ))}
            <Route path="/login" element={<Navigate to="/" replace />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </main>
    </div>
  );
}
