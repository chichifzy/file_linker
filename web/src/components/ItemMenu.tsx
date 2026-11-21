import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

const WIDTH = 148;
const ITEM = 34; // 每项高度，只用来判断要不要往上翻
const SEP = 9; // 分组细线 + 上下留白
const PADDING = 12;
const GAP = 6;
const EDGE = 8;

/**
 * 文件行尾「⋯」按钮弹出的菜单。挂到 body 上并用 fixed 定位：文件列表有
 * overflow: hidden（为了圆角），放在里面会被裁掉。
 */
export default function ItemMenu({
  anchor,
  onClose,
  onCopy,
  onCut,
  onRename,
  onExtract,
  onDelete,
  trashName,
}: {
  anchor: HTMLElement;
  onClose: () => void;
  /** 记住这一项，等会儿到目标文件夹粘贴（复制 / 移动） */
  onCopy: () => void;
  onCut: () => void;
  onRename: () => void;
  /** 压缩包才有：解压到当前文件夹 */
  onExtract?: () => void;
  onDelete: () => void;
  /** 删除的去处：「回收站」或「废纸篓」 */
  trashName: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  // 分三组：拷贝/剪切 · 重命名/解压 · 删除
  const items = 2 + 1 + (onExtract ? 1 : 0) + 1;
  const height = PADDING + ITEM * items + SEP * 2;

  useLayoutEffect(() => {
    const r = anchor.getBoundingClientRect();
    const left = Math.min(window.innerWidth - WIDTH - EDGE, Math.max(EDGE, r.right - WIDTH));
    const below = r.bottom + GAP;
    // 下方放不下就翻到按钮上方
    const top = below + height > window.innerHeight - EDGE ? Math.max(EDGE, r.top - height - GAP) : below;
    setPos({ left, top });
  }, [anchor, height]);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor.contains(t)) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const close = () => onClose();
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    // 列表滚动或窗口变化后按钮位置就变了，直接收起，免得菜单飘在错的地方
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [anchor, onClose]);

  return createPortal(
    <div
      ref={ref}
      className="row-menu"
      role="menu"
      style={{
        left: pos?.left ?? -9999,
        top: pos?.top ?? -9999,
        visibility: pos ? "visible" : "hidden",
      }}
    >
      <button type="button" role="menuitem" onClick={onCopy}>
        拷贝
      </button>
      <button type="button" role="menuitem" onClick={onCut}>
        剪切
      </button>
      <div className="menu-sep" role="separator" />
      <button type="button" role="menuitem" onClick={onRename}>
        重命名
      </button>
      {onExtract && (
        <button type="button" role="menuitem" onClick={onExtract}>
          解压
        </button>
      )}
      <div className="menu-sep" role="separator" />
      <button type="button" role="menuitem" className="danger-text" onClick={onDelete}>
        {/* 跟系统自己的叫法一致：macOS 的右键菜单就是「移到废纸篓」 */}
        {trashName === "废纸篓" ? "移到废纸篓" : "删除"}
      </button>
    </div>,
    document.body,
  );
}
