package domain

import (
	"fmt"
	"time"
)

// Show は番組。保存先の番組名を名前とし、名前で一意に決まる。
type Show struct {
	// Name は保存先の番組名。番組を識別するキーでもある。
	Name string
	// Split は分割番組か（1 回の放送が番組表で複数の放送回に分かれて載る番組）と、分割番組ならパートを連結するか。
	// 零値は分割番組でない（SplitNone）。連結するのは SplitJoin だけ（ADR 0003）。保存の track の決め方にも使う（ADR 0002）。
	Split SplitMode
	// CheckTitle は、録音済みと録音できなかったの通知に添える番組名の食い違いを調べるか。既定は調べる（NewShow を使う）。
	// 毎週の予約だけに当たる放送回で、番組名の食い違う回を録らない予定・録らなかったにするか（Show.Hit。
	// ADR 0018・0019）もこれで決める。
	// 保存の title の形には関係しない（ADR 0007）。
	CheckTitle bool
	// Disabled は無効な番組か（人が止めている）。無効な番組の予約（毎週と 1 回）は放送回に当たらない（Show.Matches）。
	// 零値（有効）が既定になるように、有効ではなく無効を持つ。
	Disabled bool
	// Weekly は番組に属する毎週の予約。持たない番組もある（1 回の予約だけを持つ番組）。
	Weekly []Weekly
	// Onces は番組に属する 1 回の予約。番組の作成・置き換えの本文には書かず、1 回の予約の API で足す・消す。キーワードが足したものも同じ。
	// 0 個でもよい（録り終えた 1 回の予約は消えるが、番組は残る）。
	Onces []Once
}

// Once は 1 回の予約。特定の放送回 1 つを、局と開始時刻（Airing.Key と同じ）で指定する。
// 番組に属し、保存先の番組名や連結の設定はその番組に従う。
type Once struct {
	// ID は保存先が振る。まだ保存していない 1 回の予約では 0。
	ID        int64
	StationID string
	Start     time.Time
	// KeywordID は、この 1 回の予約を足したキーワードの ID。人が足したもの、足したキーワードを消した後は 0。
	// 足した後は、人が足したものと同じに扱う（印として持つだけで、展開には使わない）。
	KeywordID int64
}

// Key は 1 回の予約が指す放送回の Key（Airing.Key と同じ形）。
func (o Once) Key() string {
	return Airing{StationID: o.StationID, Start: o.Start}.Key()
}

// Day は 1 回の予約が指す放送回の放送日（開始時刻で決まる）。
func (o Once) Day() BroadcastDay {
	return BroadcastDayOf(o.Start)
}

// Deadline は 1 回の予約を消してよくなる時刻（放送回の録音期限の見積もり）。1 回の予約は終了時刻を持たないので、
// 放送回の録音期限（AiringDeadline。終了時刻が属する放送日から数える）より早くならないように、開始時刻が属する
// 放送日の翌日に終わったとみなして数える。放送日の区切り（5:00）をまたがない放送回では、本当の録音期限より
// 1 日遅くなる（消すのが遅れるだけ）。
func (o Once) Deadline(m Membership) time.Time {
	a := Airing{StationID: o.StationID, Start: o.Start}
	return BroadcastDayOf(o.Start).AddDays(1 + AvailableDays(m, a)).End()
}

// NewShow は既定（分割番組ではない・番組名の食い違いを調べる）の番組を作る。
func NewShow(name string, weekly ...Weekly) Show {
	return Show{Name: name, CheckTitle: true, Weekly: weekly}
}

// SplitMode は番組の分割番組の設定（ADR 0003）。分割番組でない、分割番組でつなぐ、分割番組でつながない、の 3 つ。
// 零値は分割番組でない。
type SplitMode int

const (
	// SplitNone は分割番組でない番組。同じ放送日に放送回が 2 つ以上当たっても連結せず、放送回ごとのジョブにする。
	SplitNone SplitMode = iota
	// SplitJoin は分割番組で、パートを連結する番組。
	SplitJoin
	// SplitSeparate は分割番組で、パートを連結しない番組。
	SplitSeparate
)

// String は API の JSON の値（"none"・"join"・"separate"）を返す。
func (m SplitMode) String() string {
	switch m {
	case SplitJoin:
		return "join"
	case SplitSeparate:
		return "separate"
	}
	return "none"
}

// ParseSplitMode は API の JSON の値（"none"・"join"・"separate"）を読む。ほかの値はエラー。
func ParseSplitMode(s string) (SplitMode, error) {
	switch s {
	case "none":
		return SplitNone, nil
	case "join":
		return SplitJoin, nil
	case "separate":
		return SplitSeparate, nil
	}
	return SplitNone, fmt.Errorf("split は \"none\"・\"join\"・\"separate\" のどれか（%q は使えない）", s)
}

// Joins は、番組がジョブの放送回を連結するか（分割番組でつなぐ番組だけ）。
func (s Show) Joins() bool {
	return s.Split == SplitJoin
}

// IsSplit は、番組が分割番組か（つなぐか、つながないか）。
func (s Show) IsSplit() bool {
	return s.Split == SplitJoin || s.Split == SplitSeparate
}

