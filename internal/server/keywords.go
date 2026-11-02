package server

// キーワードの API（Web UI のキーワードの画面が呼ぶ）。どれもセッションが要る（API トークンでも使える）。
// JSON のキーは onceJSON（onces.go）と同じ camelCase。キーワードは用語集の「キーワード」（ADR 0014）。
//
//   - GET /api/keywords: キーワードの一覧（作った順）。{"keywords": [{id, words, exclude, stations, enabled, pending}]}。
//     pending は、足した放送回のうち、まだ録音を終えていない（録音予定の）回の数（上限 keyword_max_pending と比べる数）
//   - POST /api/keywords: キーワードを作る。body は {words, exclude, stations, enabled}（exclude・stations は省ける。
//     enabled を省けば true）。成功は 201 とキーワード 1 つ
//   - PUT /api/keywords/{id}: キーワードを置き換える（body は POST と同じ）。成功は 200 とキーワード 1 つ。
//     足した 1 回の予約と、足した放送回の記録は残す
//   - DELETE /api/keywords/{id}: キーワードを消す。成功は 204。足した 1 回の予約と作った番組は残す
//     （1 回の予約の keywordId は付かなくなる）。足した放送回の記録は消える
//   - GET /api/keywords/{id}/airings: 足した放送回の記録を、開始時刻の新しい順に返す。
//     {"airings": [{stationId, stationName, start, end, title, showName, state}]}。state は planned（録音予定）・
//     done（録音済み）・missed（録音できなかった）・skip_planned（録らない予定）・skipped（録らなかった）・
//     removed（録音を終える前に、1 回の予約が外された）
//   - GET /api/keywords/preview?words=&exclude=&stations=<局 ID をカンマ区切り>: 今キーワードを作ったら当たる、まだ始まっていない
//     放送回（今日から 7 日先までの放送日）。{"airings": [...], "truncated": bool}。放送回の形は GET /api/guide/search と同じ
//     （guide.go）。一致が多すぎるときは先頭の searchLimit 件にして truncated を true にする。当てる範囲はキーワードを番組表に
//     当てるとき（internal/app/keyword.go）と同じで、会員種別がエリアフリー・ダブルプランなら、stations で名指しした自分のエリアの
//     外の局の番組表も足して引き（app.OutOfAreaKeywordStations）、エリアの外から録る印（domain.Airing.OutOfArea）を立てて当てる。
//     その放送回には stationAreaName（局のエリアの都道府県名。分かるときだけ）を付ける
//
// 作った・変えた直後に、キーワードを番組表に当てることを展開役に頼み（Expander.RequestKeywords）、待たずに応答を返す。
//
// 断るとき: JSON が読めない・words が正規化して空・局の ID が空のものがある（preview では words が空）なら 400。
// {id} が整数でなければ 400、そのキーワードが無ければ 404。語の形（domain.KeywordKey）が同じほかのキーワードがあれば 409。
// preview で radiko から番組表を取れなければ 502（名指しした自分のエリアの外の局の番組表を取れないときも）。

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

// Keywords はキーワードの保存先（store.Store が満たす）。
type Keywords interface {
	ListKeywords(ctx context.Context) ([]domain.Keyword, error)
	GetKeyword(ctx context.Context, id int64) (domain.Keyword, error)
	CreateKeyword(ctx context.Context, k domain.Keyword) (int64, error)
	UpdateKeyword(ctx context.Context, k domain.Keyword) error
	DeleteKeyword(ctx context.Context, id int64) error
	KeywordAirings(ctx context.Context, id int64) ([]store.KeywordAiring, error)
	KeywordPending(ctx context.Context) (map[int64]int, error)
}

var _ Keywords = (*store.Store)(nil)

// maxKeywordBody はキーワードの JSON の大きさの上限。
const maxKeywordBody = 1 << 16

// registerKeywords はキーワードの API を mux に載せる。Keywords が nil なら載せない。preview は AreaGuide・Shows・Jobs も要る。
func (u *UI) registerKeywords(mux *http.ServeMux) {
	if u.Keywords == nil {
		return
	}
	mux.HandleFunc("GET /api/keywords", u.guard(u.authed(u.listKeywords)))
	mux.HandleFunc("POST /api/keywords", u.guard(u.authed(u.createKeyword)))
	mux.HandleFunc("PUT /api/keywords/{id}", u.guard(u.authed(u.updateKeyword)))
	mux.HandleFunc("DELETE /api/keywords/{id}", u.guard(u.authed(u.deleteKeyword)))
	mux.HandleFunc("GET /api/keywords/{id}/airings", u.guard(u.authed(u.keywordAirings)))
	if u.AreaGuide != nil && u.Shows != nil && u.Jobs != nil {
		mux.HandleFunc("GET /api/keywords/preview", u.guard(u.authed(u.previewKeyword)))
	}
}

