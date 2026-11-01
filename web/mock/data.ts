// README のスクリーンショット用の、架空のダミーのデータ（docs/development.md の「README のスクリーンショットを撮り直す」）。
// 局・番組名・出演者・説明・画像のすべてを作り話にする。radiko の実際の番組表は使わない（ADR 0028）。
// 型だけを src から読む（import type は消えるので、このファイルは src の実行コードに依存しない）。
import type { GuideAiring, GuideStation } from "../src/guide/api.ts";
import type { JobDetail, JobSummary } from "../src/jobs/jobsApi.ts";
import type { Show, Station } from "../src/shows/api.ts";

/** スクリーンショットの「今」。撮るスクリプトがブラウザの時計をこの時刻に固定する（2026-04-15 水曜 10:30 JST）。 */
export const MOCK_NOW = "2026-04-15T10:30:00+09:00";

const nowMs = new Date(MOCK_NOW).getTime();
const at = (offsetMinutes: number) => new Date(nowMs + offsetMinutes * 60_000).toISOString();

export const stations: Station[] = [
  { id: "HOSHIZORA", name: "ほしぞらFM" },
  { id: "MINATO", name: "みなとラジオ" },
  { id: "TSUMUGI", name: "つむぎ放送" },
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

// 録音の状態を付ける番組（Show の name と同じ）。タイトルが同じ放送回に付く。
const showByTitle: Record<string, string> = {
  朝のどんぐりカフェ: "朝のどんぐりカフェ",
  月曜の小さな図書館: "月曜の小さな図書館",
  夕やけトラベラーズ: "夕やけトラベラーズ",
  星降る深夜ラジオ: "星降る深夜ラジオ",
  波打ちぎわの昭和歌謡: "波打ちぎわの昭和歌謡",
  つむぎ夜の読み物: "つむぎ夜の読み物",
};

/** 放送日 day（YYYY-MM-DD）の、3 局の番組表。今（MOCK_NOW）より前の放送回で録るものは録音済み、後は録音予定。 */
export function guideOf(day: string): GuideStation[] {
  const base = new Date(`${day}T05:00:00+09:00`).getTime();
  return stations.map((st) => {
    let cursor = 0;
    const airings: GuideAiring[] = lineups[st.id].map((slot, i) => {
      const start = new Date(base + cursor * 60_000);
      const end = new Date(base + (cursor + slot.minutes) * 60_000);
      cursor += slot.minutes;
      const showName = showByTitle[slot.title];
      const state: GuideAiring["state"] = showName ? (end.getTime() <= nowMs ? "done" : "planned") : "none";
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
        notAvailable: false,
      };
    });
    return { id: st.id, name: st.name, airings };
  });
}