// Weekly は毎週の予約。局・曜日・開始時刻で放送回に当たる。曜日は放送日で数える。開始日（From）があれば、それより前の放送日の放送回には当たらない（零値なら制限なし）。
// 終了日（Until）があれば、それより後の放送日の放送回には当たらない（終了日の放送日の放送回には当たる。零値なら制限なし）。
// 今日より前の放送日の放送回でジョブを作るのは、開始日を持つものだけ（Show.Hit）。
type Weekly struct {
	StationID string
	Weekday   time.Weekday
	Start     Clock
	// From は開始日（放送日）。これより前の放送日の放送回には当たらない。零値（IsZero）なら制限なし。
	From BroadcastDay
	// Until は終了日（放送日）。これより後の放送日の放送回には当たらない（終了日の放送日は当たる）。零値（IsZero）なら制限なし。
	Until BroadcastDay
}

// NewWeekly は毎週の予約を作る。
func NewWeekly(station string, weekday time.Weekday, start Clock) Weekly {
	return Weekly{StationID: station, Weekday: weekday, Start: start}
}

// WithFrom は開始日 from（放送日）を持つ毎週の予約を返す。
func (r Weekly) WithFrom(from BroadcastDay) Weekly {
	r.From = from
	return r
}

// WithUntil は終了日 until（放送日）を持つ毎週の予約を返す。
func (r Weekly) WithUntil(until BroadcastDay) Weekly {
	r.Until = until
	return r
}

// InDays は、放送日 d が毎週の予約の開始日と終了日の内側にあるか（どちらも含む。零値の端は制限なし）。曜日は見ない。
func (r Weekly) InDays(d BroadcastDay) bool {
	return (r.From.IsZero() || !d.Before(r.From)) && (r.Until.IsZero() || !r.Until.Before(d))
}

// Airing は放送回。radiko の型には依存しない。
type Airing struct {
	// ProgramID は番組表上の ID。メタデータとして持つだけで、同一性には使わない
	// （分割番組の放送回が同じ ID で返るため）。空でもよい。
	ProgramID   string
	StationID   string
	Start       time.Time
	End         time.Time
	Title       string
	Performer   string
	Description string
	// DescriptionParts は Description を、リンクの部品とそうでない部品に分けた並び。
	// text を全部つないだものは Description と同じ。番組表から読んだときだけ入り、DB には書かず、
	// equal では比べない（番組表の画面でリンクとして描くためだけに持つ）。
	DescriptionParts []DescriptionPart
	ImageURL         string
	// TimeFree は番組表のタイムフリーの可否のフラグ。
	TimeFree TimeFreeFlags
}

// DescriptionPart は説明の部品。Href が空でなければリンク。
type DescriptionPart struct {
	Text string
	Href string
}

// TimeFreeFlags は番組表のタイムフリーの可否のフラグの写し（radiko の ts_in_ng・ts_out_ng）。
// 値の解釈はまだ分かっていないので、そのまま持つ。
type TimeFreeFlags struct {
	InNG  int
	OutNG int
}

// TimeFreeExcluded は、放送回がタイムフリーの対象外か（番組表の ts_in_ng が 0 でない）。対象外の放送回はライブで録る。
func (a Airing) TimeFreeExcluded() bool {
	return a.TimeFree.InNG != 0
}

// LiveAirings は、放送回 airings を持つジョブをライブで録るか。放送回の 1 つでもタイムフリーの対象外なら、全部をライブで録る
// （方式はジョブごとに決め、番組や予約には選ばせない）。それ以外はタイムフリーから録る。
func LiveAirings(airings []Airing) bool {
	for _, a := range airings {
		if a.TimeFreeExcluded() {
			return true
		}
	}
	return false
}

// Key は放送回を識別するキー。局と開始時刻で決まる。
// 番組表の ID は使わない（radiko では分割番組の放送回が同じ ID で返る）。
func (a Airing) Key() string {
	return a.StationID + "@" + a.Start.UTC().Format(time.RFC3339)
}

// Day は放送回が属する放送日（開始時刻で決まる）。
func (a Airing) Day() BroadcastDay {
	return BroadcastDayOf(a.Start)
}

// equal は放送回の中身が同じか。時刻は瞬間として比べる。
func (a Airing) equal(b Airing) bool {
	return a.ProgramID == b.ProgramID && a.StationID == b.StationID &&
		a.Start.Equal(b.Start) && a.End.Equal(b.End) &&
		a.Title == b.Title && a.Performer == b.Performer &&
		a.Description == b.Description && a.ImageURL == b.ImageURL &&
		a.TimeFree == b.TimeFree
}

// Membership は会員種別。
type Membership string

const (
	// MembershipFree は非会員。
	MembershipFree Membership = "free"
	// MembershipAreaFree はエリアフリー。
	MembershipAreaFree Membership = "areafree"
	// MembershipTimeFree30 はタイムフリー30。
	MembershipTimeFree30 Membership = "timefree30"
	// MembershipDouble はダブルプラン。
	MembershipDouble Membership = "double"
)

