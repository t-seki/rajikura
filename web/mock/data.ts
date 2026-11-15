// README のスクリーンショット用の、架空のダミーのデータ（docs/development.md の「README のスクリーンショットを撮り直す」）。
// 局・番組名・出演者・説明・画像のすべてを作り話にする。radiko の実際の番組表は使わない（ADR 0028）。
// 型だけを src から読む（import type は消えるので、このファイルは src の実行コードに依存しない）。
import type { GuideAiring, GuideStation } from "../src/guide/api.ts";
import type { JobAttempt, JobDetail, JobSummary } from "../src/jobs/jobsApi.ts";
import type { Keyword, KeywordAiring } from "../src/keywords/keywordsApi.ts";
import type { Show, Station } from "../src/shows/api.ts";
import type { ApiToken } from "../src/tokens/tokensApi.ts";

/** スクリーンショットの「今」。撮るスクリプトがブラウザの時計をこの時刻に固定する（2026-04-15 水曜 10:30 JST）。 */
export const MOCK_NOW = "2026-04-15T10:30:00+09:00";

const nowMs = new Date(MOCK_NOW).getTime();

// 3 局とも自分のエリア（東京都。GET /api/guide/areas の own）の局（GET /api/stations の areaName・outOfArea）。
export const stations: Station[] = [
  { id: "HOSHIZORA", name: "ほしぞらFM", areaName: "東京都", outOfArea: false },
  { id: "MINATO", name: "みなとラジオ", areaName: "東京都", outOfArea: false },
  { id: "TSUMUGI", name: "つむぎ放送", areaName: "東京都", outOfArea: false },
];

type Slot = { minutes: number; title: string; performer: string; description: string };

