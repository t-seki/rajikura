package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// pollInterval は、serve を読む間隔（通知は録音のループの周（1 分）ごとに送られる。ADR 0011）。
const pollInterval = time.Minute

// unreachableAfter は、何回続けて届かなければトーストを出すか。startupQuiet は、起動してからトーストを出さない長さ
// （PC の起動直後は、トレイがサービスやネットワークより先に立ち上がりうる）。
const (
	unreachableAfter = 3
	startupQuiet     = 3 * time.Minute
)

// iconState はアイコンの状態。
type iconState int

const (
	// stateNormal は待機中（届いていて、録音中でも異常ありでもない）。
	stateNormal iconState = iota
	// stateRecording は録音中（state=running のジョブがある）。
	stateRecording
	// stateTrouble は異常あり（続いているサービスの異常があるか、通知先に送れなかった通知がある）。
	stateTrouble
	// stateStopped は止まっている（rajikura の serve に届かない。その PC のサービスでも、リモートの serve でも同じ）。
	stateStopped
	// stateUnconfigured は未設定（tray.toml が無い、tray.toml を読めない（TOML の誤り、知らないキー）、トークンが無い、
	// 接続先（-url か tray.toml の url）が http(s) の URL として読めない、401 を受けた）。
	stateUnconfigured
)

// iconStates はアイコンの状態の全部（絵を作るときとテストで回す）。
var iconStates = []iconState{stateNormal, stateRecording, stateTrouble, stateStopped, stateUnconfigured}

func (s iconState) String() string {
	switch s {
	case stateNormal:
		return "待機中"
	case stateRecording:
		return "録音中"
	case stateTrouble:
		return "異常あり"
	case stateStopped:
		return "止まっている"
	case stateUnconfigured:
		return "未設定"
	}
	return fmt.Sprintf("iconState(%d)", int(s))
}

// trayStatus はアイコンとツールチップに出すもの。
type trayStatus struct {
	State   iconState
	Tooltip string
}

// toastMsg は出すトースト。Open は押したときに開く Web UI のパス（接続先の URL の後ろに付ける。空なら / ）。
type toastMsg struct {
	Title string
	Body  string
	Open  string
}

// monitor は、ポーリングの結果からアイコンの状態と出すトーストを決める（OS に依らない。時刻は呼ぶ側が渡す）。
// 並行には使わない。
type monitor struct {
	start time.Time
	// host は、今の接続先の URL のホスト（届かないときの文言に出す。setURL で渡す）。
	host string

	// baselined は、通知の id の基準（lastID）を持っているか。起動して最初に読めたとき（と、連携し直したとき）に作る。
	baselined bool
	// lastID は、前に見た通知の id のいちばん大きいもの（メモリにだけ持つ）。
	lastID int64
	// fails は、続けて届かなかった回数。downToasted は、届かないトーストを出した後で、まだ戻っていないこと。
	fails       int
	downToasted bool
	// linkToasted は、未設定のときの「連携してください」のトーストを出したか（起動してから 1 回だけ出す）。
	linkToasted bool
}

func newMonitor(start time.Time) *monitor {
	return &monitor{start: start}
}

// setURL は、今の接続先の URL を渡す（ポーリングのたびに、observe の前に呼ぶ）。届かないときのトースト・ツールチップに、
// その URL のホストを出す（その PC のサービスでも、リモートの serve でも通じる文言にする）。
func (m *monitor) setURL(baseURL string) {
	m.host = hostOf(baseURL)
}

// reset は、接続先かトークンが変わった（連携し直した）ときに呼ぶ。通知の id の基準を作り直す（別の rajikura の id と比べない）。
func (m *monitor) reset() {
	m.baselined = false
	m.lastID = 0
	m.fails = 0
	m.downToasted = false
}

// unconfigured は、serve を読む前に未設定と分かったとき（tray.toml が無い、tray.toml を読めない（TOML の誤り、知らないキー）、
// トークンが無い、接続先（-url か tray.toml の url）が http(s) の URL として読めない）の状態。serve は読まない。
// 401 を受けたときの未設定は observe が返す。
func (m *monitor) unconfigured(reason string) (trayStatus, []toastMsg) {
	return m.unconfiguredStatus(reason), m.linkToast()
}

func (m *monitor) unconfiguredStatus(reason string) trayStatus {
	return trayStatus{State: stateUnconfigured, Tooltip: "rajikura: 未設定（" + reason + "。メニューの「Web UI と連携する」で連携する）"}
}

func (m *monitor) linkToast() []toastMsg {
	if m.linkToasted {
		return nil
	}
	m.linkToasted = true
	return []toastMsg{{
		Title: "rajikura: Web UI と連携してください",
		Body:  "トレイのアイコンのメニューの「Web UI と連携する」を押し、開いた Web UI で「このトレイと連携する」を押す。",
	}}
}