// ParseMembership は会員種別の文字列を読む。
func ParseMembership(s string) (Membership, error) {
	switch m := Membership(s); m {
	case MembershipFree, MembershipAreaFree, MembershipTimeFree30, MembershipDouble:
		return m, nil
	}
	return "", fmt.Errorf("不明な会員種別 %q", s)
}

// Job はジョブ。1 つの番組の 1 放送日分（連結しない番組では 1 パート分）を録音する仕事。
type Job struct {
	// ID は保存先が振る。まだ保存していないジョブでは 0。
	ID       int64
	ShowName string
	Day      BroadcastDay
	// Part はパート番号。連結しない番組で、同じ放送日に複数の放送回があるときだけ 1 から振る。
	// 付けないときは 0。
	Part  int
	State State
	// Airings はジョブの放送回。開始時刻の順。
	Airings []Airing
	// NextAttemptAt は次の試行の時刻。
	NextAttemptAt time.Time
	// Attempts は試行回数。手動再試行で 0 に戻す。
	Attempts int
	// Deadline は録音期限。
	Deadline time.Time
	// OutputPath は保存先のパス。保存するまでは空。
	OutputPath string
	// Missing は欠落した放送回（Airings の一部）。
	Missing []Airing
	// ByPerson は、人が録る・録らないを決めた印（ADR 0019）。人が録らないにしたジョブは skipped、人が録るにしたジョブは
	// waiting で、どちらも展開が番組名で状態を変えない（休止・番組の無効化・メタデータの更新は効く）。一度付けたら外さない。
	ByPerson bool
	// Settled は、skipped のジョブが確定したか（用語集の「録らない予定」と「録らなかった」）。false の skipped は
	// 録らない予定で、展開が waiting のジョブと同じに扱う。最初の試行の時刻を過ぎると確定する（今日より前の放送日で
	// 展開が作る skipped のジョブは、作った時点で確定している）。skipped でないジョブでは使わない。
	Settled bool
	// Chapters は、保存したファイルにチャプターが入ったか（入った・待っている・入らなかった。ADR 0027）。取りに行っていない
	// ジョブ（ライブ録音、設定 chapters がオフ、まだ保存していない、チャプターの取り直しの前の版で保存した）は ChaptersNone。
	// ChapterCheckAt と ChapterWaits は、待っている（ChaptersWaiting）間だけ使う。
	Chapters Chapters
	// ChapterCheckAt は、待っているジョブが次にチャプターの API を引く時刻。
	ChapterCheckAt time.Time
	// ChapterWaits は、待っている放送回（Airing.Key → AiringChapterPending か AiringChapterReady）。待っていない放送回は持たない。
	ChapterWaits map[string]AiringChapterWait
	// ChapterRefetch は、今の試行（retrying・running）がチャプターの取り直しで起こしたものである印。録れなかったときに
	// 録音できなかったにせず録音済みに戻し、通知しないために持つ（人が頼んだ録り直しとは別。ADR 0027）。
	ChapterRefetch bool
}

// Expandable は、展開が触るジョブか（waiting と、確定の前の skipped）。展開は、これに当たるジョブの放送回・パート番号・
// 状態を番組表に合わせて変え、放送回が 0 になれば消す。ほかのジョブ（確定した skipped を含む）は触らない。
func (j Job) Expandable() bool {
	return j.State == StateWaiting || j.State == StateSkipped && !j.Settled
}

// Live は、ジョブをライブで録るか（LiveAirings）。方式は、ジョブの放送回の番組表の値（展開が写した ts_in_ng）で決める。
// ライブで録るジョブは、ライブの録音役（internal/app）が拾い、タイムフリーの録音のループは拾わない。入手（録音）の失敗では
// retrying にせず、録れた分の保存が失敗したとき（保存か検証の段階の失敗）だけ retrying にして保存をやり直す。
// 足したら直す: ジョブの状態の移り方
func (j Job) Live() bool {
	return LiveAirings(j.Airings)
}

// LastEnd はジョブの最後の放送回の終了時刻。
func (j Job) LastEnd() time.Time {
	var t time.Time
	for _, a := range j.Airings {
		if a.End.After(t) {
			t = a.End
		}
	}
	return t
}

// Stage は試行の段階。
type Stage string

const (
	// StageFetch は入手の段階。
	StageFetch Stage = "fetch"
	// StageVerify は検証の段階。
	StageVerify Stage = "verify"
	// StageSave は保存の段階。
	StageSave Stage = "save"
)

// Attempt は試行。ジョブを 1 回実行した記録。
type Attempt struct {
	ID        int64
	JobID     int64
	StartedAt time.Time
	EndedAt   time.Time
	// FailedStage は失敗した段階。成功なら空。
	FailedStage Stage
	// Error は失敗のエラーの文。
	Error string
	// ExtinfTotal は全セグメントの #EXTINF の合計。
	ExtinfTotal time.Duration
	// ProbeDuration は、格納した m4a の長さ（muxer が ffmpeg なら ffprobe で測り、builtin なら m4a を読み直す）。
	ProbeDuration time.Duration
}