// 各局の 1 日（放送日の 05:00 から 29:00 まで）の並び。minutes の合計は 1440。
const lineups: Record<string, Slot[]> = {
  HOSHIZORA: [
    { minutes: 120, title: "朝のどんぐりカフェ", performer: "青海 ひなた", description: "森の朝のようすと、リスナーからのお便りをお届けします。" },
    { minutes: 120, title: "ほしぞらモーニング", performer: "霧島 かなえ", description: "通勤のおともに、軽い話題と音楽を。" },
    { minutes: 120, title: "月曜の小さな図書館", performer: "白波 しずく", description: "架空の本を一冊ずつ取り上げて、あらすじを朗読します。" },
    { minutes: 120, title: "お昼のおしゃべり便", performer: "桜庭 みのり", description: "お昼どきのゆるいおしゃべりと、季節の話題。" },
    { minutes: 180, title: "午後のひだまり音楽堂", performer: "神楽坂 ゆうと", description: "午後にぴったりの、やさしい曲を選んでかけます。" },
    { minutes: 180, title: "夕やけトラベラーズ", performer: "波多野 こはる", description: "架空の町を旅する気分で、その土地の話を聞きます。" },
    { minutes: 120, title: "ほしぞらニュースダイジェスト", performer: "柊 りょう", description: "一日の出来事を短くまとめます。" },
    { minutes: 180, title: "星降る深夜ラジオ", performer: "月島 そら", description: "眠れない夜のためのトークと音楽。" },
    { minutes: 120, title: "夜ふかしクラシック", performer: "藤代 あおい", description: "夜にゆっくり聴きたい、架空の楽団の演奏を紹介します。" },
    { minutes: 180, title: "未明のひとりごと", performer: "朝霧 ねね", description: "明け方まで、静かな声でお届けします。" },
  ],
  MINATO: [
    { minutes: 180, title: "みなと朝いちばん", performer: "潮見 たかし", description: "港町の朝の天気と、漁の話題。" },
    { minutes: 120, title: "ハーバーサウンズ", performer: "浜田 みさき", description: "海にまつわる曲を集めたプレイリスト。" },
    { minutes: 120, title: "かもめ通信", performer: "岬 さつき", description: "港のお知らせと、地元のお店の紹介。" },
    { minutes: 120, title: "灯台のおやつ時間", performer: "凪野 ひろ", description: "おやつをお供に、ゆるく雑談します。" },
    { minutes: 180, title: "波打ちぎわの昭和歌謡", performer: "磯部 ことね", description: "架空の歌手たちの名曲を振り返ります。" },
    { minutes: 120, title: "船乗りの夜話", performer: "沖田 けんじ", description: "船乗りに伝わる不思議な話を語ります。" },
    { minutes: 180, title: "夜のハーバーライト", performer: "汐入 あかり", description: "夜の港を思いながら聴く、しっとりした選曲。" },
    { minutes: 120, title: "夜明け前のレコード棚", performer: "浦田 るい", description: "架空のレコードを一枚ずつ紹介します。" },
    { minutes: 300, title: "みなと深夜便", performer: "渚 はると", description: "夜通しの音楽と短い話。" },
  ],
  TSUMUGI: [
    { minutes: 120, title: "つむぎの朝ごはん", performer: "糸井 ほのか", description: "朝ごはんの話題と、手芸のちょっとしたコツ。" },
    { minutes: 180, title: "ゆうびん屋さんのたより", performer: "宛名 まさと", description: "全国から届いた架空のお便りを読みます。" },
    { minutes: 120, title: "つむぎ昼どき劇場", performer: "紡木 かえで", description: "短いラジオドラマを毎日お届けします。" },
    { minutes: 120, title: "手のひらの科学", performer: "理科室 はじめ", description: "身近な不思議を、やさしく解説します。" },
    { minutes: 180, title: "放課後ミュージックルーム", performer: "音羽 りんご", description: "放課後の教室のような雰囲気で、リクエスト曲を。" },
    { minutes: 120, title: "夕暮れの四コマ劇場", performer: "四宮 こうた", description: "四コマ漫画を声で楽しむ、短い番組。" },
    { minutes: 180, title: "つむぎ夜の読み物", performer: "文月 ゆず", description: "夜に読みたい短編を朗読します。" },
    { minutes: 180, title: "眠れぬ夜のオルゴール", performer: "鈴森 ちとせ", description: "オルゴールの音色だけでお届けします。" },
    { minutes: 120, title: "つむぎ未明の音楽会", performer: "調布 らん", description: "明け方まで続く、静かな音楽会。" },
    { minutes: 120, title: "ねむの木ラジオ", performer: "眠田 みどり", description: "朝が来るまでの、小さな番組。" },
  ],
};

/**
 * 放送日 day（YYYY-MM-DD）の、3 局の番組表。
 * ダミーのジョブ（jobs）がある放送回（局と開始・終了が、ジョブの stations と同じ）は、本物（internal/server/guide.go の view）と同じ決め方で、
 * showName・jobId・state・skipBy・skippable をそのジョブから付ける。
 * done→done、expired→missed、skipped→skip_planned（settled なら skipped。skipBy は byPerson なら person、でなければ title）、
 * それ以外→planned（skippable は、試行が 0 回の waiting のときだけ true）。
 * ジョブが無い放送回は、有効な番組の毎週の予約（weekly の局・放送日の曜日・開始時刻）に当たるときだけ、その番組の放送回にする
 * （当たらなければ予約の無い放送回 none）。本物の internal/server/guide.go と同じと言えるのは、この毎週の予約の当て方と、ジョブのある放送回の決め方だけ。
 * 当たったものの状態は、ダミーだけの簡略化: 今（MOCK_NOW）より前に終わっていれば録音済み（過去の放送日のジョブの無い放送回も録音済みにする）、後なら録音予定（jobId は付けない）。
 * 本物はこの状態の決め方をしない。
 */
