package server

// 番組の管理の API（Web UI の番組の画面が呼ぶ）。どれもセッションが要る。
//
//   - GET /api/shows: 番組の一覧。番組ごとに、次の放送予定（その番組の waiting のジョブの放送回のうち、
//     開始時刻が今より後のいちばん早いものの開始時刻）を付ける。無い番組と無効な番組には付けない。
//     当たらない日（unmatched_days。枠の開始時刻が今より後のもの。放送日・枠の開始時刻・その時刻を含む放送回・番組名の合う
//     放送回）も、枠の開始時刻の順に付ける。無い番組と無効な番組には付けない（store.UnmatchedDays）
//   - POST /api/shows: 番組を作る。同じ名前の番組があれば 409。enabled・check_title を省けば true、split を省けば
//     "none"（分割番組でない）。split は "none"・"join"・"separate" のどれか（ADR 0003）。
//     毎週の予約は 0 個でもよい（1 回の予約だけを持つ番組）
//   - GET /api/shows/{name}: 番組を 1 つ読む。1 回の予約の一覧を onces に付ける（onces.go）
//   - PUT /api/shows/{name}: 番組を丸ごと置き換える。body の name が {name} と違えば名前の変更で、
//     古い名前の番組を消して新しい名前の番組を作り、古い名前の done でないジョブを新しい名前に付け替える
//     （store.ReplaceShow）。新しい名前の番組がもうあれば 409。
//     1 回の予約は body に書かず、今の番組のものを残す（名前の変更なら引き継ぐ）
//   - DELETE /api/shows/{name}: 番組を 1 回の予約ごと消す。ジョブは消さない
//   - PUT /api/shows/{name}/enabled: 有効／無効を切り替える（{"enabled": bool}）
//   - GET /api/stations: radiko の局の一覧
//   - GET /api/show-name?station=&weekday=&start=: 新しい番組の補助。曜日 weekday（0 = 日曜）の、今日から
//     6 日先までの放送日の番組表から、開始時刻 start（HH:MM）が一致する放送回を探し、番組名から番組名の末尾の
//     印を落とした名前を返す。見つからなければ found が false
//
// {name} は番組の名前を URL エンコードしたもの。
//
// 番組の作成・置き換え・削除・有効／無効の切り替えが成功したら、次の番組表の同期を待たずに、その番組の予約
// （変える前と後の両方の毎週と 1 回の予約）に出てくる局の、今日から 7 日先までの放送日と、1 回の予約の過去の
// 放送日を展開する（Expander。app.App.RequestExpand）。展開は serve の展開役に頼むだけで、待たずに応答を返す
// （応答の直後に読み直すと、ジョブがまだできていないことがある）。展開に失敗しても番組の保存は成功のまま
// （展開役がログに出し、展開できなかった局・放送日を、間を延ばしながら試し直す）。
//
// 拒むとき: JSON が読めない・値が正しくない（名前が空、時刻が HH:MM でない、曜日が 0〜6 でない、局が空、split が
// 3 つの文字列のどれでもない（bool を含む）、使わなくなった join のキーがあるなど）、
// /enabled の body に enabled が無い、/api/show-name の station・weekday（0〜6）・start が無い・正しくないときは
// 400。{name} の番組が無ければ 404。同じ名前の番組がもうあれば 409。radiko から局の一覧・番組表を取れなければ 502。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// Shows は番組の保存先（store.Store が満たす）。
type Shows interface {
	ListShows(ctx context.Context) ([]domain.Show, error)
	GetShow(ctx context.Context, name string) (domain.Show, error)
	CreateShow(ctx context.Context, show domain.Show) error
	ReplaceShow(ctx context.Context, oldName string, show domain.Show) error
	DeleteShow(ctx context.Context, name string) error
	SetShowEnabled(ctx context.Context, name string, enabled bool) error
	NextAirings(ctx context.Context, after time.Time) (map[string]time.Time, error)
	UnmatchedDays(ctx context.Context, after time.Time) (map[string][]domain.UnmatchedDay, error)
}

var _ Shows = (*store.Store)(nil)

// Expander は番組・キーワードを変えた直後の展開（app.App が満たす）。RequestExpand は、shows の番組に出てくる局・
// 放送日の展開を展開役に頼み、展開を待たずに戻る。展開に使う番組の一覧は shows ではなく、展開するときの保存先の番組の一覧。
// RequestKeywords は、キーワードを番組表に当てる（当たった放送回を 1 回の予約として足し、続けて展開する）ことを
// 展開役に頼み、待たずに戻る。
type Expander interface {
	RequestExpand(shows ...domain.Show)
	RequestKeywords()
}

var _ Expander = (*app.App)(nil)

