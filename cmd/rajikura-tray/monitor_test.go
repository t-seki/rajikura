package main

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

var errDown = errors.New("connection refused")

func titles(ts []toastMsg) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}

func TestMonitorSummarizesUnreadAtStartup(t *testing.T) {
	start := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	m := newMonitor(start)
	s := snapshot{Notifications: []notification{{ID: 7, Message: "b"}, {ID: 5, Message: "a"}}, Unread: 2}
	_, ts := m.observe(start, s, nil)
	if len(ts) != 1 || ts[0].Title != "rajikura: 未読 2 件" || ts[0].Open != "/notifications" {
		t.Fatalf("起動の時点の未読は 1 枚にまとめる: %+v", ts)
	}
	// 同じ通知を読み直しても出さない。
	if _, ts := m.observe(start.Add(time.Minute), s, nil); len(ts) != 0 {
		t.Fatalf("前に見た通知を出し直した: %+v", ts)
	}
}

func TestMonitorNoSummaryWhenNoUnread(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	_, ts := m.observe(start, snapshot{Notifications: []notification{{ID: 3}}, Unread: 0}, nil)
	if len(ts) != 0 {
		t.Fatalf("未読が 0 なら出さない: %+v", ts)
	}
}

func TestMonitorToastsNewNotificationsOldestFirst(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	m.observe(start, snapshot{Notifications: []notification{{ID: 5}}}, nil)
	s := snapshot{Notifications: []notification{
		{ID: 8, Kind: "backup", Message: "三つ目"},
		{ID: 7, Kind: "sync", Message: "二つ目"},
		{ID: 6, Kind: "done", Message: "一つ目"},
		{ID: 5, Message: "前に見た"},
	}, Unread: 4}
	_, ts := m.observe(start.Add(time.Minute), s, nil)
	if len(ts) != 3 {
		t.Fatalf("新しい通知は種類を問わず全部出す: %+v", ts)
	}
	for i, want := range []string{"一つ目", "二つ目", "三つ目"} {
		if ts[i].Body != want || ts[i].Open != "/notifications" {
			t.Errorf("%d 番目 = %+v, 本文 %q で通知の画面を開くはず", i, ts[i], want)
		}
	}
	if _, ts := m.observe(start.Add(2*time.Minute), s, nil); len(ts) != 0 {
		t.Fatalf("出した通知を出し直した: %+v", ts)
	}
}

// 通知のトーストは、押すと通知の link の画面を開き、link が無ければ通知の画面を開く。起動して最初の「未読 N 件」は、
// link のある通知が溜まっていても通知の画面を開く。
func TestMonitorToastOpensNotificationLink(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	_, ts := m.observe(start, snapshot{Notifications: []notification{{ID: 5, Message: "前に見た", Link: "/jobs/1"}}, Unread: 1}, nil)
	if len(ts) != 1 || ts[0].Open != "/notifications" {
		t.Fatalf("「未読 N 件」は通知の画面を開くはず: %+v", ts)
	}
	s := snapshot{Notifications: []notification{
		{ID: 9, Kind: "sync", Message: "異常"},
		{ID: 8, Kind: "keyword_limit", Message: "上限", Link: "/keywords"},
		{ID: 7, Kind: "unmatched_day", Message: "当たらない日", Link: "/shows/a%2Fb"},
		{ID: 6, Kind: "expired", Message: "録音できなかった", Link: "/jobs/12"},
		{ID: 5, Message: "前に見た", Link: "/jobs/1"},
	}, Unread: 5}
	_, ts = m.observe(start.Add(time.Minute), s, nil)
	want := []struct{ body, open string }{
		{"録音できなかった", "/jobs/12?notification=6"},
		{"当たらない日", "/shows/a%2Fb?notification=7"},
		{"上限", "/keywords?notification=8"},
		{"異常", "/notifications"},
	}
	if len(ts) != len(want) {
		t.Fatalf("トースト = %+v", ts)
	}
	for i, w := range want {
		if ts[i].Body != w.body || ts[i].Open != w.open {
			t.Errorf("%d 番目 = %+v, want 本文 %q で %s を開く", i, ts[i], w.body, w.open)
		}
	}
}