export function guideOf(day: string): GuideStation[] {
  const base = new Date(`${day}T05:00:00+09:00`).getTime();
  const weekday = new Date(`${day}T00:00:00Z`).getUTCDay();
  return stations.map((st) => {
    let cursor = 0;
    const airings: GuideAiring[] = lineups[st.id].map((slot, i) => {
      const start = new Date(base + cursor * 60_000);
      const end = new Date(base + (cursor + slot.minutes) * 60_000);
      cursor += slot.minutes;
      const job = jobs.find((j) => j.stations.some((s) => s.stationId === st.id && Date.parse(s.start) === start.getTime() && Date.parse(s.end) === end.getTime()));
      const showName = job ? job.showName : showOfWeekly(st.id, weekday, hhmmJst(start));
      let state: GuideAiring["state"] = showName ? (end.getTime() <= nowMs ? "done" : "planned") : "none";
      let jobFields: Pick<GuideAiring, "jobId" | "skipBy" | "skippable"> = {};
      if (job) {
        switch (job.state) {
          case "done":
            state = "done";
            break;
          case "expired":
            state = "missed";
            break;
          case "skipped":
            state = job.settled ? "skipped" : "skip_planned";
            jobFields = { skipBy: job.byPerson ? "person" : "title" };
            break;
          default:
            state = "planned";
            if (job.state === "waiting" && job.attempts === 0) jobFields = { skippable: true };
        }
        jobFields = { jobId: job.id, ...jobFields };
      }
      return {
        stationId: st.id,
        stationName: st.name,
        start: start.toISOString(),
        end: end.toISOString(),
        title: slot.title,
        performer: slot.performer,
        description: slot.description,
        descriptionParts: [{ text: slot.description }],
        imageUrl: `/mock-images/${(i % 3) + 1}.svg`,
        state,
        ...(showName ? { showName } : {}),
        ...jobFields,
        notAvailable: false,
        timeFree30Excluded: false,
      };
    });
    return { id: st.id, name: st.name, airings };
  });
}

const hhmmJst = (d: Date) => new Date(d.getTime() + 9 * 3600_000).toISOString().slice(11, 16);

/** 有効な番組の毎週の予約が、局・放送日の曜日（0=日）・開始時刻（JST の HH:MM）に当たる番組の名前。 */
const showOfWeekly = (stationId: string, weekday: number, hhmm: string) =>
  showDefs.find((s) => s.enabled && s.weekly.some((w) => w.station === stationId && w.weekday === weekday && w.start === hhmm))?.name;

// 放送日 day（YYYY-MM-DD）の放送日の時刻 hhmm（HH:MM。24:00 以降も書ける）の時刻。番組表の並び（lineups）の時刻と合わせて書く。
const jst = (day: string, hhmm: string) => {
  const [h, m] = hhmm.split(":").map(Number);
  return new Date(new Date(`${day}T00:00:00+09:00`).getTime() + (h * 60 + m) * 60_000).toISOString();
};

// 番組（shows）の weekly、ジョブ（jobs）の stations は、番組表の並び（lineups）の放送回と同じ局・開始時刻・長さにする。
// next_airing（jobs の後で付ける。nextAiringOf）の決め方は、その直前のコメントに書く。
const showDefs: Show[] = [
  {
    name: "朝のどんぐりカフェ",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 1, start: "05:00" }, { station: "HOSHIZORA", weekday: 3, start: "05:00" }],
  },
  {
    name: "月曜の小さな図書館",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 1, start: "09:00" }],
  },
  {
    name: "夕やけトラベラーズ",
    enabled: true,
    split: "join",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 3, start: "16:00" }, { station: "HOSHIZORA", weekday: 6, start: "16:00" }],
  },
  {
    name: "星降る深夜ラジオ",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 3, start: "21:00" }],
  },
  {
    name: "ハーバーサウンズ",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "MINATO", weekday: 3, start: "08:00" }],
  },
  {
    name: "波打ちぎわの昭和歌謡",
    enabled: true,
    split: "separate",
    check_title: false,
    weekly: [{ station: "MINATO", weekday: 4, start: "14:00" }],
  },
  {
    name: "つむぎ夜の読み物",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "TSUMUGI", weekday: 2, start: "19:00" }, { station: "TSUMUGI", weekday: 5, start: "19:00" }],
  },
  {
    name: "ゆうびん屋さんのたより",
    enabled: false,
    split: "none",
    check_title: true,
    weekly: [{ station: "TSUMUGI", weekday: 0, start: "07:00" }],
  },
];