// keywordJSON はキーワードの JSON の形。
type keywordJSON struct {
	ID       int64    `json:"id"`
	Words    string   `json:"words"`
	Exclude  string   `json:"exclude"`
	Stations []string `json:"stations"`
	Enabled  bool     `json:"enabled"`
	Pending  int      `json:"pending"`
}

func toKeywordJSON(k domain.Keyword, pending int) keywordJSON {
	stations := k.Stations
	if stations == nil {
		stations = []string{}
	}
	return keywordJSON{ID: k.ID, Words: k.Words, Exclude: k.Exclude, Stations: stations, Enabled: !k.Disabled, Pending: pending}
}

// keywordBody はキーワードを作る・変えるときの body。
type keywordBody struct {
	Words    string   `json:"words"`
	Exclude  string   `json:"exclude"`
	Stations []string `json:"stations"`
	// Enabled を省けば true。
	Enabled *bool `json:"enabled"`
}

func (b keywordBody) toKeyword() (domain.Keyword, error) {
	k := domain.Keyword{Words: b.Words, Exclude: b.Exclude, Stations: b.Stations, Disabled: !orTrue(b.Enabled)}
	if err := k.Validate(); err != nil {
		return domain.Keyword{}, err
	}
	return k, nil
}

// keywordStoreError は store のエラーを HTTP の状態にする。
func (u *UI) keywordStoreError(w http.ResponseWriter, msg string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "キーワードが無い"})
	case errors.Is(err, store.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "同じ語のキーワードがもうある"})
	default:
		u.internalError(w, msg, err)
	}
}

// requestKeywords は、キーワードを作った・変えた直後に、キーワードを番組表に当てることを展開役に頼む（待たない）。
func (u *UI) requestKeywords() {
	if u.Expander != nil {
		u.Expander.RequestKeywords()
	}
}

// pendingOf は、キーワード id の録音予定の数を読む。読めなければログに出して 0。
func (u *UI) pendingOf(ctx context.Context, id int64) int {
	pending, err := u.Keywords.KeywordPending(ctx)
	if err != nil {
		u.log().Warn("キーワードの録音予定の数を読めない", "keyword", id, "err", err)
		return 0
	}
	return pending[id]
}

func (u *UI) listKeywords(w http.ResponseWriter, r *http.Request) {
	ks, err := u.Keywords.ListKeywords(r.Context())
	if err != nil {
		u.internalError(w, "キーワードを読めない", err)
		return
	}
	pending, err := u.Keywords.KeywordPending(r.Context())
	if err != nil {
		u.internalError(w, "キーワードの録音予定の数を読めない", err)
		return
	}
	out := make([]keywordJSON, len(ks))
	for i, k := range ks {
		out[i] = toKeywordJSON(k, pending[k.ID])
	}
	writeJSON(w, http.StatusOK, map[string]any{"keywords": out})
}

// readKeyword は body のキーワードを読む。読めない・値が正しくなければ 400 を返して false。
func readKeyword(w http.ResponseWriter, r *http.Request) (domain.Keyword, bool) {
	var b keywordBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKeywordBody)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return domain.Keyword{}, false
	}
	k, err := b.toKeyword()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return domain.Keyword{}, false
	}
	return k, true
}

// keywordID は {id} を読む。整数でなければ 400 を返して false。
func keywordID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID が整数ではない"})
		return 0, false
	}
	return id, true
}

func (u *UI) createKeyword(w http.ResponseWriter, r *http.Request) {
	k, ok := readKeyword(w, r)
	if !ok {
		return
	}
	id, err := u.Keywords.CreateKeyword(r.Context(), k)
	if err != nil {
		u.keywordStoreError(w, "キーワードを作れない", err)
		return
	}
	saved, err := u.Keywords.GetKeyword(r.Context(), id)
	if err != nil {
		u.internalError(w, "キーワードを読めない", err)
		return
	}
	u.requestKeywords()
	writeJSON(w, http.StatusCreated, toKeywordJSON(saved, 0))
}

func (u *UI) updateKeyword(w http.ResponseWriter, r *http.Request) {
	id, ok := keywordID(w, r)
	if !ok {
		return
	}
	k, ok := readKeyword(w, r)
	if !ok {
		return
	}
	k.ID = id
	if err := u.Keywords.UpdateKeyword(r.Context(), k); err != nil {
		u.keywordStoreError(w, "キーワードを保存できない", err)
		return
	}
	saved, err := u.Keywords.GetKeyword(r.Context(), id)
	if err != nil {
		u.keywordStoreError(w, "キーワードを読めない", err)
		return
	}
	u.requestKeywords()
	writeJSON(w, http.StatusOK, toKeywordJSON(saved, u.pendingOf(r.Context(), id)))
}

