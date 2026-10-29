package server

// 番組表の API（Web UI の番組表の画面が呼ぶ）。どれもセッションが要る。
//
//   - GET /api/guide?day=YYYY-MM-DD: 放送日 day の、エリアの全局の放送回。局ごとに {id, name, airings} を返す
//   - GET /api/guide/search?q=: 表示する範囲の放送日すべての番組表から、番組名・出演者・説明を見る、空白区切りの語の AND
//     （domain.KeywordMatcher。domain.NormalizeTitle で正規化してから部分一致を見る）に当たる放送回を、開始時刻の順に返す（{q, airings, truncated}）。q が正規化して空なら 400。
//     一致が多すぎるときは先頭の searchLimit 件にして truncated を true にする
//
// 表示する範囲は、今日の 7 日前から 7 日先までの放送日（非会員のタイムフリーの録音期限）。範囲の外の放送日は 400。
// エリアは認証（auth2）で radiko が判定したもので、そのエリアの全局を返す。エリアが分からない・radiko から番組表を
// 取れないときは 502。番組表は app.AreaGuide が放送日ごとにメモリに覚える（番組表の同期とは共有しない）。
//
// 放送回ごとに次を付ける。
//
//   - state: none（予約なし）、planned（録音予定。ジョブが waiting / running / retrying、またはジョブが無くても
//     有効な番組の予約で録る）、done（録音済み）、missed（録音できなかった。ジョブが expired）、skip_planned（録らない予定。
//     ジョブが確定の前の skipped、またはジョブが無くても有効な番組の予約で録らないにする放送の前の放送回）、skipped
//     （録らなかった。ジョブが確定した skipped、またはジョブが無くても有効な番組の予約で録らないにする放送の後の放送回）。
//     ジョブが無い放送回の放送の前か後かは、今日より前の放送日なら後、ほかはその放送回だけのジョブの最初の試行の時刻
//     （domain.JobFirstAttemptAt。タイムフリーの対象外の放送回（ライブで録る）は放送の開始の 1 分前、それ以外は、局の
//     tf_max_delay を知らないので最も短い遅れで数える）の前か後かで決める（展開が録らない予定を確定する時刻と揃える）。
//     ジョブとの照合は放送回の Key（局 + 開始時刻）で行う。ジョブが無い放送回は、展開と同じ当たり方（domain.Show.Hit）で
//     決める。今日より前の放送日では、1 回の予約と、開始日を持つ毎週の予約（録音期限の内側）だけが当たる。
//     無効な番組の毎週と 1 回の予約は当たらない
//   - showName: ジョブか、当たっている番組の名前。state が none なら付けない
//   - jobId: 放送回のジョブの ID。ジョブがあるときだけ付ける（番組表の詳細の「録らない」「録る」が使う。ADR 0019）
//   - skipBy: state が skip_planned・skipped のとき、誰が録らないと決めたか（title: rajikura が番組名の食い違いで、person: 人が）
//   - skippable: 「録らない」（POST /api/jobs/{id}/skip）を受け付けるか（ジョブが、試行がまだ 1 度も無い waiting）
//   - notAvailable: タイムフリーの対象外（ts_in_ng が立っている。domain.Airing.TimeFreeExcluded）。この放送回はライブで録る
//     （画面は「ライブで録る」と出す）。ほかの放送回と同じく、1 回の予約でも予約できる

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

const (
	// guidePastDays と guideFutureDays は、番組表の API が答える放送日の範囲（今日から数える）。
	guidePastDays   = 7
	guideFutureDays = 7
	// searchLimit は検索が返す放送回の数の上限。
	searchLimit = 500
)

// 放送回の状態。
const (
	stateNone        = "none"
	statePlanned     = "planned"
	stateDone        = "done"
	stateMissed      = "missed"
	stateSkipPlanned = "skip_planned"
	stateSkipped     = "skipped"
)

