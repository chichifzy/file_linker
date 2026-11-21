import { useSyncExternalStore } from "react";

/** 剪贴板：记住「拷贝 / 剪切」了哪一项，等用户到目标文件夹点「粘贴」 */
export type ClipMode = "copy" | "move";

export type Clip = {
  share: number;
  shareName: string;
  /** 相对共享目录，以 / 分隔 */
  path: string;
  name: string;
  isDir: boolean;
  mode: ClipMode;
};

// 放在模块作用域而不是组件 state 里：切到别的页签再回来，还记得剪的是哪一项；
// 刷新页面自然清空（本来也不该跨会话保留）。
let clip: Clip | null = null;
const listeners = new Set<() => void>();

export function setClip(next: Clip | null) {
  clip = next;
  for (const fn of listeners) fn();
}

const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};

export function useClip(): Clip | null {
  return useSyncExternalStore(
    subscribe,
    () => clip,
    () => null,
  );
}