func (u *UI) deleteKeyword(w http.ResponseWriter, r *http.Request) {
	id, ok := keywordID(w, r)
	if !ok {
		return
	}
	if err := u.Keywords.DeleteKeyword(r.Context(), id); err != nil {
		u.keywordStoreError(w, "キーワードを消せない", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// keywordAiringJSON は、キーワードが足した放送回の記録の JSON の形。
type keywordAiringJSON struct {
	StationID   string    `json:"stationId"`
	StationName string    `json:"stationName"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Title       string    `json:"title"`
	ShowName    string    `json:"showName"`
	State       string    `json:"state"`
}

func (u *UI) keywordAirings(w http.ResponseWriter, r *http.Request) {
	id, ok := keywordID(w, r)
	if !ok {
		return
	}
	records, err := u.Keywords.KeywordAirings(r.Context(), id)
	if err != nil {
		u.keywordStoreError(w, "キーワードの足した放送回を読めない", err)
		return
	}
	out := make([]keywordAiringJSON, len(records))
	for i, rec := range records {
		out[i] = keywordAiringJSON{StationID: rec.StationID, StationName: rec.StationName, Start: rec.Start, End: rec.End,
			Title: rec.Title, ShowName: rec.ShowName, State: rec.State}
	}
	writeJSON(w, http.StatusOK, map[string]any{"airings": out})
}

// previewKeyword は、今キーワードを作ったら当たる、まだ始まっていない放送回を返す（今日から 7 日先までの放送日。キーワードを
// 番組表に当てる範囲と同じ。internal/app/keyword.go）。照合は domain.Keyword の Matcher と InStations で、キーワードを当てるときと同じ。
// 名指しした自分のエリアの外の局も、キーワードを当てるときと同じく app.OutOfAreaKeywordStations で足して引く（取れなければ 502）。
func (u *UI) previewKeyword(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	k := domain.Keyword{Words: q.Get("words"), Exclude: q.Get("exclude")}
	for _, st := range strings.Split(q.Get("stations"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			k.Stations = append(k.Stations, st)
		}
	}
	if k.Key() == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "words が空"})
		return
	}
	matcher := k.Matcher()
	now := u.now()
	today := domain.BroadcastDayOf(now)
	days := make([]domain.BroadcastDay, 0, guideFutureDays+1)
	for i := 0; i <= guideFutureDays; i++ {
		days = append(days, today.AddDays(i))
	}
	membership := u.membership()
	// 名指しした自分のエリアの外の局を足して引くときの、局の名前と、局のエリア（どちらも分からなければ局 ID・付けない）。
	outOfArea := membership.AreaFree() && len(k.Stations) > 0 && u.Guide != nil
	names := map[string]string{}
	var areas map[string]string
	if outOfArea {
		if ss, err := u.Guide.Stations(r.Context()); err == nil {
			for _, s := range ss {
				names[s.ID] = s.Name
			}
		} else {
			u.log().Warn("局の一覧を取れないので、自分のエリアの外の局の名前を局 ID にする", "err", err)
		}
		areas = u.stationAreas(r.Context())
	}
	keywords := []domain.Keyword{k}
	// 放送日ごとの番組表を並列に引く（番組表の検索と同じ）。
	results := make([][]app.AreaStation, len(days))
	errs := make([]error, len(days))
	var wg sync.WaitGroup
	for i, d := range days {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = u.AreaGuide.AreaAirings(r.Context(), d)
			// 自分のエリアの番組表が空の放送日は、局一覧が分からないので、エリアの外の局も当てない（キーワードを当てるときと同じ）。
			if errs[i] != nil || !outOfArea || !slices.ContainsFunc(results[i], func(s app.AreaStation) bool { return len(s.Airings) > 0 }) {
				return
			}
			extra, failed := app.OutOfAreaKeywordStations(r.Context(), u.Guide, results[i], keywords, membership, d,
				func(id string) string { return cmp.Or(names[id], id) })
			if len(failed) > 0 {
				errs[i] = fmt.Errorf("名指しした自分のエリアの外の局 %s の番組表を取れない: %w", failed[0].Station, failed[0].Err)
				return
			}
			results[i] = append(slices.Clip(results[i]), extra...)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			u.areaError(w, days[i], err)
			return
		}
	}
	res, err := u.loadShows(r.Context())
	if err != nil {
		u.internalError(w, "番組を読めない", err)
		return
	}
	seen := map[string]bool{}
	hits := []guideAiringJSON{}
	for _, stations := range results {
		for _, st := range stations {
			for _, a := range st.Airings {
				if seen[a.Key()] {
					continue
				}
				seen[a.Key()] = true
				if a.Start.After(now) && k.InStations(st.ID) && matcher.Match(a) {
					v := res.view(a, st.Name)
					if a.OutOfArea && areas[st.ID] != "" {
						v.StationAreaName = radiko.AreaName(areas[st.ID])
					}
					hits = append(hits, v)
				}
			}
		}
	}
	slices.SortStableFunc(hits, func(a, b guideAiringJSON) int {
		return cmp.Or(a.Start.Compare(b.Start), cmp.Compare(a.StationID, b.StationID))
	})
	truncated := len(hits) > searchLimit
	if truncated {
		hits = hits[:searchLimit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"airings": hits, "truncated": truncated})
}