const baseJob = {
  attempts: 0,
  nextAttemptAt: "",
  deadline: "",
  retryable: false,
  settled: false,
  byPerson: false,
  live: false,
  skippable: false,
  recordable: false,
  rerecordable: false,
  outputPath: "",
  missingCount: 0,
  lastFailedStage: "",
  lastError: "",
} satisfies Partial<JobSummary>;


const slotOf = (stationId: string, day: string, from: string, minutes: number) => {
  const start = jst(day, from);
  return { stationId, start, end: new Date(new Date(start).getTime() + minutes * 60_000).toISOString() };
};

export const jobs: JobSummary[] = [
  {
    ...baseJob,
    id: 108,
    showName: "朝のどんぐりカフェ",
    day: "2026-04-15",
    part: 1,
    // 済んだジョブだけ付く（docs/api.md の finishedAt）。「これから」のジョブには付けない。
    finishedAt: jst("2026-04-15", "07:20"),
    deadline: jst("2026-04-22", "29:00"),
    state: "done",
    attempts: 1,
    chapters: "added",
    rerecordable: true,
    outputPath: "朝のどんぐりカフェ/2026-04-15 朝のどんぐりカフェ.m4a",
    stations: [slotOf("HOSHIZORA", "2026-04-15", "05:00", 120)],
  },
  {
    ...baseJob,
    id: 107,
    showName: "ハーバーサウンズ",
    day: "2026-04-15",
    part: 1,
    deadline: jst("2026-04-22", "29:00"),
    state: "running",
    attempts: 1,
    // 放送は 10:00 に終わり、最初の試行は 5 分後の 10:05 に始まった（今の 10:30 まで 25 分）。
    // 放送の長さ 2 時間（7200 秒）のうち、25 分（1500 秒）で 1 時間分（3600 秒）を取れた（実時間の 2.4 倍の速さ。残りもおよそ 25 分で終わる）。
    progress: { stage: "fetch", fetchedSeconds: 3600, totalSeconds: 7200 },
    stations: [slotOf("MINATO", "2026-04-15", "08:00", 120)],
  },
  {
    ...baseJob,
    id: 109,
    showName: "夕やけトラベラーズ",
    day: "2026-04-15",
    part: 1,
    state: "waiting",
    // タイムフリーの最初の試行は、放送の終わりのおよそ 5 分後
    nextAttemptAt: jst("2026-04-15", "19:05"),
    deadline: jst("2026-04-22", "29:00"),
    skippable: true,
    stations: [slotOf("HOSHIZORA", "2026-04-15", "16:00", 180)],
  },
  {
    ...baseJob,
    id: 110,
    showName: "星降る深夜ラジオ",
    day: "2026-04-15",
    part: 1,
    state: "waiting",
    // ライブ録音の最初の試行は放送の開始の 1 分前（LiveLead）、録音期限は放送の終わりの 5 分後（LiveDeadlineAfter）。internal/domain/schedule.go の JobFirstAttemptAt、internal/domain/deadline.go の JobDeadline
    nextAttemptAt: jst("2026-04-15", "20:59"),
    deadline: jst("2026-04-15", "24:05"),
    live: true,
    skippable: true,
    stations: [slotOf("HOSHIZORA", "2026-04-15", "21:00", 180)],
  },
  {
    ...baseJob,
    id: 106,
    showName: "つむぎ夜の読み物",
    day: "2026-04-14",
    part: 1,
    state: "retrying",
    attempts: 5,
    retryable: true,
    // 最後の試行（4/15 08:20）の 6 時間後（本物の再試行の間隔は 15 分 → 1 時間 → 3 時間 → 以降 6 時間）
    nextAttemptAt: jst("2026-04-15", "14:20"),
    deadline: jst("2026-04-21", "29:00"),
    lastFailedStage: "fetch",
    lastError: "音声の取得に失敗した（ダミーのエラー）",
    stations: [slotOf("TSUMUGI", "2026-04-14", "19:00", 180)],
  },
  {
    ...baseJob,
    id: 105,
    showName: "波打ちぎわの昭和歌謡",
    day: "2026-04-09",
    part: 1,
    finishedAt: jst("2026-04-09", "17:20"),
    deadline: jst("2026-04-16", "29:00"),
    state: "done",
    attempts: 1,
    chapters: "not_added",
    rerecordable: true,
    outputPath: "波打ちぎわの昭和歌謡/2026-04-09 波打ちぎわの昭和歌謡.m4a",
    stations: [slotOf("MINATO", "2026-04-09", "14:00", 180)],
  },
  {
    ...baseJob,
    id: 104,
    showName: "月曜の小さな図書館",
    day: "2026-04-13",
    part: 1,
    finishedAt: jst("2026-04-13", "11:20"),
    deadline: jst("2026-04-20", "29:00"),
    state: "done",
    attempts: 1,
    chapters: "added",
    rerecordable: true,
    outputPath: "月曜の小さな図書館/2026-04-13 月曜の小さな図書館.m4a",
    stations: [slotOf("HOSHIZORA", "2026-04-13", "09:00", 120)],
  },
  {
    ...baseJob,
    id: 103,
    showName: "つむぎ夜の読み物",
    day: "2026-04-10",
    part: 1,
    // 試行のある expired は、最後の試行の終わり（attemptLogs の 103 の最後）。
    // 非会員の試行の上限（10 回。DefaultMaxAttemptsFree）に達して次の試行が無くなり、録音期限（放送日の終わり + 7 日）の前に期限切れになった形
    finishedAt: jst("2026-04-12", "14:24"),
    deadline: jst("2026-04-17", "29:00"),
    state: "expired",
    attempts: 10,
    // 録音期限の前なので、手動再試行を受け付ける（ManualRetryInTime）
    retryable: true,
    lastFailedStage: "fetch",
    lastError: "音声の取得に失敗した（ダミーのエラー）",
    stations: [slotOf("TSUMUGI", "2026-04-10", "19:00", 180)],
  },
  {
    ...baseJob,
    id: 111,
    showName: "波打ちぎわの昭和歌謡",
    day: "2026-04-16",
    part: 1,
    deadline: jst("2026-04-23", "29:00"),
    state: "skipped",
    byPerson: true,
    recordable: true,
    stations: [slotOf("MINATO", "2026-04-16", "14:00", 180)],
  },
];

