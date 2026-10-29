package server

// 1 回の予約の API（Web UI の番組の画面と番組表の画面が呼ぶ）。どれもセッションが要る。
//
//   - POST /api/shows/{name}/onces: 番組に 1 回の予約を足す。body は {"airings": [{"station": 局, "start": 開始時刻(RFC 3339)}, ...]}。
//     1 回で複数の放送回を受け取り、全部足すか 1 つも足さないか（store が 1 つのトランザクションで足す）。
//     足す前に、エリアの日別番組表（AreaGuide）でその放送回があることを確かめる。
//     成功したら 201 と、足した 1 回の予約の一覧（{"onces": [...]}。GET /api/shows/{name} の onces と同じ形）を返す
//   - DELETE /api/shows/{name}/onces/{id}: 1 回の予約を消す。{name} の番組の 1 回の予約でなければ 404
//   - GET /api/shows/{name}（shows.go）は onces に 1 回の予約の一覧を付ける（onceJSON）。キーワードが足した
//     1 回の予約には keywordId（足したキーワードの ID）と keywordWords（そのキーワードの語）を付ける（人が足したもの、
//     足したキーワードを消した後は付けない）
//
// 足した・消した後に、その局・放送日の展開を展開役に頼み（Expander）、展開を待たずに応答を返す（shows.go の expand）。
//
// 断るとき:
//   - 400: JSON が読めない、airings が空、局か開始時刻が空、同じ放送回が重なっている、
//     番組表の範囲（今日の 7 日前から 7 日先までの放送日）の外、番組表にその放送回が無い、
//     録音期限を過ぎた放送回（タイムフリーの対象外の放送回（ライブで録る）は domain.AiringDeadline（放送の終了の 5 分後）、
//     それ以外は domain.Once.Deadline で数える。タイムフリーから録る放送回は、今の会員種別（非会員）では番組表の範囲の内側で
//     録音期限を過ぎないので起きない（会員種別が増えたときの防御））
//   - 404: 番組が無い（消すときは、その番組にその 1 回の予約が無い）
//   - 409: ほかの番組が当てている（reason: other_show。showName にその番組の名前）、同じ番組がすでに当てている（reason: already。showName は同じ番組）。
//     409 のときは airing に断った放送回の {station, start} を付ける（並行するリクエストとの競合で store に
//     断られたときは、どの放送回か分からないので付けない）
//   - 502: radiko から番組表を取れない
//
// 「当てている」かの判定は domain.CheckOnce で、キーワードが 1 回の予約を足すとき（internal/app/keyword.go）と同じ関数を使う。
// 番組表の API の state（guide.go の shows.view）に合わせて決める（同じ番組の当たり方は、展開と同じ
// domain.Show.Hit）。ただし完全に同じではない: ほかの番組の毎週・1 回の予約に当たる放送回は、今日より前の
// 放送日でも other_show で断る（足すと展開で矛盾になるため）。番組表では、その放送回は過去の放送日だと、
// 開始日を持たない毎週の予約なら「予約なし」と出る。
//   - 放送回にジョブがあれば、そのジョブの番組で決める（ほかの番組なら other_show、同じ番組なら already。番組表と同じ）
//   - ジョブが無ければ、ほかの番組の毎週・1 回の予約に当たるとき other_show（今日より前の放送日でも）。
//     同じ番組は、1 回の予約が同じ放送回を指しているか、同じ番組の予約で録るとき already。今日より前の
//     放送日で、開始日を持たない毎週の予約だけに当たる放送回と、録らない予定・録らなかったにする放送回（どの放送日でも）で
//     ジョブが無いもの（連結する番組で一部だけ番組名が違ったとき）は、1 回の予約で足せる
//
// 無効な番組にも足せる（当たらないだけ）。無効なほかの番組の毎週・1 回の予約は「当たっている」とみなさない。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// Onces は 1 回の予約の保存先（store.Store が満たす）。
type Onces interface {
	// CreateOnces は 1 つのトランザクションで足す（全部足すか 1 つも足さないか）。
	CreateOnces(ctx context.Context, showName string, os []domain.Once) ([]int64, error)
	DeleteOnce(ctx context.Context, id int64) error
}

var _ Onces = (*store.Store)(nil)

// maxOnceBody は 1 回の予約の JSON の大きさの上限。
const maxOnceBody = 1 << 16