// observe は 1 回のポーリングの結果（err が nil なら s が読めた）を受け取り、アイコンの状態と出すトーストを返す。
func (m *monitor) observe(now time.Time, s snapshot, err error) (trayStatus, []toastMsg) {
	if err != nil && !errors.Is(err, errUnauthorized) {
		m.fails++
		var toasts []toastMsg
		if m.fails >= unreachableAfter && !m.downToasted && now.Sub(m.start) >= startupQuiet {
			m.downToasted = true
			toasts = append(toasts, toastMsg{
				Title: "rajikura に届かない",
				Body:  fmt.Sprintf("%d 回続けて rajikura（%s）に届かなかった。rajikura が動いているか、ネットワークを確かめる。", m.fails, m.host),
			})
		}
		return trayStatus{State: stateStopped, Tooltip: "rajikura: 止まっている（" + m.host + " に届かない）"}, toasts
	}

	// 401 も含め、serve には届いている。
	var toasts []toastMsg
	m.fails = 0
	if m.downToasted {
		m.downToasted = false
		toasts = append(toasts, toastMsg{Title: "rajikura につながった", Body: "rajikura（" + m.host + "）に、また届くようになった。"})
	}
	if errors.Is(err, errUnauthorized) {
		return m.unconfiguredStatus("API トークンが受け付けられない"), append(toasts, m.linkToast()...)
	}

	toasts = append(toasts, m.newNotifications(s)...)
	return statusOf(s), toasts
}

// newNotifications は、前に見た id より新しい通知を、古い順にトーストにする。押したときに開くのは、通知の link があれば
// その画面（通知の id を notification= のクエリで添える。Web UI がその 1 件を既読にする）、無ければ通知の画面（/notifications）。基準が無ければ（起動して最初に読めたとき）、1 件ずつは出さず、
// 未読が 1 件以上なら「未読 N 件」の 1 枚にまとめる（押すと通知の画面）。
func (m *monitor) newNotifications(s snapshot) []toastMsg {
	if !m.baselined {
		m.baselined = true
		m.lastID = maxID(s.Notifications)
		if s.Unread > 0 {
			return []toastMsg{{
				Title: fmt.Sprintf("rajikura: 未読 %d 件", s.Unread),
				Body:  "未読の通知がある。押すと Web UI の通知の画面を開く。",
				Open:  "/notifications",
			}}
		}
		return nil
	}
	var fresh []notification
	for _, n := range s.Notifications {
		if n.ID > m.lastID {
			fresh = append(fresh, n)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	toasts := make([]toastMsg, 0, len(fresh))
	for _, n := range fresh {
		// 押したら、通知が指す画面（link）を開く。指す先が無ければ通知の画面を開く。
		// link のあるトーストは、開いた Web UI がその 1 件を既読にできるよう、通知の id をクエリで渡す。
		open := "/notifications"
		if n.Link != "" {
			open = withNotificationQuery(n.Link, n.ID)
		}
		toasts = append(toasts, toastMsg{Title: "rajikura", Body: n.Message, Open: open})
		m.lastID = n.ID
	}
	return toasts
}

// withNotificationQuery は、通知 link の後ろに notification=<通知の id> のクエリを足す（link に ? が無ければ ?、あれば &）。
func withNotificationQuery(link string, id int64) string {
	sep := "?"
	if strings.Contains(link, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%snotification=%d", link, sep, id)
}

func maxID(ns []notification) int64 {
	var id int64
	for _, n := range ns {
		if n.ID > id {
			id = n.ID
		}
	}
	return id
}

// statusOf は、読めた状態からアイコンの状態を決める。異常あり、録音中、待機中の順に見る。
func statusOf(s snapshot) trayStatus {
	var parts []string
	if n := len(s.Anomalies); n > 0 {
		parts = append(parts, fmt.Sprintf("サービスの異常 %d 件", n))
	}
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("送れなかった通知 %d 件", s.Failed))
	}
	if s.Running > 0 {
		parts = append(parts, fmt.Sprintf("録音中 %d 件", s.Running))
	}
	if s.Unread > 0 {
		parts = append(parts, fmt.Sprintf("未読 %d 件", s.Unread))
	}
	st := stateNormal
	switch {
	case len(s.Anomalies) > 0 || s.Failed > 0:
		st = stateTrouble
	case s.Running > 0:
		st = stateRecording
	}
	tip := "rajikura: " + st.String()
	if len(parts) > 0 {
		tip += "（" + strings.Join(parts, "、") + "）"
	}
	return trayStatus{State: st, Tooltip: tip}
}

// tooltipMax は、ツールチップに入る UTF-16 の長さ（NOTIFYICONDATA の szTip は終端を含めて 128）。
const tooltipMax = 127

// clipTooltip は、ツールチップを tooltipMax に収まるよう切る（収まらなければ末尾を … にする）。
func clipTooltip(s string) string {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > tooltipMax-1 {
			return s[:i] + "…"
		}
		n += w
	}
	return s
}
