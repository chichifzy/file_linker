import type { Entry } from "../api";

// 可爱风文件图标：圆角外形 + 柔和配色

function FolderIcon() {
  return (
    <svg viewBox="0 0 36 36" width="34" height="34" aria-hidden>
      <path d="M4 10a4 4 0 0 1 4-4h6.5l3 3H28a4 4 0 0 1 4 4v1H4z" fill="#f5b942" />
      <rect x="3" y="12" width="30" height="19" rx="5" fill="#ffd36e" />
    </svg>
  );
}

function DocIcon({ body, fold, lines }: { body: string; fold: string; lines?: string }) {
  return (
    <svg viewBox="0 0 36 36" width="34" height="34" aria-hidden>
      <path d="M9 3h12l8 8v18a4 4 0 0 1-4 4H9a4 4 0 0 1-4-4V7a4 4 0 0 1 4-4z" fill={body} />
      <path d="M21 3l8 8h-5a3 3 0 0 1-3-3z" fill={fold} />
      {lines && (
        <g stroke={lines} strokeWidth="1.6" strokeLinecap="round">
          <path d="M10 10h7" />
          <path d="M10 14h5" />
        </g>
      )}
    </svg>
  );
}

function VideoIcon() {
  return (
    <svg viewBox="0 0 36 36" width="34" height="34" aria-hidden>
      <rect x="3" y="7" width="30" height="22" rx="6" fill="#b9a6ff" />
      <path d="M15 13.5v9l7.5-4.5z" fill="#fff" strokeLinejoin="round" stroke="#fff" strokeWidth="1.5" />
      <circle cx="8" cy="11" r="1.2" fill="#fff" opacity="0.8" />
    </svg>
  );
}

function BoxIcon() {
  return (
    <svg viewBox="0 0 36 36" width="34" height="34" aria-hidden>
      <path d="M5 12l13-6 13 6v13a3 3 0 0 1-1.8 2.7L18 33 6.8 27.7A3 3 0 0 1 5 25z" fill="#e8c9a0" />
      <path d="M5 12l13 6 13-6-13-6z" fill="#f3dbb8" />
      <path d="M18 18v15" stroke="#d4ae7e" strokeWidth="1.2" />
    </svg>
  );
}

export function KindIcon({ kind }: { kind: Entry["kind"] }) {
  switch (kind) {
    case "dir":
      return <FolderIcon />;
    case "text":
      return <DocIcon body="#a8dcff" fold="#7cc4f5" lines="#5aa9e0" />;
    case "image":
      return <DocIcon body="#ffc2d4" fold="#ff9fbb" />;
    case "video":
      return <VideoIcon />;
    default:
      return <BoxIcon />;
  }
}

/** 视频中间的三角形播放小图标 */
export function PlayBadge() {
  return (
    <svg className="play-badge" viewBox="0 0 20 20" width="18" height="18" aria-hidden>
      <circle cx="10" cy="10" r="10" fill="rgba(0,0,0,0.45)" />
      <path d="M8 6.2v7.6l6-3.8z" fill="#fff" />
    </svg>
  );
}