// 番組の next_airing。本物（internal/store/shows.go の NextAirings）は、その番組の録音予定（waiting）のジョブの放送回のうち、開始が今（MOCK_NOW）より後のいちばん早い開始にする。
// 本物のサーバーは、今日から 7 日先までの予約に当たる放送回に全部ジョブを作る。ダミーのジョブは画面を見せるための一部しか無いので、それを補う:
// 今（MOCK_NOW）の放送日から 7 日先までの放送日の、有効な毎週の予約に当たる放送回のうち、開始が今より後で、
// ジョブがあればそのジョブが waiting のもの、ジョブが無ければ（本物では録音予定のジョブがあるはずなので）録音予定とみなすもののいちばん早い開始。無ければ付けない。
const nextAiringOf = (name: string): string | undefined => {
  // 今の放送日（JST の 05:00 で日を切る。本物の domain.BroadcastDayOf と同じ）
  const broadcastDay = new Date(nowMs + (9 - 5) * 3_600_000).toISOString().slice(0, 10);
  const today = new Date(`${broadcastDay}T00:00:00Z`).getTime();
  let best: string | undefined;
  for (let d = 0; d <= 7; d++) {
    const day = new Date(today + d * 86_400_000).toISOString().slice(0, 10);
    for (const st of guideOf(day)) {
      for (const a of st.airings) {
        if (a.showName !== name || Date.parse(a.start) <= nowMs) continue;
        const job = a.jobId === undefined ? undefined : jobs.find((j) => j.id === a.jobId);
        if (job && job.state !== "waiting") continue;
        if (best === undefined || Date.parse(a.start) < Date.parse(best)) best = a.start;
      }
    }
  }
  return best;
};