export const shows: Show[] = [
  {
    name: "朝のどんぐりカフェ",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 1, start: "05:00" }, { station: "HOSHIZORA", weekday: 3, start: "05:00" }],
    next_airing: at(60 * 18 + 30),
  },
  {
    name: "月曜の小さな図書館",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 1, start: "09:00" }],
    next_airing: at(60 * 24 * 5 - 90),
  },
  {
    name: "夕やけトラベラーズ",
    enabled: true,
    split: "join",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 3, start: "16:00" }, { station: "HOSHIZORA", weekday: 6, start: "16:00" }],
    next_airing: at(60 * 5 + 30),
  },
  {
    name: "星降る深夜ラジオ",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "HOSHIZORA", weekday: 3, start: "00:00" }],
    next_airing: at(60 * 13 + 30),
  },
  {
    name: "波打ちぎわの昭和歌謡",
    enabled: true,
    split: "separate",
    check_title: false,
    weekly: [{ station: "MINATO", weekday: 4, start: "14:00" }],
    next_airing: at(60 * 27 + 30),
  },
  {
    name: "つむぎ夜の読み物",
    enabled: true,
    split: "none",
    check_title: true,
    weekly: [{ station: "TSUMUGI", weekday: 2, start: "21:00" }, { station: "TSUMUGI", weekday: 5, start: "21:00" }],
    next_airing: at(60 * 34 + 30),
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

const slotOf = (stationId: string, startOffsetMin: number, minutes: number) => ({
  stationId,
  start: at(startOffsetMin),
  end: at(startOffsetMin + minutes),
});

const dayOf = (offsetMin: number) => new Date(nowMs + offsetMin * 60_000 + 9 * 3600_000 - 5 * 3600_000).toISOString().slice(0, 10);

export const jobs: JobSummary[] = [
  {
    ...baseJob,
    id: 108,
    showName: "朝のどんぐりカフェ",
    day: dayOf(-60 * 5),
    part: 1,
    state: "done",
    attempts: 1,
    chapters: "added",
    rerecordable: true,
    outputPath: "朝のどんぐりカフェ/2026-04-15 朝のどんぐりカフェ.m4a",
    stations: [slotOf("HOSHIZORA", -60 * 5.5, 120)],
  },
  {
    ...baseJob,
    id: 107,
    showName: "夕やけトラベラーズ",
    day: dayOf(-60 * 20),
    part: 1,
    state: "running",
    attempts: 1,
    progress: { stage: "fetch", fetchedSeconds: 2820, totalSeconds: 10800 },
    stations: [slotOf("HOSHIZORA", -60 * 21, 180)],
  },
  {
    ...baseJob,
    id: 109,
    showName: "夕やけトラベラーズ",
    day: dayOf(60 * 5),
    part: 1,
    state: "waiting",
    nextAttemptAt: at(60 * 8),
    deadline: at(60 * 24 * 7),
    skippable: true,
    stations: [slotOf("HOSHIZORA", 60 * 5.5, 180)],
  },
  {
    ...baseJob,
    id: 110,
    showName: "星降る深夜ラジオ",
    day: dayOf(60 * 13),
    part: 1,
    state: "waiting",
    nextAttemptAt: at(60 * 17),
    deadline: at(60 * 24 * 7),
    live: true,
    skippable: true,
    stations: [slotOf("HOSHIZORA", 60 * 13.5, 180)],
  },
  {
    ...baseJob,
    id: 106,
    showName: "つむぎ夜の読み物",
    day: dayOf(-60 * 36),
    part: 1,
    state: "retrying",
    attempts: 2,
    retryable: true,
    nextAttemptAt: at(25),
    deadline: at(60 * 24 * 5),
    lastFailedStage: "fetch",
    lastError: "音声の取得に失敗した（ダミーのエラー）",
    stations: [slotOf("TSUMUGI", -60 * 37, 120)],
  },
  {
    ...baseJob,
    id: 105,
    showName: "波打ちぎわの昭和歌謡",
    day: dayOf(-60 * 24 * 2),
    part: 1,
    state: "done",
    attempts: 1,
    chapters: "not_added",
    rerecordable: true,
    outputPath: "波打ちぎわの昭和歌謡/2026-04-13 波打ちぎわの昭和歌謡.m4a",
    stations: [slotOf("MINATO", -60 * 24 * 2 - 60 * 3, 180)],
  },
  {
    ...baseJob,
    id: 104,
    showName: "月曜の小さな図書館",
    day: dayOf(-60 * 24 * 3),
    part: 1,
    state: "done",
    attempts: 1,
    chapters: "added",
    rerecordable: true,
    outputPath: "月曜の小さな図書館/2026-04-12 月曜の小さな図書館.m4a",
    stations: [slotOf("HOSHIZORA", -60 * 24 * 3 - 60 * 2, 120)],
  },
  {
    ...baseJob,
    id: 103,
    showName: "つむぎ夜の読み物",
    day: dayOf(-60 * 24 * 6),
    part: 1,
    state: "expired",
    attempts: 5,
    lastFailedStage: "fetch",
    lastError: "録音の期限が過ぎた（ダミーのエラー）",
    stations: [slotOf("TSUMUGI", -60 * 24 * 6, 120)],
  },
  {
    ...baseJob,
    id: 111,
    showName: "波打ちぎわの昭和歌謡",
    day: dayOf(60 * 27),
    part: 1,
    state: "skipped",
    byPerson: true,
    recordable: true,
    stations: [slotOf("MINATO", 60 * 27.5, 180)],
  },
];

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
  attemptLog: [],
  durationSeconds: j.stations.reduce((n, s) => n + (new Date(s.end).getTime() - new Date(s.start).getTime()) / 1000, 0),
});

/** 画像の代わりに返す、単色の SVG（番組の画像のダミー）。 */
export const mockImage = (n: string) => {
  const colors: Record<string, string> = { "1": "#7aa7d9", "2": "#e0a96d", "3": "#8fc5a0" };
  const c = colors[n] ?? "#bbbbbb";
  return `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200" viewBox="0 0 200 200"><rect width="200" height="200" fill="${c}"/><circle cx="100" cy="100" r="46" fill="#ffffff" fill-opacity="0.6"/></svg>`;
};
