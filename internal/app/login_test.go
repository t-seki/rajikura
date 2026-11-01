package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/secret"
)

// useMember は、偽の radiko を使う radiko.Client を会員（ログイン情報あり）にし、ログインの結果をサービスの異常につなぐ（cmd と同じ）。
func (e *env) useMember(areaFree bool) *radiko.Client {
	e.t.Helper()
	c := e.app.Fetcher.(*radiko.Client)
	c.Account = func() (radiko.Account, bool) {
		return radiko.Account{Mail: "me@example.com", Password: "pass", AreaFree: areaFree}, true
	}
	c.OnLogin = e.app.LoginResult
	// LoginResult は、結果が届いた時点の会員種別が非会員なら始まりを書かないので、会員にしておく。
	e.app.Membership = domain.MembershipTimeFree30
	if areaFree {
		e.app.Membership = domain.MembershipAreaFree
	}
	return c
}

// radiko がログインを拒んだら、非会員として認証して録り続け、サービスの異常（radiko のログインの失敗）の始まりを書く。
// ログインが通ったら（保存してセッションを捨てた後）、復旧を書く。
func TestLoginRejectedRecordsAsFreeAndNotifies(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	c := e.useMember(true)
	e.fake.set(func(f *fakeRadiko) { f.loginReject = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	e.checkNotifications()
	got := d.take()
	if len(got) != 2 || !strings.Contains(got[0], "【radiko のログインの失敗】") || !strings.Contains(got[0], "拒んだ") ||
		!strings.Contains(got[1], "【録音済み】") {
		t.Fatalf("通知 = %q", got)
	}
	if ongoing, err := e.store.AnomalyOngoing(e.ctx, notify.KindLogin); err != nil || !ongoing {
		t.Fatalf("radiko のログインの失敗が続いていない: %v, %v", ongoing, err)
	}
	// 続いている間は書かず、ログインし直さない（非会員として録る）。
	if !e.runDue() {
		t.Fatal("2 件目が実行されない")
	}
	e.checkNotifications()
	d.expectOne(t, "【録音済み】")
	e.fake.set(func(f *fakeRadiko) {
		if f.logins != 1 || len(f.authSessions) == 0 || f.authSessions[len(f.authSessions)-1] != "" {
			t.Errorf("logins = %d, auth2 sessions = %q; want one rejected login and non-member auth", f.logins, f.authSessions)
		}
		f.loginReject = false
	})

	// 保存したら（セッションを捨てる）、次の認証でログインし直し、通ったら復旧を書く。
	c.ResetLogin()
	if _, err := c.Authenticate(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectOne(t, "【復旧】", "radiko のログイン")
	if ongoing, err := e.store.AnomalyOngoing(e.ctx, notify.KindLogin); err != nil || ongoing {
		t.Fatalf("ongoing = %v, %v", ongoing, err)
	}
}

// ログインの応答のエリアフリーの値が、選んだ会員種別と食い違うときも、radiko のログインの失敗として知らせる。
func TestLoginAreaFreeMismatchNotifies(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	c := e.useMember(true)
	e.fake.set(func(f *fakeRadiko) { f.loginAreaFree = "0" })
	tok, err := c.Authenticate(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Session != "sess1" || tok.AreaFree {
		t.Fatalf("token = %+v", tok)
	}
	e.checkNotifications()
	d.expectOne(t, "【radiko のログインの失敗】", "areafree = 0")
	// ネットワークの失敗ではない、通ったログインは続けて書かない。食い違いが無くなったら復旧。
	e.fake.set(func(f *fakeRadiko) { f.loginAreaFree = "1" })
	c.ResetLogin()
	if _, err := c.Authenticate(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectOne(t, "【復旧】")
}

// radiko のログインの失敗が続いている間に会員種別を非会員にして保存したら、radiko のログインを止めたことを復旧として閉じる。
// 会員（areafree）のまま、会員種別を書かずにほかの設定を保存しても閉じない。異常が続いていなければ、非会員にしても書かない。
func TestSettingsAppliedFreeClosesLoginAnomaly(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	s := &Settings{Store: e.store, Secrets: secret.Load(filepath.Join(t.TempDir(), config.KeyFile)), Log: e.app.log(),
		Build: func(c config.Config) Runtime {
			return Runtime{Config: c, Media: e.media, Preset: library.Jellyfin{}, Notifier: e.app.Notifier}
		}}
	if err := s.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.app.Live = s.Runtime
	s.OnApply = e.app.SettingsApplied
	c := e.app.Fetcher.(*radiko.Client)
	c.Account = s.RadikoAccount
	c.OnLogin = e.app.LoginResult
	e.fake.set(func(f *fakeRadiko) { f.loginReject = true })

	// 異常が続いていないときに非会員を保存しても、復旧は書かない。
	freeM := string(domain.MembershipFree)
	if _, err := s.Apply(e.ctx, config.Patch{Membership: &freeM}); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectNone(t)

	mail, pass, m := "me@example.com", "pass", string(domain.MembershipAreaFree)
	if _, err := s.Apply(e.ctx, config.Patch{Membership: &m, Radiko: &config.RadikoPatch{Mail: &mail, Password: &pass}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectOne(t, "【radiko のログインの失敗】")

	// 会員種別を書かずに、ほかの設定だけを保存しても閉じない（会員種別は areafree のまま）。
	keep := 5
	if _, err := s.Apply(e.ctx, config.Patch{KeywordMaxPending: &keep}); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectNone(t)

	if _, err := s.Apply(e.ctx, config.Patch{Membership: &freeM}); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectOne(t, "【復旧】", "非会員")
	if ongoing, err := e.store.AnomalyOngoing(e.ctx, notify.KindLogin); err != nil || ongoing {
		t.Fatalf("ongoing = %v, %v", ongoing, err)
	}
}

// radiko のログインの要求の間に会員種別を非会員にして保存したら、後から届いた拒否・食い違いの結果では始まりを書かない。
// 会員のままなら書く。
func TestLoginResultAfterSwitchingToFree(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	m := domain.MembershipFree
	e.app.Live = func() Runtime { return Runtime{Config: config.Config{Membership: m}, Notifier: e.app.Notifier} }
	rejected := radiko.LoginEvent{Err: radiko.ErrLoginRejected}
	mismatch := radiko.LoginEvent{Mismatch: true, AreaFreeValue: "0"}
	e.app.LoginResult(rejected)
	e.app.LoginResult(mismatch)
	e.checkNotifications()
	d.expectNone(t)
	if ongoing, err := e.store.AnomalyOngoing(e.ctx, notify.KindLogin); err != nil || ongoing {
		t.Fatalf("ongoing = %v, %v; want no anomaly for free", ongoing, err)
	}
	m = domain.MembershipAreaFree
	e.app.LoginResult(rejected)
	e.checkNotifications()
	d.expectOne(t, "【radiko のログインの失敗】")
}

// 番組表の tsplus_in_ng を放送回に渡し、0 でなければタイムフリー30・ダブルプランでも録音期限が 7 日になる。
func TestToAiringsTSPlusInNG(t *testing.T) {
	as := toAirings([]radiko.Program{
		{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0), TSInNG: "0", TSPlusInNG: "1"},
		{StationID: "TBS", Start: jst(2026, 10, 5, 11, 0), End: jst(2026, 10, 5, 12, 0), TSInNG: "0", TSPlusInNG: "0"},
	})
	if as[0].TimeFree.PlusInNG != 1 || as[1].TimeFree.PlusInNG != 0 {
		t.Fatalf("airings = %+v", as)
	}
	for _, m := range []domain.Membership{domain.MembershipTimeFree30, domain.MembershipDouble} {
		if got, want := domain.AiringDeadline(as[0], m), jst(2026, 10, 13, 5, 0); !got.Equal(want) {
			t.Errorf("%s: tsplus_in_ng の放送回の録音期限 = %v, want %v（7 日）", m, got, want)
		}
		if got, want := domain.AiringDeadline(as[1], m), jst(2026, 11, 5, 5, 0); !got.Equal(want) {
			t.Errorf("%s: 録音期限 = %v, want %v（30 日）", m, got, want)
		}
	}
}

// 会員種別を保存したら再起動なしで効き、まだ終わっていないジョブの録音期限が付け直され、radiko のログインのセッションを捨てる。
func TestSettingsAppliedMembership(t *testing.T) {
	e := newFlowEnv(t)
	s := &Settings{Store: e.store, Secrets: secret.Load(filepath.Join(t.TempDir(), config.KeyFile)), Log: e.app.log(),
		Build: func(c config.Config) Runtime { return Runtime{Config: c, Media: e.media, Preset: library.Jellyfin{}} }}
	if err := s.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.app.Live = s.Runtime
	s.OnApply = e.app.SettingsApplied
	c := e.app.Fetcher.(*radiko.Client)
	c.Account = s.RadikoAccount
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	j := e.job(flatShow.Name)
	free := domain.JobDeadline(j.Airings, domain.MembershipFree)
	if !j.Deadline.Equal(free) {
		t.Fatalf("deadline = %v, want %v", j.Deadline, free)
	}
	// 非会員: ログインしない。
	if tok, err := c.Authenticate(e.ctx); err != nil || tok.Session != "" {
		t.Fatalf("token = %+v, %v", tok, err)
	}

	mail, pass, m := "me@example.com", "pass", string(domain.MembershipTimeFree30)
	if _, err := s.Apply(e.ctx, config.Patch{Membership: &m, Radiko: &config.RadikoPatch{Mail: &mail, Password: &pass}}); err != nil {
		t.Fatal(err)
	}
	if got := e.app.membership(); got != domain.MembershipTimeFree30 {
		t.Fatalf("membership = %s, want timefree30（保存したらすぐ効く）", got)
	}
	thirty := domain.JobDeadline(j.Airings, domain.MembershipTimeFree30)
	if j := e.job(flatShow.Name); !j.Deadline.Equal(thirty) || thirty.Equal(free) {
		t.Fatalf("deadline = %v, want %v（30 日に付け直す）", j.Deadline, thirty)
	}
	// 会員: 次の認証でログインする。
	tok, err := c.Authenticate(e.ctx)
	if err != nil || tok.Session != "sess1" {
		t.Fatalf("token = %+v, %v", tok, err)
	}
	// ログイン情報を保存したらセッションを捨て、次の認証でログインし直す。
	pass2 := "pass2"
	if _, err := s.Apply(e.ctx, config.Patch{Radiko: &config.RadikoPatch{Password: &pass2}}); err != nil {
		t.Fatal(err)
	}
	if tok, err := c.Authenticate(e.ctx); err != nil || tok.Session != "sess2" {
		t.Fatalf("token = %+v, %v; want a new session", tok, err)
	}
	if j := e.job(flatShow.Name); !j.Deadline.Equal(thirty) {
		t.Fatalf("会員種別を変えない保存で、録音期限が変わった: %v", j.Deadline)
	}

	// 非会員に戻すと、7 日に戻り、ログインしない。
	freeM := string(domain.MembershipFree)
	if _, err := s.Apply(e.ctx, config.Patch{Membership: &freeM}); err != nil {
		t.Fatal(err)
	}
	if j := e.job(flatShow.Name); !j.Deadline.Equal(free) {
		t.Fatalf("deadline = %v, want %v", j.Deadline, free)
	}
	if tok, err := c.Authenticate(e.ctx); err != nil || tok.Session != "" {
		t.Fatalf("token = %+v, %v; want a non-member token", tok, err)
	}
	e.fake.set(func(f *fakeRadiko) {
		if f.logins != 2 {
			t.Errorf("logins = %d, want 2", f.logins)
		}
	})
}