// Recorder はタイムフリーの録音のループを起こす（app.App が満たす）。ジョブの「再試行」「録る」が成功したら、次の見回りを待たずに
// 録り始めさせる。ライブで録るジョブでも区別せずに呼ぶ（ライブの録音役は別に見回る）。
type Recorder interface {
	WakeRecorder()
}

var _ Recorder = (*app.App)(nil)

// Progresses は録音中のジョブの進捗（用語集）を読む（app.App が満たす）。進捗は serve のメモリにだけあり、DB には無い。
// 別のフィールドにはせず、UI.Recorder が満たしていれば使う（録音するものが、録っているジョブの進捗も持つため）。満たさなければ、
// ジョブの API は進捗を付けない。
type Progresses interface {
	JobProgress(id int64) (app.Progress, bool)
}

var _ Progresses = (*app.App)(nil)

// maxShowBody は番組の JSON の大きさの上限。
const maxShowBody = 1 << 16

// suggestDays は、新しい番組の補助で番組表を探す放送日の数（今日から）。曜日ごとに 1 日だけ当たる。
const suggestDays = 7

// registerShows は番組の API を mux に載せる。Shows が nil なら載せない。
func (u *UI) registerShows(mux *http.ServeMux) {
	if u.Shows == nil {
		return
	}
	mux.HandleFunc("GET /api/shows", u.guard(u.authed(u.listShows)))
	mux.HandleFunc("POST /api/shows", u.guard(u.authed(u.createShow)))
	mux.HandleFunc("GET /api/shows/{name}", u.guard(u.authed(u.getShow)))
	mux.HandleFunc("PUT /api/shows/{name}", u.guard(u.authed(u.replaceShow)))
	mux.HandleFunc("DELETE /api/shows/{name}", u.guard(u.authed(u.deleteShow)))
	mux.HandleFunc("PUT /api/shows/{name}/enabled", u.guard(u.authed(u.setShowEnabled)))
	if u.Guide != nil {
		mux.HandleFunc("GET /api/stations", u.guard(u.authed(u.stations)))
		mux.HandleFunc("GET /api/show-name", u.guard(u.authed(u.suggestShowName)))
	}
}

// weeklyJSON は毎週の予約の JSON の形。weekday は 0 = 日曜（放送日で数える）、start は HH:MM。
type weeklyJSON struct {
	Station string `json:"station"`
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	// From は開始日（放送日、YYYY-MM-DD）。省く（空）と制限なし。
	From string `json:"from,omitempty"`
	// Until は終了日（放送日、YYYY-MM-DD。その放送日を含む）。省く（空）と制限なし。開始日より前なら 400。
	Until string `json:"until,omitempty"`
}

// unmatchedAiringJSON は当たらない日に添える放送回（番組名・開始・終了）。
type unmatchedAiringJSON struct {
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// unmatchedDayJSON は当たらない日の JSON の形。day は放送日（YYYY-MM-DD）、start は枠の開始時刻。covering はその時刻を含む
// 放送回（無ければ null）、titled は同じ局・同じ放送日で番組名の合う放送回（無ければ空の配列）。
type unmatchedDayJSON struct {
	Day      string                `json:"day"`
	Station  string                `json:"station"`
	Start    time.Time             `json:"start"`
	Covering *unmatchedAiringJSON  `json:"covering"`
	Titled   []unmatchedAiringJSON `json:"titled"`
}

func toUnmatchedDayJSON(u domain.UnmatchedDay) unmatchedDayJSON {
	out := unmatchedDayJSON{Day: u.Day.String(), Station: u.StationID, Start: u.Start, Titled: []unmatchedAiringJSON{}}
	if c := u.Covering; c != nil {
		out.Covering = &unmatchedAiringJSON{Title: c.Title, Start: c.Start, End: c.End}
	}
	for _, a := range u.Titled {
		out.Titled = append(out.Titled, unmatchedAiringJSON{Title: a.Title, Start: a.Start, End: a.End})
	}
	return out
}

// showJSON は番組の JSON の形。NextAiring と UnmatchedDays は一覧でだけ埋める。
// 受け取るとき、enabled・check_title を省けば true とみなす（split を省けば "none"）。
type showJSON struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
	// Join は使わなくなったキー（ADR 0003）。受け取ったら 400 にするためだけに持ち、返すときは書かない。
	Join json.RawMessage `json:"join,omitempty"`
	// Split は分割番組の設定（"none"・"join"・"separate"）。受け取るとき、bool やほかの値は 400。
	Split      *string      `json:"split"`
	CheckTitle *bool        `json:"check_title"`
	Weekly     []weeklyJSON `json:"weekly"`
	NextAiring *time.Time   `json:"next_airing,omitempty"`
	// UnmatchedDays は一覧でだけ埋める当たらない日の並び。無ければ省く。受け取るときは読まない。
	UnmatchedDays []unmatchedDayJSON `json:"unmatched_days,omitempty"`
	// Onces は GET /api/shows/{name} でだけ埋める 1 回の予約の一覧（onces.go）。受け取るときは読まない。
	Onces []onceJSON `json:"onces,omitempty"`
}