// onceJSON は 1 回の予約の JSON の形。Title・End・StationName は番組表から引けたときだけ入る。
// Missing は、番組表に見える範囲の放送日なのに、その放送回が番組表に無い（開始時刻が変わった・消えた）こと。
// 番組表を引けなかった・範囲の外の放送日では false のまま（分からない）。
type onceJSON struct {
	ID          int64      `json:"id"`
	StationID   string     `json:"stationId"`
	StationName string     `json:"stationName,omitempty"`
	Start       time.Time  `json:"start"`
	End         *time.Time `json:"end,omitempty"`
	Title       string     `json:"title,omitempty"`
	Missing     bool       `json:"missing"`
	// KeywordID と KeywordWords は、キーワードが足した 1 回の予約だけに付く（足したキーワードの ID と語）。
	KeywordID    int64  `json:"keywordId,omitempty"`
	KeywordWords string `json:"keywordWords,omitempty"`
}

type airingRef struct {
	Station string    `json:"station"`
	Start   time.Time `json:"start"`
}

func (u *UI) registerOnces(mux *http.ServeMux) {
	if u.Shows == nil || u.Onces == nil {
		return
	}
	if u.AreaGuide != nil {
		mux.HandleFunc("POST /api/shows/{name}/onces", u.guard(u.authed(u.createOnces)))
	}
	mux.HandleFunc("DELETE /api/shows/{name}/onces/{id}", u.guard(u.authed(u.deleteOnce)))
}

// onceViews は 1 回の予約 os を、番組表（あれば）と突き合わせた JSON の形にする。
func (u *UI) onceViews(ctx context.Context, os []domain.Once) []onceJSON {
	out := make([]onceJSON, len(os))
	days := map[domain.BroadcastDay][]app.AreaStation{}
	failed := map[domain.BroadcastDay]bool{}
	first, last := u.guideRange()
	words := u.keywordWords(ctx, os)
	for i, o := range os {
		out[i] = onceJSON{ID: o.ID, StationID: o.StationID, Start: o.Start}
		if o.KeywordID != 0 {
			out[i].KeywordID, out[i].KeywordWords = o.KeywordID, words[o.KeywordID]
		}
		day := o.Day()
		if u.AreaGuide == nil || day.String() < first.String() || day.String() > last.String() || failed[day] {
			continue
		}
		stations, ok := days[day]
		if !ok {
			var err error
			stations, err = u.AreaGuide.AreaAirings(ctx, day)
			if err != nil {
				u.log().Warn("1 回の予約の放送回を番組表で確かめられない", "day", day, "err", err)
				failed[day] = true
				continue
			}
			days[day] = stations
		}
		a, name, found := findAiring(stations, o.Key())
		if !found {
			out[i].Missing = true
			continue
		}
		end := a.End
		out[i].End, out[i].Title, out[i].StationName = &end, a.Title, name
	}
	return out
}

// keywordWords は、1 回の予約 os を足したキーワードの ID → 語。キーワードが足したものが無い、または Keywords が
// nil なら空。読めなければログに出して空（語を付けないだけ）。
func (u *UI) keywordWords(ctx context.Context, os []domain.Once) map[int64]string {
	out := map[int64]string{}
	if u.Keywords == nil || !slices.ContainsFunc(os, func(o domain.Once) bool { return o.KeywordID != 0 }) {
		return out
	}
	ks, err := u.Keywords.ListKeywords(ctx)
	if err != nil {
		u.log().Warn("1 回の予約を足したキーワードを読めない", "err", err)
		return out
	}
	for _, k := range ks {
		out[k.ID] = k.Words
	}
	return out
}

// findAiring は番組表 stations から Key が key の放送回を探す。
func findAiring(stations []app.AreaStation, key string) (domain.Airing, string, bool) {
	for _, st := range stations {
		for _, a := range st.Airings {
			if a.Key() == key {
				return a, st.Name, true
			}
		}
	}
	return domain.Airing{}, "", false
}

// conflict は 409 の本文。
type conflict struct {
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	ShowName string `json:"showName,omitempty"`
	// Airing は断った放送回。分かるときだけ付く（競合で store に断られたときは付かない）。
	Airing *airingRef `json:"airing,omitempty"`
}

