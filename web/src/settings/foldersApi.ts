// フォルダの API（internal/server/folders.go の GET /api/folders）を呼ぶ。serve のマシンのフォルダを一覧する。

export interface FolderEntry {
  name: string;
  path: string;
}

export interface FolderListing {
  /** 正規化した今の場所。根の一覧なら空。 */
  path: string;
  /** 親の場所。根（ドライブの根・UNC の共有の根を含む）と根の一覧では空。 */
  parent: string;
  folders: FolderEntry[];
}

export class FoldersApiError extends Error {}

/** path の下のフォルダを読む。path が空なら根の一覧。開けなければ 400 の理由を FoldersApiError で投げる。 */
export async function listFolders(path: string): Promise<FolderListing> {
  const res = await fetch(`/api/folders?path=${encodeURIComponent(path)}`, { credentials: "same-origin" });
  if (!res.ok) {
    let msg = `/api/folders が ${res.status} を返した`;
    try {
      const b = (await res.json()) as { error?: string };
      if (b.error) msg = b.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new FoldersApiError(msg);
  }
  const v = (await res.json()) as Partial<FolderListing>;
  return { path: v.path ?? "", parent: v.parent ?? "", folders: v.folders ?? [] };
}

/**
 * 開けなかった path の、ひとつ上の場所。これ以上さかのぼれない（根・共有の根・共有名の無い \\server の形）ときは null
 * （呼び出し側は根の一覧を開く）。区切りの \ と / の両方を扱う。
 */
export function parentToTry(path: string): string | null {
  let s = path.replace(/[\\/]+$/, "");
  if (/^[A-Za-z]:$/.test(s)) return null; // ドライブの根（C:\）
  if (s === "") return null; // / の根
  const idx = Math.max(s.lastIndexOf("\\"), s.lastIndexOf("/"));
  if (idx < 0) return null;
  s = s.slice(0, idx);
  if (s === "") return "/";
  if (/^[A-Za-z]:$/.test(s)) return `${s}\\`;
  // 共有名の無い \\server の形まで落ちたら、そこは開かずに根の一覧へ（\\nas\radio → \\nas → … と 400 を繰り返さない）。
  if (/^[\\/]{2}[^\\/]*$/.test(s)) return null;
  return s;
}