func toShowJSON(s domain.Show) showJSON {
	out := showJSON{Name: s.Name, Enabled: ptr(!s.Disabled), Split: ptrString(s.Split.String()), CheckTitle: ptr(s.CheckTitle),
		Weekly: make([]weeklyJSON, len(s.Weekly))}
	for i, r := range s.Weekly {
		out.Weekly[i] = weeklyJSON{Station: r.StationID, Weekday: int(r.Weekday), Start: r.Start.String(),
			From: dayString(r.From), Until: dayString(r.Until)}
	}
	return out
}

// dayString は開始日・終了日の JSON の値。制限なし（零値）なら空。
func dayString(d domain.BroadcastDay) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

func ptr(b bool) *bool { return &b }

func ptrString(s string) *string { return &s }

// orTrue は、省かれていれば（nil）true、あればその値を返す。
func orTrue(b *bool) bool { return b == nil || *b }

func (sj showJSON) toShow() (domain.Show, error) {
	if sj.Join != nil {
		return domain.Show{}, errors.New("join は使えない（分割番組の設定は split に \"none\"・\"join\"・\"separate\" で書く）")
	}
	show := domain.Show{Name: sj.Name, Disabled: !orTrue(sj.Enabled), CheckTitle: orTrue(sj.CheckTitle)}
	if sj.Split != nil {
		split, err := domain.ParseSplitMode(*sj.Split)
		if err != nil {
			return domain.Show{}, err
		}
		show.Split = split
	}
	for i, rj := range sj.Weekly {
		start, err := domain.ParseClock(rj.Start)
		if err != nil {
			return domain.Show{}, errors.New("毎週の予約 " + strconv.Itoa(i+1) + ": " + err.Error())
		}
		var from, until domain.BroadcastDay
		if rj.From != "" {
			if from, err = domain.ParseBroadcastDay(rj.From); err != nil {
				return domain.Show{}, errors.New("毎週の予約 " + strconv.Itoa(i+1) + ": " + err.Error())
			}
		}
		if rj.Until != "" {
			if until, err = domain.ParseBroadcastDay(rj.Until); err != nil {
				return domain.Show{}, errors.New("毎週の予約 " + strconv.Itoa(i+1) + ": " + err.Error())
			}
		}
		show.Weekly = append(show.Weekly, domain.NewWeekly(rj.Station, time.Weekday(rj.Weekday), start).WithFrom(from).WithUntil(until))
	}
	if err := show.Validate(); err != nil {
		return domain.Show{}, err
	}
	return show, nil
}

// storeError は store のエラーを HTTP の状態にする。
func (u *UI) storeError(w http.ResponseWriter, msg string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "番組が無い"})
	case errors.Is(err, store.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "同じ名前の番組がもうある"})
	default:
		u.internalError(w, msg, err)
	}
}

func (u *UI) listShows(w http.ResponseWriter, r *http.Request) {
	shows, err := u.Shows.ListShows(r.Context())
	if err != nil {
		u.internalError(w, "番組を読めない", err)
		return
	}
	next, err := u.Shows.NextAirings(r.Context(), u.now())
	if err != nil {
		u.internalError(w, "次の放送予定を読めない", err)
		return
	}
	unmatched, err := u.Shows.UnmatchedDays(r.Context(), u.now())
	if err != nil {
		u.internalError(w, "当たらない日を読めない", err)
		return
	}
	out := make([]showJSON, len(shows))
	for i, s := range shows {
		out[i] = toShowJSON(s)
		// 無効な番組には出さない（waiting のジョブが次の番組表の取り直しで消えるまでの間も）。
		if t, ok := next[s.Name]; ok && !s.Disabled {
			out[i].NextAiring = &t
		}
		// 当たらない日も、無効な番組には出さない（記録が次の展開で消えるまでの間も）。
		if !s.Disabled {
			for _, d := range unmatched[s.Name] {
				out[i].UnmatchedDays = append(out[i].UnmatchedDays, toUnmatchedDayJSON(d))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (u *UI) getShow(w http.ResponseWriter, r *http.Request) {
	show, err := u.Shows.GetShow(r.Context(), r.PathValue("name"))
	if err != nil {
		u.storeError(w, "番組を読めない", err)
		return
	}
	out := toShowJSON(show)
	out.Onces = u.onceViews(r.Context(), show.Onces)
	writeJSON(w, http.StatusOK, out)
}

// readShow は body の番組を読む。読めない・値が正しくなければ 400 を返して false。
func readShow(w http.ResponseWriter, r *http.Request) (domain.Show, bool) {
	var sj showJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, maxShowBody)).Decode(&sj); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return domain.Show{}, false
	}
	show, err := sj.toShow()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return domain.Show{}, false
	}
	return show, true
}

