// 番組表からの予約の操作（「この回を録音」「毎週録音」「この回の録音をやめる」「録る」「録らない」）と、その取り消し。
// 「録る」（録らない予定のジョブを録音予定に戻す、録らなかったのジョブの手動再試行）と「録らない」（録音予定のジョブを
// 録らない予定にする）は、ジョブの操作なので取り消せない（ADR 0019。逆の操作を押し直す）。
// 言葉は画面とコードで同じ（番組、毎週・1 回の予約。GLOSSARY.md）。
//
// 取り消しで戻すのは、その操作の応答で作られたと分かっているもの（作った番組の名前、足した 1 回の予約の ID、
// 足した毎週の予約）だけ。
//   - 新しく作った番組は消す。ただし番組を読み直して、作った直後の形（設定・毎週・1 回の予約の ID の集合）と
//     違えば（その間に別に変えられていれば）、消さずに知らせる
//   - 既存の番組に足した 1 回の予約は、足した ID のうち、読み直した番組にまだ残っているものだけを消す
//     （途中で失敗しても、押し直せば残りだけを消す）
//   - 足した毎週の予約は、足す前の番組の形に PUT で戻す。その間に番組が別に変えられていたら
//     （足した直後の形と違えば）、戻さずに知らせる
//   - 「パートを 1 つのファイルにつなぐ」で分割番組でない既存の番組の split を "join" に変えたときは、足した分を外した後に
//     split を "none" に戻す（毎週の予約は、足す前の形に戻すので一緒に戻る）。その間に番組が別に変えられていたら、
//     戻さずに知らせる

import {
  createShow,
  deleteOnce,
  deleteShow,
  getShow,
  replaceShow,
  type Once,
  type Weekly,
  type Show,
  type SplitMode,
} from "../shows/api";
import { recordJob, skipJob } from "../jobs/jobsApi";
import { addOnces, OnceConflict, OnceRefused, type GuideAiring } from "./api";
import { broadcastClock, broadcastDayOf, clock, hasTrailingMark, liveExpired, titleKey, trimTrailingMark, weekdayOf } from "./format";

const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** 1 回の予約の API の 409 を、理由ごとの文にする（reason: other_show / already）。時刻はその放送回の放送日の時刻。 */
export function conflictMessage(e: OnceConflict): string {
  const when = e.airing ? `（${broadcastClock(e.airing.start)} ${e.airing.station}）` : "";
  switch (e.reason) {
    case "other_show":
      return `ほかの番組「${e.showName ?? "?"}」がすでにこの放送回を当てている${when}`;
    case "already":
      return `この番組「${e.showName ?? "?"}」ですでに録音予定${when}`;
    default:
      // 理由が付かない 409 は、サーバーの文をそのまま出す
      return e.message;
  }
}

/** 番組の名前の既定。番組表の番組名から末尾の印を落としたもの（domain.StripTrailingMark と同じく NFKC をかける）。 */
export const defaultShowName = (title: string) => trimTrailingMark(title).normalize("NFKC").trim();

/** 共通の頭の名前がこの文字数より短ければ、先頭の放送回の既定の名前にする。 */
const MIN_COMMON_NAME = 4;