export const shows: Show[] = showDefs.map((s) => {
  const next = s.enabled ? nextAiringOf(s.name) : undefined;
  return next ? { ...s, next_airing: next } : s;
});

// durationSeconds は本物と同じく、録れた音声の長さ（失敗した試行は 0）。
const attempt = (startedAt: string, endedAt: string, failure?: { stage: string; error: string }, recordedSeconds = 0): JobAttempt => ({
  startedAt,
  endedAt,
  failedStage: failure?.stage ?? "",
  error: failure?.error ?? "",
  durationSeconds: failure ? 0 : recordedSeconds,
});

const failureOf = (id: number) => {
  const j = jobs.find((x) => x.id === id);
  if (!j) throw new Error(`ダミーのジョブ ${id} が無い`);
  return { stage: j.lastFailedStage, error: j.lastError };
};

const fetchFailed = { stage: "fetch", error: "音声の取得に失敗した（ダミーのエラー）" };

/**
 * ジョブ id ごとの試行の履歴（古い順）。件数は一覧の attempts と同じにし、失敗したジョブの最後の試行は lastFailedStage・lastError と同じにする。
 * 時刻は本物の決まりに合わせる。最初の試行は放送の終わりのおよそ 5 分後、再試行は前の試行の始まりから 15 分 → 1 時間 → 3 時間 → 以降 6 時間。
 * 済んだジョブの最後の試行は、finishedAt に終わる。
 * 決まりの元は internal/domain/schedule.go（FirstAttemptAt・JobFirstAttemptAt・LiveLead・retryIntervals）。
 * 録音期限（deadline）の決まりの元は internal/domain/deadline.go（AiringDeadline・JobDeadline）。非会員は放送日の終わり（翌 05:00）+ 7 日、ライブ録音は放送の終わりの 5 分後。
 * 本物のサーバーは試行が終わったときにだけ履歴を書くので、録音中のジョブ（107）の履歴は空にする（終わっていない試行は入れない）。
 */
const attemptLogs: Record<number, JobAttempt[]> = {
  108: [attempt(jst("2026-04-15", "07:05"), jst("2026-04-15", "07:20"), undefined, 2 * 3600)],
  106: [
    attempt(jst("2026-04-14", "22:05"), jst("2026-04-14", "22:09"), fetchFailed),
    attempt(jst("2026-04-14", "22:20"), jst("2026-04-14", "22:24"), fetchFailed),
    attempt(jst("2026-04-14", "23:20"), jst("2026-04-14", "23:24"), fetchFailed),
    attempt(jst("2026-04-15", "02:20"), jst("2026-04-15", "02:24"), fetchFailed),
    attempt(jst("2026-04-15", "08:20"), jst("2026-04-15", "08:25"), failureOf(106)),
  ],
  105: [attempt(jst("2026-04-09", "17:05"), jst("2026-04-09", "17:20"), undefined, 3 * 3600)],
  104: [attempt(jst("2026-04-13", "11:05"), jst("2026-04-13", "11:20"), undefined, 2 * 3600)],
  103: [
    attempt(jst("2026-04-10", "22:05"), jst("2026-04-10", "22:09"), fetchFailed),
    attempt(jst("2026-04-10", "22:20"), jst("2026-04-10", "22:24"), fetchFailed),
    attempt(jst("2026-04-10", "23:20"), jst("2026-04-10", "23:24"), fetchFailed),
    attempt(jst("2026-04-11", "02:20"), jst("2026-04-11", "02:24"), fetchFailed),
    attempt(jst("2026-04-11", "08:20"), jst("2026-04-11", "08:24"), fetchFailed),
    attempt(jst("2026-04-11", "14:20"), jst("2026-04-11", "14:24"), fetchFailed),
    attempt(jst("2026-04-11", "20:20"), jst("2026-04-11", "20:24"), fetchFailed),
    attempt(jst("2026-04-12", "02:20"), jst("2026-04-12", "02:24"), fetchFailed),
    attempt(jst("2026-04-12", "08:20"), jst("2026-04-12", "08:24"), fetchFailed),
    // 10 回目（非会員の上限）。次の試行が無いので、ここで期限切れになる
    attempt(jst("2026-04-12", "14:20"), jst("2026-04-12", "14:24"), failureOf(103)),
  ],
};