// currentShow は、変える前の番組 name を読む（番組を変えた直後の展開に使う）。番組が無ければ零値と true
// （変える操作の側で 404 にする）。読めなければ 500 を返して false。Expander が nil なら読まない。
func (u *UI) currentShow(w http.ResponseWriter, r *http.Request, name string) (domain.Show, bool) {
	if u.Expander == nil {
		return domain.Show{}, true
	}
	show, err := u.Shows.GetShow(r.Context(), name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		u.internalError(w, "番組を読めない", err)
		return domain.Show{}, false
	}
	return show, true
}

// expand は、番組を変えた直後に、番組 shows の番組に出てくる局・放送日の展開を展開役に頼む。
// 展開を待たずに戻る。
func (u *UI) expand(shows ...domain.Show) {
	if u.Expander == nil {
		return
	}
	u.Expander.RequestExpand(shows...)
}

func (u *UI) createShow(w http.ResponseWriter, r *http.Request) {
	show, ok := readShow(w, r)
	if !ok {
		return
	}
	if err := u.Shows.CreateShow(r.Context(), show); err != nil {
		u.storeError(w, "番組を作れない", err)
		return
	}
	u.expand(show)
	writeJSON(w, http.StatusCreated, toShowJSON(show))
}

func (u *UI) replaceShow(w http.ResponseWriter, r *http.Request) {
	show, ok := readShow(w, r)
	if !ok {
		return
	}
	before, ok := u.currentShow(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if err := u.Shows.ReplaceShow(r.Context(), r.PathValue("name"), show); err != nil {
		u.storeError(w, "番組を保存できない", err)
		return
	}
	// 1 回の予約は store が引き継ぐので、変えた後の番組の 1 回の予約は変える前と同じ。
	after := show
	after.Onces = before.Onces
	u.expand(before, after)
	writeJSON(w, http.StatusOK, toShowJSON(show))
}

func (u *UI) deleteShow(w http.ResponseWriter, r *http.Request) {
	before, ok := u.currentShow(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if err := u.Shows.DeleteShow(r.Context(), r.PathValue("name")); err != nil {
		u.storeError(w, "番組を消せない", err)
		return
	}
	u.expand(before)
	w.WriteHeader(http.StatusNoContent)
}

func (u *UI) setShowEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled が無い"})
		return
	}
	before, ok := u.currentShow(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if err := u.Shows.SetShowEnabled(r.Context(), r.PathValue("name"), *req.Enabled); err != nil {
		u.storeError(w, "番組の有効／無効を変えられない", err)
		return
	}
	u.expand(before)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}

func (u *UI) stations(w http.ResponseWriter, r *http.Request) {
	ss, err := u.Guide.Stations(r.Context())
	if err != nil {
		u.log().Warn("局の一覧を取れない", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "radiko から局の一覧を取れない"})
		return
	}
	type stationJSON struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]stationJSON, len(ss))
	for i, s := range ss {
		out[i] = stationJSON{ID: s.ID, Name: s.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

// showNameJSON は新しい番組の補助の答え。
type showNameJSON struct {
	Found bool `json:"found"`
	// Day は探した放送日（YYYY-MM-DD）。
	Day string `json:"day"`
	// Name は番組名から番組名の末尾の印を落とした名前。Title は番組表の番組名そのもの。
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
}

func (u *UI) suggestShowName(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	station := q.Get("station")
	wd, err := strconv.Atoi(q.Get("weekday"))
	if station == "" || err != nil || wd < 0 || wd > 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "station と weekday（0〜6）が要る"})
		return
	}
	start, err := domain.ParseClock(q.Get("start"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	day := suggestDay(u.now(), time.Weekday(wd))
	airings, err := u.Guide.Airings(r.Context(), station, day)
	if err != nil {
		u.log().Warn("番組表を取れない", "station", station, "day", day, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "radiko から番組表を取れない"})
		return
	}
	out := showNameJSON{Day: day.String()}
	for _, a := range airings {
		if a.StationID == station && a.Day() == day && domain.ClockOf(a.Start) == start {
			out.Found = true
			out.Name = domain.TrimTrailingMark(a.Title)
			out.Title = a.Title
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// suggestDay は、今日から 6 日先までの放送日のうち、曜日が wd のもの。
func suggestDay(now time.Time, wd time.Weekday) domain.BroadcastDay {
	today := domain.BroadcastDayOf(now)
	for i := range suggestDays {
		if d := today.AddDays(i); d.Weekday() == wd {
			return d
		}
	}
	return today // 届かない
}