/** 共通の頭の末尾から落とす文字（空白・開き括弧・区切りの記号）。 */
const commonTailRe = /[\s(（「『【[［〈《{｛<＜・･/／、，,:：;；\-‐–—~〜～|｜]+$/;

/**
 * 選んだ放送回 targets から決まる番組の名前の既定。1 件なら defaultShowName。2 件以上なら、それぞれの既定の名前
 * （末尾の印を落として NFKC をかけたもの）の共通の頭にし、全部が同じでなければ末尾の空白・開き括弧・区切りの記号を落とす。
 * 共通の頭が 4 文字未満なら、先頭（開始の順）の放送回の既定の名前にする。
 * 例: ふんわりの 4 件 →「ふんわり」、ナイツの 3 件 →「ナイツ ザ・ラジオショー」、`ふらっと (1)` と `(2)` →「ふらっと」。
 */
export function groupName(targets: Pick<GuideAiring, "title" | "start">[]): string {
  const sorted = [...targets].sort((a, b) => a.start.localeCompare(b.start));
  const names = sorted.map((a) => defaultShowName(a.title));
  if (names.length === 0) return "";
  const first = names[0];
  if (names.length === 1) return first;
  let chars = Array.from(first);
  for (const n of names.slice(1)) {
    const cs = Array.from(n);
    let i = 0;
    while (i < chars.length && i < cs.length && chars[i] === cs[i]) i++;
    chars = chars.slice(0, i);
  }
  let prefix = chars.join("");
  if (!names.every((n) => n === prefix)) prefix = prefix.replace(commonTailRe, "");
  return Array.from(prefix).length < MIN_COMMON_NAME ? first : prefix;
}

/**
 * 放送回 airing の自動の組（用語集。番組表の普通のクリックの初めの選択。domain.KeywordParts と同じ規則）。開始の順。
 * 同じ局・同じ放送日で、番組名の末尾の印を落とすと番組名（比べるのは titleKey）が airing と同じになる放送回（タイムフリーの対象外の放送回も含む）を
 * 集め、その中に末尾に印のある放送回が 1 つでもあれば、集めたものを組にする。1 つも無ければ airing だけ（1 日に何度もある
 * 同じ番組名の放送回や、同じ日の再放送を巻き込まないため）。
 * airing のほかのパートのうち、録音期限（放送の終了の 5 分後）を now で過ぎたタイムフリーの対象外の放送回は除く
 * （混ぜると 1 回の予約の API が全体ごと断るため）。予約済みの放送回を除くのは呼ぶ側（番組表の選択）。
 */
export function autoGroup(airing: GuideAiring, dayAirings: GuideAiring[], now: number = Date.now()): GuideAiring[] {
  const key = titleKey(airing.title);
  if (key === "") return [airing];
  const day = broadcastDayOf(airing.start);
  const parts: GuideAiring[] = [];
  for (const a of dayAirings) {
    if (a.stationId !== airing.stationId || broadcastDayOf(a.start) !== day || titleKey(a.title) !== key) continue;
    if (parts.some((p) => p.start === a.start)) continue;
    if (a.start !== airing.start && liveExpired(a, now)) continue;
    parts.push(a);
  }
  if (!parts.some(hasMark) && !hasTrailingMark(airing.title)) return [airing];
  if (!parts.some((a) => a.start === airing.start)) parts.push(airing);
  return parts.sort((a, b) => a.start.localeCompare(b.start));
}

const hasMark = (a: GuideAiring) => hasTrailingMark(a.title);

/** 放送回から作る毎週の予約（局・放送日の曜日・開始時刻。開始日はその放送回の放送日）。 */
export const weeklyOf = (a: GuideAiring): Weekly => ({
  station: a.stationId,
  weekday: weekdayOf(broadcastDayOf(a.start)),
  start: clock(a.start),
  from: broadcastDayOf(a.start),
});

/**
 * 足す先の番組。既存の番組か、新しく作る番組（名前とつなぐ設定）。新しく作る番組の分割番組の設定は、足す放送回が 2 つ以上
 * なら join に従って "join" か "separate"、1 つなら "none"（newSplit）。
 */
export type Dest = { kind: "existing"; show: Show } | { kind: "new"; name: string; join: boolean };

/** 番組の名前を比べる形（NFKC と前後の空白）。サーバーは番組の名前を正規化せずに保存するので、こちらで揃えて比べる。 */
const nameKey = (name: string) => name.normalize("NFKC").trim();

/**
 * 足す先の番組の既定。選んだ放送回 targets から決まる名前（groupName）と同じ名前（NFKC で揃えて比べる。完全に同じものを先に選ぶ）の
 * 番組があればそれ、無ければその名前で新しく作る（つなぐはオン）。名前を変えた番組は探さない。
 */
export function defaultDest(shows: Show[], targets: GuideAiring[]): Dest {
  const name = groupName(targets);
  const show = shows.find((s) => s.name === name) ?? shows.find((s) => nameKey(s.name) === name);
  return show ? { kind: "existing", show } : { kind: "new", name, join: true };
}

/**
 * 足す先の番組 dest が、放送回 targets を足した後に、その放送日に当てる放送回の数（targets と、番組表 dayAirings のうち dest の番組が
 * もう当てている放送回（番組表の放送回の showName で数える）を合わせて、重ねずに数える）。放送日は targets の先頭の放送日。
 * 新しく作る番組は、もう当てている放送回が無い。
 */
export function countOnDay(dest: Dest, targets: GuideAiring[], dayAirings: GuideAiring[]): number {
  if (targets.length === 0) return 0;
  const keys = new Set(targets.map((a) => `${a.stationId}|${a.start}`));
  if (dest.kind === "existing") {
    const day = broadcastDayOf(targets[0].start);
    for (const a of dayAirings) {
      if (a.showName === dest.show.name && broadcastDayOf(a.start) === day) keys.add(`${a.stationId}|${a.start}`);
    }
  }
  return keys.size;
}

/**
 * 「パートを 1 つのファイルにつなぐ」のチェックを出すか。足した後にその放送日に当てる放送回が 2 つ以上（countOnDay）で、足す先の番組が
 * 新しく作る番組か、分割番組でない（split が none の）既存の番組のとき。既存の join・separate の番組は、その設定に従う。
 */
export const offersJoin = (dest: Dest, targets: GuideAiring[], dayAirings: GuideAiring[]) =>
  (dest.kind === "new" || dest.show.split === "none") && countOnDay(dest, targets, dayAirings) >= 2;

/** 操作の結果。message は帯に出す文、showName は「番組を開く」で開く番組、undo は取り消し（取り消せない操作では無い）。 */
export type Outcome = {
  message: string;
  showName: string;
  undo?: () => Promise<{ message: string; showName?: string }>;
};

/** 足す先の番組が無効な番組か。 */
export const disabledDest = (d: Dest) => d.kind === "existing" && !d.show.enabled;

const refOf = (a: GuideAiring) => ({ station: a.stationId, start: a.start });

/** 新しく作る番組の分割番組の設定（ADR 0003）。パートを 2 つ以上まとめて足すなら分割番組（つなぐかは join）、それ以外は分割番組でない。 */
export const newSplit = (join: boolean, parts: number): SplitMode => (parts >= 2 ? (join ? "join" : "separate") : "none");

/** 分割番組の設定を「つなぐ」に変えたことの添え書き。 */
const joinedNote = "（パートを 1 つのファイルにつなぐ設定にしました）";

/**
 * 分割番組でない既存の番組 name の split を "join" に変える（「パートを 1 つのファイルにつなぐ」がオンのとき）。読み直した番組が
 * split "none" でなければ変えずに null を返す。変えたら、変える前と後の形を返す（取り消しで戻すため）。
 */
async function joinExisting(name: string): Promise<{ before: Show; after: Show } | null> {
  const before = shapeOf(await getShow(name));
  if (before.split !== "none") return null;
  const after: Show = { ...before, split: "join" };
  await replaceShow(name, after);
  return { before, after };
}

/** joinExisting で変えた split を戻す。読み直した番組が変えた直後の形でなければ（その間に別に変えられていれば）、戻さずに文を投げる。 */
async function unjoin(name: string, changed: { before: Show; after: Show }) {
  const now = shapeOf(await getShow(name));
  if (!sameShape(now, changed.after)) {
    throw new Error(`番組「${name}」がその後に変わったので、分割番組の設定は戻さなかった。番組の画面で確かめてほしい`);
  }
  await replaceShow(name, changed.before);
}

/**
 * 「この回を録音」。targets（選んだ放送回）を足す先の番組の 1 回の予約にする。失敗したら文を投げる。
 * join は「パートを 1 つのファイルにつなぐ」がオンか。足す先の番組が分割番組でない既存の番組で join なら、足す前に split を "join" に
 * 変える（取り消しで戻す）。
 */
export async function recordOnce(dest: Dest, targets: GuideAiring[], join = false): Promise<Outcome> {
  const refs = targets.map(refOf);
  if (refs.length === 0) throw new Error("選んだ回が無い");
  const what = refs.length >= 2 ? `この回（${refs.length} パート）` : "この回";
  if (dest.kind === "existing") {
    const name = dest.show.name;
    const changed = join && dest.show.split === "none" ? await joinExisting(name) : null;
    let restored = false;
    let added: Once[];
    try {
      added = await addOnces(name, refs);
    } catch (err) {
      let text = err instanceof OnceConflict ? conflictMessage(err) : message(err, "録音の予約ができなかった");
      if (changed) {
        if (err instanceof OnceRefused) {
          // 足していないと分かるので、変えた分割番組の設定も戻す
          try {
            await unjoin(name, changed);
          } catch {
            text += `（番組「${name}」の分割番組の設定を「つなぐ」に変えたまま戻せなかった。番組の画面で確かめてほしい）`;
          }
        } else {
          text += `（番組「${name}」の分割番組の設定は「つなぐ」に変えた）`;
        }
      }
      throw new Error(text);
    }
    return {
      message: `${what}を録音します。番組「${name}」に足しました${changed ? joinedNote : ""}`,
      showName: name,
      undo: async () => {
        // 応答に足した 1 回の予約が無ければ、どれを消せばよいか分からない。推測で消さない
        if (added.length === 0) throw new Error("足した 1 回の予約が分からないので、取り消せない。番組の画面で消してほしい");
        // 読み直して、まだ残っているものだけを消す（前の取り消しが途中で失敗していても、残りだけを消せる）
        const present = new Set((await getShow(name)).onces?.map((o) => o.id));
        let failed = 0;
        for (const o of added) {
          if (!present.has(o.id)) continue;
          try {
            await deleteOnce(name, o.id);
          } catch {
            failed++;
          }
        }
        if (failed > 0) {
          throw new Error(`番組「${name}」から、1 回の予約を ${failed} 件外せなかった。もう一度取り消すか、番組の画面で消してほしい`);
        }
        if (changed && !restored) {
          // 変えた分割番組の設定を「分割番組でない」に戻す（押し直したときは、もう戻した分を戻し直さない）
          await unjoin(name, changed);
          restored = true;
        }
        const note = changed ? "、分割番組の設定を戻しました" : "";
        return { message: `取り消しました（番組「${name}」から${what}の録音を外しました${note}）`, showName: name };
      },
    };
  }

  const name = dest.name.trim();
  if (!name) throw new Error("番組の名前を入れてほしい");
  const made: Show = { name, enabled: true, split: newSplit(dest.join, refs.length), check_title: true, weekly: [] };
  await createShow(made);
  let added: Once[];
  try {
    added = await addOnces(name, refs);
  } catch (err) {
    let text = err instanceof OnceConflict ? conflictMessage(err) : message(err, "録音の予約ができなかった");
    if (err instanceof OnceRefused) {
      // API が応答で断ったので、足していないと分かる。いま作った（番組も毎週の予約も無い）番組は残さない
      try {
        await deleteShow(name);
      } catch {
        text += `（作った番組「${name}」を消せなかった。番組の画面で消してほしい）`;
      }
    } else {
      // 通信の失敗など、足せたか分からない。消さない
      text = `番組「${name}」は作ったが、1 回の予約を足せたか分からない。番組の画面で確かめてほしい（${text}）`;
    }
    throw new Error(text);
  }
  return {
    message: `${what}を録音します。番組「${name}」を新しく作りました`,
    showName: name,
    undo: () => deleteCreated(made, added),
  };
}

/** 番組の形を比べるための一致（開始日と終了日も同じ。空と省略は同じ）。 */
const sameWeekly = (a: Weekly, b: Weekly) =>
  a.station === b.station &&
  a.weekday === b.weekday &&
  a.start === b.start &&
  (a.from ?? "") === (b.from ?? "") &&
  (a.until ?? "") === (b.until ?? "");

/** 既にある毎週の予約 have が、足そうとする weekly の放送回を全部当てるか。局・曜日・開始時刻が同じで、have の開始日が無いか、weekly の開始日以前のとき。weekly の開始日が空なら、have の開始日が無いときだけ当てる（空は「いつからでも」なので、開始日のある have は全部は当てない）。
 * 終了日も同じく、have の終了日が無いか、weekly の終了日以後のときだけ当てる（weekly の終了日が空なら、終了日のある have は全部は当てない）。 */
const covers = (have: Weekly, weekly: Weekly) =>
  have.station === weekly.station &&
  have.weekday === weekly.weekday &&
  have.start === weekly.start &&
  (!have.from || (!!weekly.from && have.from <= weekly.from)) &&
  (!have.until || (!!weekly.until && have.until >= weekly.until));

/** 番組の形（名前・設定・毎週の予約）。1 回の予約と次の放送予定は含めない。 */
const shapeOf = (s: Show): Show => ({
  name: s.name,
  enabled: s.enabled,
  split: s.split,
  check_title: s.check_title,
  weekly: s.weekly,
});

const sameShape = (a: Show, b: Show) =>
  a.name === b.name &&
  a.enabled === b.enabled &&
  a.split === b.split &&
  a.check_title === b.check_title &&
  a.weekly.length === b.weekly.length &&
  a.weekly.every((r, i) => sameWeekly(r, b.weekly[i]));

/**
 * 新しく作った番組 made を消す（取り消し）。読み直した番組が、作った直後の形（設定・毎週の予約）と 1 回の予約の ID の集合
 * onces のままのときだけ消す。違えば（その間に別に変えられていれば）、消さずに文を投げる。
 */
async function deleteCreated(made: Show, onces: Once[]): Promise<{ message: string }> {
  const now = await getShow(made.name);
  const ids = (os: { id: number }[] | undefined) => (os ?? []).map((o) => o.id).sort((a, b) => a - b).join(",");
  if (!sameShape(shapeOf(now), made) || ids(now.onces) !== ids(onces)) {
    throw new Error(`番組「${made.name}」がその後に変わったので、取り消さなかった。番組の画面で確かめてほしい`);
  }
  await deleteShow(made.name);
  return { message: `取り消しました（番組「${made.name}」を消しました）` };
}

/**
 * 「毎週録音」。足す先の番組に、targets（選んだ放送回）の毎週の予約を足す（新しく作るなら、その毎週の
 * 予約を持つ番組を作る）。既存の番組にもうある（全部を当てる）毎週の予約は足さず、全部あれば断る。失敗したら文を投げる。
 * join は「パートを 1 つのファイルにつなぐ」がオンか。足す先の番組が分割番組でない既存の番組で join なら、毎週の予約を足すのと
 * 同じ PUT で split を "join" に変える（取り消しは足す前の形に戻すので、split も戻る）。
 */
export async function recordWeekly(dest: Dest, targets: GuideAiring[], join = false): Promise<Outcome> {
  const weeklies = targets.map(weeklyOf);
  if (weeklies.length === 0) throw new Error("選んだ回が無い");
  const what = weeklies.length >= 2 ? `毎週（${weeklies.length} パート）` : "毎週";
  if (dest.kind === "new") {
    const name = dest.name.trim();
    if (!name) throw new Error("番組の名前を入れてほしい");
    const made: Show = { name, enabled: true, split: newSplit(dest.join, weeklies.length), check_title: true, weekly: weeklies };
    await createShow(made);
    return {
      message: `${what}録音します。番組「${name}」を新しく作りました`,
      showName: name,
      undo: () => deleteCreated(made, []),
    };
  }

  const name = dest.show.name;
  // 足す前の形は、いまの番組を読み直して決める（画面を開いた後に変えられていても消さない）
  const before = shapeOf(await getShow(name));
  const adding = weeklies.filter((w) => !before.weekly.some((r) => covers(r, w)));
  if (adding.length === 0) throw new Error(`番組「${name}」には、同じ「毎週」がもうある`);
  const joined = join && before.split === "none";
  const after: Show = { ...before, split: joined ? "join" : before.split, weekly: [...before.weekly, ...adding] };
  await replaceShow(name, after);
  return {
    message: `${what}録音します。番組「${name}」に足しました${joined ? joinedNote : ""}`,
    showName: name,
    undo: async () => {
      const now = shapeOf(await getShow(name));
      if (!sameShape(now, after)) {
        throw new Error(`番組「${name}」がその後に変わったので、取り消さなかった。番組の画面で確かめてほしい`);
      }
      await replaceShow(name, before);
      const note = joined ? "、分割番組の設定を戻しました" : "";
      return { message: `取り消しました（番組「${name}」から「毎週」を外しました${note}）`, showName: name };
    },
  };
}

/** 1 回の予約 onces から、放送回 a を指すものを探す（局と開始の時刻で比べる）。 */
export const findOnce = (onces: Once[] | undefined, a: GuideAiring) =>
  onces?.find((o) => o.stationId === a.stationId && Date.parse(o.start) === Date.parse(a.start));

/** 「この回の録音をやめる」。番組 showName の 1 回の予約 once を消す。取り消しは同じ放送回を足し直す。 */
export async function stopOnce(showName: string, once: Once, airing: GuideAiring): Promise<Outcome> {
  await deleteOnce(showName, once.id);
  return {
    message: `この回の録音をやめました（番組「${showName}」）`,
    showName,
    undo: async () => {
      try {
        await addOnces(showName, [refOf(airing)]);
      } catch (err) {
        throw new Error(err instanceof OnceConflict ? conflictMessage(err) : message(err, "取り消せなかった"));
      }
      return { message: `取り消しました（この回をまた録音します。番組「${showName}」）`, showName };
    },
  };
}

/**
 * 「録る」。録らない予定・録らなかったのジョブ jobId を録るに戻す（ジョブの画面の「録る」と同じ API）。録らない予定は録音予定に戻し、
 * 録らなかったは手動再試行と同じく再試行にする。録音期限を過ぎた録らなかったは API が断る。
 */
export async function recordSkipped(jobId: number, showName: string, planned: boolean): Promise<Outcome> {
  await recordJob(jobId);
  return planned
    ? { message: `この回を録音します。番組「${showName}」の録らない予定の回を録音予定に戻しました`, showName }
    : { message: `この回を録音します。番組「${showName}」の録らなかった回を再試行にしました`, showName };
}

/** 「録らない」。録音予定のジョブ jobId を録らないにする（ジョブの画面の「録らない」と同じ API）。 */
export async function skipPlanned(jobId: number, showName: string): Promise<Outcome> {
  await skipJob(jobId);
  return { message: `この回は録音しません。番組「${showName}」の録音予定の回を録らないにしました`, showName };
}