// 録らないと決めたのは誰か（guideAiringJSON.SkipBy）。
const (
	skipByTitle  = "title"
	skipByPerson = "person"
)

// registerGuide は番組表の API を mux に載せる。AreaGuide・Shows・Jobs のどれかが nil なら載せない。
func (u *UI) registerGuide(mux *http.ServeMux) {
	if u.AreaGuide == nil || u.Shows == nil || u.Jobs == nil {
		return
	}
	mux.HandleFunc("GET /api/guide", u.guard(u.authed(u.guideDay)))
	mux.HandleFunc("GET /api/guide/search", u.guard(u.authed(u.guideSearch)))
}

// descriptionPartJSON は説明の部品。Href はリンクの部品だけ。
type descriptionPartJSON struct {
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

type guideAiringJSON struct {
	StationID   string    `json:"stationId"`
	StationName string    `json:"stationName"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Title       string    `json:"title"`
	Performer   string    `json:"performer"`
	Description string    `json:"description"`
	// DescriptionParts は説明を、リンクの部品とそうでない部品に分けた並び（text をつなぐと description になる）。
	DescriptionParts []descriptionPartJSON `json:"descriptionParts"`
	ImageURL         string                `json:"imageUrl,omitempty"`
	State            string                `json:"state"`
	ShowName         string                `json:"showName,omitempty"`
	JobID            int64                 `json:"jobId,omitempty"`
	SkipBy           string                `json:"skipBy,omitempty"`
	Skippable        bool                  `json:"skippable"`
	NotAvailable     bool                  `json:"notAvailable"`
}

type guideStationJSON struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Airings []guideAiringJSON `json:"airings"`
}

// guideRange は番組表の API が答える放送日の範囲（両端を含む）。
func (u *UI) guideRange() (first, last domain.BroadcastDay) {
	today := domain.BroadcastDayOf(u.now())
	return today.AddDays(-guidePastDays), today.AddDays(guideFutureDays)
}

// shows は放送回に状態を付けるための、ジョブと番組。
type shows struct {
	// jobs は放送回の Key → そのジョブ（domain.JobsByAiring の形）。
	jobs  map[string]domain.Job
	shows []domain.Show
	// now は今の時刻、membership は会員種別（domain.Show.Hit に渡す）。
	now        time.Time
	membership domain.Membership
}

func (u *UI) loadShows(ctx context.Context) (shows, error) {
	rs, err := u.Shows.ListShows(ctx)
	if err != nil {
		return shows{}, err
	}
	jobs, err := u.Jobs.ListJobsFromDay(ctx, u.visibleFromDay())
	if err != nil {
		return shows{}, err
	}
	return shows{jobs: domain.JobsByAiring(jobs), shows: rs, now: u.now(), membership: u.membership()}, nil
}

// view は放送回 a の状態を決めて JSON の形にする。
func (r shows) view(a domain.Airing, stationName string) guideAiringJSON {
	out := guideAiringJSON{
		StationID: a.StationID, StationName: stationName, Start: a.Start, End: a.End, Title: a.Title,
		Performer: a.Performer, Description: a.Description, ImageURL: a.ImageURL,
		State: stateNone, NotAvailable: a.TimeFreeExcluded(),
	}
	out.DescriptionParts = make([]descriptionPartJSON, 0, len(a.DescriptionParts))
	for _, p := range a.DescriptionParts {
		out.DescriptionParts = append(out.DescriptionParts, descriptionPartJSON{Text: p.Text, Href: p.Href})
	}
	if j, ok := r.jobs[a.Key()]; ok {
		out.ShowName = j.ShowName
		out.JobID = j.ID
		switch j.State {
		case domain.StateDone:
			out.State = stateDone
		case domain.StateExpired:
			out.State = stateMissed
		case domain.StateSkipped:
			out.State = stateSkipPlanned
			if j.Settled {
				out.State = stateSkipped
			}
			out.SkipBy = skipByTitle
			if j.ByPerson {
				out.SkipBy = skipByPerson
			}
		default:
			out.State = statePlanned
			out.Skippable = j.State == domain.StateWaiting && j.Attempts == 0
		}
		return out
	}
	// ジョブが無い放送回は、展開と同じ当たり方で決める（録るなら録音予定、録らないにするなら放送の前は録らない予定、後は録らなかった）。
	for _, s := range r.shows {
		switch s.Hit(a, r.now, r.membership) {
		case domain.HitRecord:
			out.State = statePlanned
		case domain.HitSkip:
			out.State, out.SkipBy = stateSkipPlanned, skipByTitle
			// 最初の試行の時刻は、局の tf_max_delay を最も短い遅れ（0）として数える。
			if a.Day().Before(domain.BroadcastDayOf(r.now)) || !r.now.Before(domain.JobFirstAttemptAt([]domain.Airing{a}, 0)) {
				out.State = stateSkipped
			}
		default:
			continue
		}
		out.ShowName = s.Name
		break
	}
	return out
}

func (u *UI) areaError(w http.ResponseWriter, day domain.BroadcastDay, err error) {
	u.log().Warn("エリアの番組表を取れない", "day", day, "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "radiko から番組表を取れない"})
}

func (u *UI) guideDay(w http.ResponseWriter, r *http.Request) {
	day, err := domain.ParseBroadcastDay(r.URL.Query().Get("day"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "day は YYYY-MM-DD の形で指定する"})
		return
	}
	first, last := u.guideRange()
	if day.String() < first.String() || day.String() > last.String() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "day は " + first.String() + " から " + last.String() + " の範囲で指定する"})
		return
	}
	stations, err := u.AreaGuide.AreaAirings(r.Context(), day)
	if err != nil {
		u.areaError(w, day, err)
		return
	}
	res, err := u.loadShows(r.Context())
	if err != nil {
		u.internalError(w, "番組を読めない", err)
		return
	}
	out := make([]guideStationJSON, 0, len(stations))
	for _, st := range stations {
		sj := guideStationJSON{ID: st.ID, Name: st.Name, Airings: make([]guideAiringJSON, 0, len(st.Airings))}
		for _, a := range st.Airings {
			sj.Airings = append(sj.Airings, res.view(a, st.Name))
		}
		out = append(out, sj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"day": day.String(), "stations": out})
}

func (u *UI) guideSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	matcher := domain.NewKeywordMatcher(q, "")
	if matcher.Empty() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q が空"})
		return
	}
	first, last := u.guideRange()
	var days []domain.BroadcastDay
	for d := first; d.String() <= last.String(); d = d.AddDays(1) {
		days = append(days, d)
	}
	// 放送日ごとの番組表を並列に引く（覚えていないものは radiko に取りにいく）。
	results := make([][]app.AreaStation, len(days))
	errs := make([]error, len(days))
	var wg sync.WaitGroup
	for i, d := range days {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = u.AreaGuide.AreaAirings(r.Context(), d)
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
	// 放送日をまたぐ放送回（深夜）が隣の日の番組表にも出うるので、Key で重ねを除く。
	seen := map[string]bool{}
	var hits []guideAiringJSON
	for _, stations := range results {
		for _, st := range stations {
			for _, a := range st.Airings {
				if seen[a.Key()] {
					continue
				}
				seen[a.Key()] = true
				if matcher.Match(a) {
					hits = append(hits, res.view(a, st.Name))
				}
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if c := hits[a].Start.Compare(hits[b].Start); c != 0 {
			return c < 0
		}
		return hits[a].StationID < hits[b].StationID
	})
	truncated := len(hits) > searchLimit
	if truncated {
		hits = hits[:searchLimit]
	}
	if hits == nil {
		hits = []guideAiringJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"q": q, "airings": hits, "truncated": truncated})
}