export const jobDetail = (j: JobSummary): JobDetail => ({
  ...j,
  airings: j.stations.map((s) => ({
    stationId: s.stationId,
    start: s.start,
    end: s.end,
    title: j.showName,
    performer: "",
    description: "",
    missing: false,
  })),
  attemptLog: attemptLogs[j.id] ?? [],
  durationSeconds: j.stations.reduce((n, s) => n + (new Date(s.end).getTime() - new Date(s.start).getTime()) / 1000, 0),
});

export const keywords: Keyword[] = [
  { id: 1, words: "どんぐり", exclude: "", stations: ["HOSHIZORA"], enabled: true, pending: 1 },
  { id: 2, words: "昭和歌謡", exclude: "再放送", stations: ["MINATO", "TSUMUGI"], enabled: true, pending: 0 },
  { id: 3, words: "朗読", exclude: "", stations: [], enabled: false, pending: 0 },
];

/** キーワード id ごとの、足した放送回（GET /api/keywords/{id}/airings）。開始の新しい順。番組表の並び（lineups）と同じ局・時刻・題名にする。 */
export const keywordAirings: Record<number, KeywordAiring[]> = {
  1: [
    { ...slotOf("HOSHIZORA", "2026-04-20", "05:00", 120), stationName: "ほしぞらFM", title: "朝のどんぐりカフェ", showName: "朝のどんぐりカフェ", state: "planned" },
    { ...slotOf("HOSHIZORA", "2026-04-15", "05:00", 120), stationName: "ほしぞらFM", title: "朝のどんぐりカフェ", showName: "朝のどんぐりカフェ", state: "done" },
  ],
  2: [
    { ...slotOf("MINATO", "2026-04-16", "14:00", 180), stationName: "みなとラジオ", title: "波打ちぎわの昭和歌謡", showName: "波打ちぎわの昭和歌謡", state: "skip_planned" },
    { ...slotOf("MINATO", "2026-04-09", "14:00", 180), stationName: "みなとラジオ", title: "波打ちぎわの昭和歌謡", showName: "波打ちぎわの昭和歌謡", state: "done" },
  ],
  3: [],
};

export const tokens: ApiToken[] = [
  { id: 1, name: "ダミーのスクリプト", createdAt: jst("2026-04-01", "10:00"), lastUsedAt: jst("2026-04-14", "08:30") },
  { id: 2, name: "ダミーのスマホ", createdAt: jst("2026-04-10", "21:15"), lastUsedAt: null },
];

/** 画像の代わりに返す、単色の SVG（番組の画像のダミー）。 */
export const mockImage = (n: string) => {
  const colors: Record<string, string> = { "1": "#7aa7d9", "2": "#e0a96d", "3": "#8fc5a0" };
  const c = colors[n] ?? "#bbbbbb";
  return `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200" viewBox="0 0 200 200"><rect width="200" height="200" fill="${c}"/><circle cx="100" cy="100" r="46" fill="#ffffff" fill-opacity="0.6"/></svg>`;
};