func (u *UI) createOnces(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Airings []airingRef `json:"airings"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOnceBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	if len(req.Airings) == 0 {
		bad("airings が空")
		return
	}
	seen := map[string]bool{}
	for _, a := range req.Airings {
		o := domain.Once{StationID: a.Station, Start: a.Start}
		if err := o.Validate(); err != nil {
			bad(err.Error())
			return
		}
		if seen[o.Key()] {
			bad("同じ放送回が重なっている: " + o.Key())
			return
		}
		seen[o.Key()] = true
	}
	shows, err := u.Shows.ListShows(r.Context())
	if err != nil {
		u.internalError(w, "番組を読めない", err)
		return
	}
	var self *domain.Show
	for i := range shows {
		if shows[i].Name == name {
			self = &shows[i]
		}
	}
	if self == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "番組が無い"})
		return
	}

	// 番組表で確かめる。放送日ごとに 1 回だけ引く。
	first, last := u.guideRange()
	days := map[domain.BroadcastDay][]app.AreaStation{}
	var found []domain.Airing
	for _, ref := range req.Airings {
		o := domain.Once{StationID: ref.Station, Start: ref.Start}
		day := o.Day()
		if day.String() < first.String() || day.String() > last.String() {
			bad("放送日 " + day.String() + " は番組表の範囲（" + first.String() + " から " + last.String() + "）の外")
			return
		}
		stations, ok := days[day]
		if !ok {
			stations, err = u.AreaGuide.AreaAirings(r.Context(), day)
			if err != nil {
				u.areaError(w, day, err)
				return
			}
			days[day] = stations
		}
		a, _, ok := findAiring(stations, o.Key())
		if !ok {
			bad("番組表に無い放送回: " + o.Key())
			return
		}
		// 録音期限を過ぎた放送回は足しても録れない。タイムフリーの対象外の放送回（ライブで録る）は、放送の終了の 5 分後
		// （domain.AiringDeadline）を過ぎたら断る。タイムフリーから録る放送回は、今の会員種別（非会員）では番組表の範囲の
		// 内側で録音期限を過ぎないので起きない（会員種別が増えたときの防御）。
		deadline := domain.Once{StationID: a.StationID, Start: a.Start}.Deadline(u.membership())
		if a.TimeFreeExcluded() {
			deadline = domain.AiringDeadline(a, u.membership())
		}
		if !u.now().Before(deadline) {
			bad("録音期限を過ぎた放送回: " + o.Key())
			return
		}
		found = append(found, a)
	}

	// 放送回にジョブがあるかを、番組表の API と同じ範囲と優先で見るために、ジョブを読む。
	jobs := map[string]domain.Job{}
	if u.Jobs != nil {
		js, err := u.Jobs.ListJobsFromDay(r.Context(), u.visibleFromDay())
		if err != nil {
			u.internalError(w, "ジョブを読めない", err)
			return
		}
		jobs = domain.JobsByAiring(js)
	}
	// 足せるかの判定は、キーワードが 1 回の予約を足すときと同じ関数で行う（domain.CheckOnce）。
	now := u.now()
	for _, a := range found {
		ref := &airingRef{Station: a.StationID, Start: a.Start}
		switch reason, holder := domain.CheckOnce(a, self, shows, jobs, now, u.membership()); reason {
		case domain.RefuseOtherShow:
			writeJSON(w, http.StatusConflict, conflict{Error: "ほかの番組「" + holder + "」がすでに当てている: " + a.Title,
				Reason: reason, ShowName: holder, Airing: ref})
			return
		case domain.RefuseAlready:
			writeJSON(w, http.StatusConflict, conflict{Error: "すでに録音予定: " + a.Title, Reason: reason, ShowName: name, Airing: ref})
			return
		}
	}

	// 全部足すか、1 つも足さないか（store が 1 つのトランザクションで足す）。
	os := make([]domain.Once, len(found))
	for i, a := range found {
		os[i] = domain.Once{StationID: a.StationID, Start: a.Start}
	}
	ids, err := u.Onces.CreateOnces(r.Context(), name, os)
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			writeJSON(w, http.StatusConflict, conflict{Error: "すでに録音予定の放送回がある", Reason: "already", ShowName: name})
			return
		}
		u.storeError(w, "1 回の予約を足せない", err)
		return
	}
	after, err := u.Shows.GetShow(r.Context(), name)
	if err != nil {
		u.internalError(w, "番組を読めない", err)
		return
	}
	u.expand(after)
	var added []domain.Once
	for _, o := range after.Onces {
		for _, id := range ids {
			if o.ID == id {
				added = append(added, o)
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"onces": u.onceViews(r.Context(), added)})
}

func (u *UI) deleteOnce(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID が整数ではない"})
		return
	}
	show, err := u.Shows.GetShow(r.Context(), name)
	if err != nil {
		u.storeError(w, "番組を読めない", err)
		return
	}
	found := false
	for _, o := range show.Onces {
		found = found || o.ID == id
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "1 回の予約が無い"})
		return
	}
	if err := u.Onces.DeleteOnce(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "1 回の予約が無い"})
			return
		}
		u.internalError(w, "1 回の予約を消せない", err)
		return
	}
	// 消す前の番組で展開する（1 回の予約の局・放送日を含めるため）。
	u.expand(show)
	w.WriteHeader(http.StatusNoContent)
}
