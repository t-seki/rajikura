package server

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// linksOf は GET /api/notifications を読み、本文ごとの link を返す。link のキーが無い通知は ok が false。
func linksOf(t *testing.T, body []byte) map[string]struct {
	link string
	ok   bool
} {
	t.Helper()
	var list struct {
		Notifications []map[string]json.RawMessage `json:"notifications"`
	}
	decode(t, body, &list)
	out := map[string]struct {
		link string
		ok   bool
	}{}
	for _, n := range list.Notifications {
		var msg, link string
		if err := json.Unmarshal(n["message"], &msg); err != nil {
			t.Fatal(err)
		}
		raw, ok := n["link"]
		if ok {
			if err := json.Unmarshal(raw, &link); err != nil {
				t.Fatal(err)
			}
		}
		out[msg] = struct {
			link string
			ok   bool
		}{link, ok}
	}
	return out
}

// 通知の種類ごとに、指す画面の link が出る。指す先が無ければ link のキーごと出さない。列を足す前からある行（ジョブの id・
// 番組の名前を持たない行）は、keyword_limit なら /keywords、ほかは付けない。
func TestNotificationLinks(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		n    store.Notification
		want string // 空なら link を出さない
	}{
		{store.Notification{Kind: notify.KindRecorded, JobID: 12}, "/jobs/12"},
		{store.Notification{Kind: notify.KindMissing, JobID: 13}, "/jobs/13"},
		{store.Notification{Kind: notify.KindExpired, JobID: 14}, "/jobs/14"},
		{store.Notification{Kind: notify.KindSkipped, JobID: 15}, "/jobs/15"},
		{store.Notification{Kind: notify.KindSkipPlanned, JobID: 16}, "/jobs/16"},
		{store.Notification{Kind: notify.KindUnskipped, JobID: 17}, "/jobs/17"},
		{store.Notification{Kind: notify.KindDeadlineNear, JobID: 18}, "/jobs/18"},
		{store.Notification{Kind: notify.KindKeywordAdded, Show: "番組 a"}, "/shows/" + url.PathEscape("番組 a")},
		// `/` もパスの 1 区切りとしてエスケープする（Web UI の routeOf がデコードして名前に戻す）。
		{store.Notification{Kind: notify.KindUnmatchedDay, Show: "a/b?c#d%e"}, "/shows/a%2Fb%3Fc%23d%25e"},
		{store.Notification{Kind: notify.KindKeywordLimit}, "/keywords"},
		{store.Notification{Kind: notify.KindSync, Anomaly: notify.KindSync}, ""},
		{store.Notification{Kind: notify.KindOutOfArea, Anomaly: notify.KindOutOfArea}, ""},
		{store.Notification{Kind: notify.KindBackup, Anomaly: notify.KindBackup}, ""},
		{store.Notification{Kind: notify.KindWake, Anomaly: notify.KindWake}, ""},
		{store.Notification{Kind: notify.KindRescan, Anomaly: notify.KindRescan}, ""},
		{store.Notification{Kind: notify.KindRecovered, Anomaly: notify.KindSync}, ""},
		{store.Notification{Kind: notify.KindTest}, ""},
		// 列を足す前からある行（指す先を持たない）。
		{store.Notification{Kind: notify.KindRecorded}, ""},
		{store.Notification{Kind: notify.KindDeadlineNear}, ""},
		{store.Notification{Kind: notify.KindKeywordAdded}, ""},
		{store.Notification{Kind: notify.KindUnmatchedDay}, ""},
	}
	msgs := make([]string, len(tests))
	for i, tt := range tests {
		tt.n.Message = tt.n.Kind + "・" + string(rune('a'+i))
		tt.n.CreatedAt = at
		msgs[i] = tt.n.Message
		if _, err := st.AddNotification(ctx, tt.n); err != nil {
			t.Fatal(err)
		}
	}
	rec := do(h, "GET", "/api/notifications", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("一覧 = %d %s", rec.Code, rec.Body)
	}
	got := linksOf(t, rec.Body.Bytes())
	for i, tt := range tests {
		g := got[msgs[i]]
		if g.link != tt.want || g.ok != (tt.want != "") {
			t.Errorf("%s の link = %q（キーあり %v）、want %q", msgs[i], g.link, g.ok, tt.want)
		}
	}
}

// 番組の名前を変えた後は、その番組を指していた通知の link が新しい名前の番組を指す。
func TestNotificationLinkFollowsShowRename(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	old := domain.NewShow("古い/番組", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := st.PutShows(ctx, []domain.Show{old}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	if _, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindUnmatchedDay, Message: "当たらない日", CreatedAt: at, Show: "古い/番組"}); err != nil {
		t.Fatal(err)
	}
	if got := linksOf(t, do(h, "GET", "/api/notifications", "", cookie).Body.Bytes())["当たらない日"]; got.link != "/shows/%E5%8F%A4%E3%81%84%2F%E7%95%AA%E7%B5%84" {
		t.Fatalf("名前を変える前の link = %+v", got)
	}
	renamed := domain.NewShow("新しい 番組", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := st.ReplaceShow(ctx, "古い/番組", renamed); err != nil {
		t.Fatal(err)
	}
	if got := linksOf(t, do(h, "GET", "/api/notifications", "", cookie).Body.Bytes())["当たらない日"]; got.link != "/shows/"+url.PathEscape("新しい 番組") {
		t.Fatalf("名前を変えた後の link = %+v", got)
	}
}