// link に既にクエリがあれば & でつなぐ。
func TestWithNotificationQuery(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"/jobs/12", "/jobs/12?notification=345"},
		{"/shows/%E6%9C%9D", "/shows/%E6%9C%9D?notification=345"},
		{"/keywords", "/keywords?notification=345"},
		{"/guide?day=1", "/guide?day=1&notification=345"},
	} {
		if got := withNotificationQuery(c.link, 345); got != c.want {
			t.Errorf("withNotificationQuery(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

func TestMonitorUnreachableToastAfterThreeFailures(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	m.observe(start, snapshot{}, nil)
	now := start.Add(10 * time.Minute)
	for i := 1; i <= 2; i++ {
		st, ts := m.observe(now, snapshot{}, errDown)
		if st.State != stateStopped {
			t.Fatalf("届かなければ止まっている: %v", st.State)
		}
		if len(ts) != 0 {
			t.Fatalf("%d 回目で出した: %+v", i, ts)
		}
		now = now.Add(time.Minute)
	}
	_, ts := m.observe(now, snapshot{}, errDown)
	if got := titles(ts); len(got) != 1 || got[0] != "rajikura に届かない" {
		t.Fatalf("3 回続けて届かなければ 1 回出す: %v", got)
	}
	if _, ts := m.observe(now.Add(time.Minute), snapshot{}, errDown); len(ts) != 0 {
		t.Fatalf("届かないトーストは 1 回だけ: %+v", ts)
	}
	_, ts = m.observe(now.Add(2*time.Minute), snapshot{}, nil)
	if got := titles(ts); len(got) != 1 || got[0] != "rajikura につながった" {
		t.Fatalf("戻ったら出す: %v", got)
	}
}

// 届かない・つながったのトーストとツールチップは、その PC のサービスを前提にせず、接続先の URL のホストを出す。
func TestMonitorUnreachableWordingNamesHost(t *testing.T) {
	for _, c := range []struct{ url, host string }{
		{"https://rajikura.example.com", "rajikura.example.com"},
		{"http://127.0.0.1:8080", "127.0.0.1"},
	} {
		t.Run(c.host, func(t *testing.T) {
			start := time.Now()
			m := newMonitor(start)
			m.setURL(c.url)
			now := start.Add(10 * time.Minute)
			var st trayStatus
			var ts []toastMsg
			for i := 0; i < unreachableAfter; i++ {
				st, ts = m.observe(now, snapshot{}, errDown)
			}
			if want := "rajikura: 止まっている（" + c.host + " に届かない）"; st.Tooltip != want {
				t.Errorf("ツールチップ = %q, want %q", st.Tooltip, want)
			}
			if want := "3 回続けて rajikura（" + c.host + "）に届かなかった。rajikura が動いているか、ネットワークを確かめる。"; len(ts) != 1 || ts[0].Body != want {
				t.Errorf("届かないトースト = %+v, want 本文 %q", ts, want)
			}
			_, ts = m.observe(now.Add(time.Minute), snapshot{}, nil)
			if want := "rajikura（" + c.host + "）に、また届くようになった。"; len(ts) != 1 || ts[0].Body != want {
				t.Errorf("つながったトースト = %+v, want 本文 %q", ts, want)
			}
			// アイコンの状態の名前「止まっている」は変えない。
			if st.State.String() != "止まっている" {
				t.Errorf("状態の名前 = %q", st.State)
			}
		})
	}
}

func TestMonitorFailureCountResetsOnSuccess(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	now := start.Add(10 * time.Minute)
	m.observe(now, snapshot{}, errDown)
	m.observe(now, snapshot{}, errDown)
	m.observe(now, snapshot{}, nil)
	if _, ts := m.observe(now, snapshot{}, errDown); len(ts) != 0 {
		t.Fatalf("続けてでなければ出さない: %+v", ts)
	}
}

func TestMonitorQuietForFirstThreeMinutes(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	for i := 0; i < 3; i++ {
		if _, ts := m.observe(start.Add(time.Duration(i)*time.Minute), snapshot{}, errDown); len(ts) != 0 {
			t.Fatalf("起動して最初の 3 分は出さない（%d 分）: %+v", i, ts)
		}
	}
	// 3 分を過ぎてもまだ届かなければ出す。
	_, ts := m.observe(start.Add(3*time.Minute), snapshot{}, errDown)
	if got := titles(ts); len(got) != 1 || got[0] != "rajikura に届かない" {
		t.Fatalf("3 分を過ぎたら出す: %v", got)
	}
}

func TestMonitorNoReconnectToastWithoutDownToast(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	m.observe(start, snapshot{}, errDown)
	m.observe(start.Add(time.Minute), snapshot{}, errDown)
	m.observe(start.Add(2*time.Minute), snapshot{}, errDown)
	if _, ts := m.observe(start.Add(3*time.Minute), snapshot{}, nil); len(ts) != 0 {
		t.Fatalf("届かないトーストを出していなければ、つながったも出さない: %+v", ts)
	}
}

func TestMonitorUnauthorizedIsUnconfigured(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	st, ts := m.observe(start, snapshot{}, errUnauthorized)
	if st.State != stateUnconfigured {
		t.Fatalf("401 は未設定: %v", st.State)
	}
	if got := titles(ts); len(got) != 1 || !strings.Contains(got[0], "連携") {
		t.Fatalf("未設定の初回は連携のトーストを出す: %v", got)
	}
	if _, ts := m.observe(start.Add(time.Minute), snapshot{}, errUnauthorized); len(ts) != 0 {
		t.Fatalf("連携のトーストは初回だけ: %+v", ts)
	}
	if _, ts := m.unconfigured("API トークンが無い"); len(ts) != 0 {
		t.Fatalf("連携のトーストは初回だけ: %+v", ts)
	}
}

func TestMonitorUnconfiguredOnce(t *testing.T) {
	m := newMonitor(time.Now())
	st, ts := m.unconfigured("API トークンが無い")
	if st.State != stateUnconfigured || len(ts) != 1 {
		t.Fatalf("未設定の初回: %v %+v", st.State, ts)
	}
	if _, ts := m.unconfigured("API トークンが無い"); len(ts) != 0 {
		t.Fatalf("2 回目は出さない: %+v", ts)
	}
}

func TestMonitorResetRebaselines(t *testing.T) {
	start := time.Now()
	m := newMonitor(start)
	m.observe(start, snapshot{Notifications: []notification{{ID: 50}}}, nil)
	m.reset()
	// 連携し直した先の id は前の基準と比べない（1 件ずつは出さず、未読をまとめる）。
	_, ts := m.observe(start.Add(time.Minute), snapshot{Notifications: []notification{{ID: 3}}, Unread: 1}, nil)
	if got := titles(ts); len(got) != 1 || got[0] != "rajikura: 未読 1 件" {
		t.Fatalf("基準を作り直す: %v", got)
	}
}

func TestStatusOf(t *testing.T) {
	cases := []struct {
		name string
		s    snapshot
		want iconState
	}{
		{"待機中", snapshot{}, stateNormal},
		{"録音中", snapshot{Running: 1}, stateRecording},
		{"異常あり（サービスの異常）", snapshot{Anomalies: []anomaly{{Kind: "sync"}}}, stateTrouble},
		{"異常あり（種類を問わない: wake）", snapshot{Anomalies: []anomaly{{Kind: "wake"}}}, stateTrouble},
		{"異常あり（種類を問わない: rescan）", snapshot{Anomalies: []anomaly{{Kind: "rescan"}}}, stateTrouble},
		{"異常あり（送れなかった通知）", snapshot{Failed: 1}, stateTrouble},
		{"異常ありは録音中より先", snapshot{Failed: 1, Running: 2}, stateTrouble},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := statusOf(c.s)
			if st.State != c.want {
				t.Fatalf("状態 = %v, want %v", st.State, c.want)
			}
			if !strings.Contains(st.Tooltip, c.want.String()) {
				t.Errorf("ツールチップに状態が無い: %q", st.Tooltip)
			}
		})
	}
}

func TestIconStatesAreFive(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range iconStates {
		seen[s.String()] = true
	}
	for _, name := range []string{"待機中", "録音中", "異常あり", "止まっている", "未設定"} {
		if !seen[name] {
			t.Errorf("状態 %s が無い", name)
		}
	}
	if len(iconStates) != 5 {
		t.Errorf("状態は 5 つ: %d", len(iconStates))
	}
}

func TestClipTooltip(t *testing.T) {
	short := "rajikura: 待機中"
	if clipTooltip(short) != short {
		t.Fatal("収まるものは切らない")
	}
	long := strings.Repeat("あ", 200)
	got := clipTooltip(long)
	if n := len(utf16.Encode([]rune(got))); n > tooltipMax {
		t.Fatalf("UTF-16 で %d（%d まで）", n, tooltipMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("切ったら … で終える: %q", got)
	}
}
