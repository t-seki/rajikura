package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/secret"
	"github.com/t-seki/rajikura/internal/server"
	"github.com/t-seki/rajikura/internal/store"
)

// useFakeRadiko は、serve が radiko の代わりに srv につなぐようにする（起動の設定のホストを差す。人が使うフラグは無い）。
func useFakeRadiko(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := bootHook
	bootHook = func(b *config.Boot) {
		b.RadikoAuthHost = srv.URL
		b.RadikoProgramHost = srv.URL
	}
	t.Cleanup(func() { bootHook = old })
}

func runCmd(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(ctx, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun(t *testing.T) {
	isolateClient(t)
	data := t.TempDir()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "rajikura dev", ""},
		// 一覧が埋め込まれているかに関わらず exit 0（中身は licenses_test.go で見る）。
		{"licenses", []string{"licenses"}, 0, "", ""},
		{"licenses の引数が多い", []string{"licenses", "x"}, 2, "", "使い方: rajikura licenses"},
		{"引数なし", nil, 2, "", "使い方"},
		{"不明なサブコマンド", []string{"nope"}, 2, "", "不明なサブコマンド"},
		// 0.x の -config は、入れ方の docs を添えて止める（serve の前でも後でも）。
		{"serve に -config", []string{"-config", "config.toml", "serve"}, 2, "", "-config は無くなった。データのフォルダは -data で渡し、設定は Web UI で決め直す"},
		{"serve の後に -config", []string{"serve", "-config", "config.toml"}, 2, "", "-config は無くなった"},
		{"jobs に -config", []string{"-config", "config.toml", "jobs"}, 2, "", "-config は無くなった"},
		{"serve に -url", []string{"-data", data, "-url", "http://localhost:8080", "serve"}, 2, "", "-url と -token は"},
		{"serve の前と後に -data", []string{"-data", data, "serve", "-data", data}, 2, "", "両方に書いている"},
		{"serve の引数が多い", []string{"serve", "x"}, 2, "", "使い方"},
		{"jobs に -data", []string{"-data", data, "jobs"}, 2, "", "-data と -listen と -allowed-hosts は serve だけ"},
		{"jobs に -allowed-hosts", []string{"-allowed-hosts", "rajikura.example.com", "jobs"}, 2, "", "-allowed-hosts は serve だけ"},
		{"serve の前と後に -allowed-hosts", []string{"-allowed-hosts", "a.example", "serve", "-allowed-hosts", "b.example"}, 2, "", "-allowed-hosts を serve の前と後の両方に書いている"},
		{"-allowed-hosts にポート", []string{"-data", data, "serve", "-allowed-hosts", "rajikura.example.com:443"}, 2, "", "名前だけを書く"},
		{"-allowed-hosts に URL", []string{"-data", data, "-allowed-hosts", "a.example, https://b.example", "serve"}, 2, "", `"https://b.example" は名前だけを書く`},
		{"service に -allowed-hosts", []string{"-allowed-hosts", "rajikura.example.com", "service", "install"}, 2, "", "rajikura service: -allowed-hosts は使わない"},
		// service は serve に HTTP で話さないので、-url・-token・-data を使わない（-listen は service install の後に書く）。
		{"service に -url", []string{"-url", "http://localhost:8080", "service", "install"}, 2, "", "rajikura service: -url は使わない"},
		{"service の前に -listen", []string{"-listen", "127.0.0.1:9000", "service", "install"}, 2, "", "service install -listen で渡す"},
		// tray も serve に HTTP で話さないので、tray の前に書いたフラグは使い方の誤り（-url・-token は tray install の後に書く）。
		{"tray の前に -token", []string{"-token", "X", "tray", "install"}, 2, "", "rajikura tray: -token は tray の前に書かない"},
		{"tray の前に -url", []string{"-url", "http://127.0.0.1:8080", "tray", "install"}, 2, "", "rajikura tray: -url は tray の前に書かない"},
		{"tray の前に -data", []string{"-data", data, "tray", "install"}, 2, "", "rajikura tray: -data は"},
		{"tray の前に -listen", []string{"-listen", "127.0.0.1:9000", "tray", "uninstall"}, 2, "", "rajikura tray: -listen は"},
		{"tray の前に -allowed-hosts", []string{"-allowed-hosts", "a.example", "tray", "install"}, 2, "", "rajikura tray: -allowed-hosts は"},
		// TOML による番組・設定の受け渡し（import・export・settings）は無い（ADR 0017）。知らないサブコマンドとして止める。
		{"import は無い", []string{"import", "x.toml"}, 2, "", "不明なサブコマンド \"import\""},
		{"export は無い", []string{"export"}, 2, "", "不明なサブコマンド \"export\""},
		{"settings の import は無い", []string{"settings", "import", "x.toml"}, 2, "", "不明なサブコマンド \"settings\""},
		{"settings の export は無い", []string{"settings", "export"}, 2, "", "不明なサブコマンド \"settings\""},
		// 使い方の誤りは、接続先とトークンが無くても先に知らせる。
		{"jobs の不明な状態", []string{"jobs", "-state", "nope"}, 1, "", "不明な状態"},
		{"jobs の不明なフラグ", []string{"jobs", "-x"}, 2, "", "-x"},
		{"retry の ID が無い", []string{"retry"}, 2, "", "使い方"},
		{"retry の ID が整数でない", []string{"retry", "abc"}, 2, "", "整数ではない"},
		{"接続先が無い", []string{"jobs"}, 1, "", "接続先が無い"},
		{"トークンが無い", []string{"-url", "http://localhost:1", "jobs"}, 1, "", "API トークンが無い"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runCmd(t, context.Background(), tt.args...)
			if code != tt.wantCode {
				t.Errorf("終了コード = %d, want %d（stderr: %s）", code, tt.wantCode, errOut)
			}
			if !strings.Contains(out, tt.wantStdout) {
				t.Errorf("stdout = %q, want %q を含む", out, tt.wantStdout)
			}
			if !strings.Contains(errOut, tt.wantStderr) {
				t.Errorf("stderr = %q, want %q を含む", errOut, tt.wantStderr)
			}
		})
	}
}

// 使い方に、無くなったサブコマンド（import・export・settings）が出ない。
func TestUsageWithoutTOMLTransfer(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, word := range []string{"import", "export", "settings"} {
		if strings.Contains(buf.String(), word) {
			t.Errorf("使い方に %q が出る:\n%s", word, buf.String())
		}
	}
}

// 使い方に、tray の install と uninstall が出る。
func TestUsageListsTray(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, word := range []string{"rajikura tray <install -url <URL> [-token <トークン>] | uninstall>", "  tray install ", "  tray uninstall "} {
		if !strings.Contains(buf.String(), word) {
			t.Errorf("使い方に %q が無い:\n%s", word, buf.String())
		}
	}
}

// jobs は、放送日・番組・パート・ID の順に並べ、保存先（欠落の数）と直近の試行のエラーを出す。
func TestJobsTable(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	add := func(name, day string, part int, state domain.State, output string, missing []domain.Airing) int64 {
		t.Helper()
		d, err := domain.ParseBroadcastDay(day)
		if err != nil {
			t.Fatal(err)
		}
		a := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: name}
		id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: name, Day: d, Part: part, State: state, OutputPath: output,
			Airings: []domain.Airing{a}, Missing: missing, NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	gap := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "番組B"}
	newer := add("番組B", "2026-10-11", 0, domain.StateDone, "/library/番組B.m4a", []domain.Airing{gap})
	part2 := add("番組A", "2026-10-10", 2, domain.StateWaiting, "", nil)
	part1 := add("番組A", "2026-10-10", 1, domain.StateRetrying, "", nil)
	other := add("番組C", "2026-10-10", 0, domain.StateWaiting, "", nil)
	if _, err := api.Store.RecordAttempt(ctx, domain.Attempt{JobID: part1, StartedAt: now, EndedAt: now, FailedStage: domain.StageFetch, Error: "落ちた\n2 行目"}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := api.run(t, "jobs")
	if code != 0 {
		t.Fatalf("jobs: %d %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("jobs = %s", out)
	}
	for i, id := range []int64{part1, part2, other, newer} {
		if !strings.HasPrefix(lines[i+1], strconv.FormatInt(id, 10)+" ") {
			t.Errorf("%d 行目 = %q, want ID %d", i+1, lines[i+1], id)
		}
	}
	if !strings.Contains(lines[1], "番組A (1)") || !strings.Contains(lines[1], "[fetch] 落ちた 2 行目") {
		t.Errorf("失敗したジョブの行 = %q", lines[1])
	}
	if !strings.Contains(lines[4], "/library/番組B.m4a（欠落 1）") {
		t.Errorf("保存したジョブの行 = %q", lines[4])
	}

	code, out, _ = api.run(t, "jobs", "-state", "done,expired")
	if code != 0 || strings.Count(strings.TrimSpace(out), "\n") != 1 || !strings.Contains(out, "番組B") {
		t.Errorf("jobs -state done,expired: %d %q", code, out)
	}
}

// jobs の状態の列は、ライブで録るジョブ（API の live）に「ライブ録音」を添える。skipped・人が決めたの添え書きの後に並べる。
func TestStateLabelLive(t *testing.T) {
	for _, c := range []struct {
		job  client.Job
		want string
	}{
		{client.Job{State: "waiting"}, "waiting"},
		{client.Job{State: "waiting", Live: true}, "waiting（ライブ録音）"},
		{client.Job{State: "done", Live: true}, "done（ライブ録音）"},
		{client.Job{State: "skipped", Live: true}, "skipped（録らない予定・ライブ録音）"},
		{client.Job{State: "skipped", Settled: true, ByPerson: true, Live: true}, "skipped（録らなかった・人が決めた・ライブ録音）"},
	} {
		if got := stateLabel(c.job); got != c.want {
			t.Errorf("stateLabel(%+v) = %q, want %q", c.job, got, c.want)
		}
	}
}

// jobs は、タイムフリーの対象外の放送回を含むジョブの状態に「ライブ録音」を添え、含まないジョブには添えない。
func TestJobsTableLive(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	add := func(name string, inNG int) {
		t.Helper()
		a := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: name}
		a.TimeFree.InNG = inNG
		if _, err := api.Store.InsertJob(ctx, domain.Job{ShowName: name, Day: domain.BroadcastDayOf(now), State: domain.StateWaiting,
			Airings: []domain.Airing{a}, NextAttemptAt: now.Add(3 * time.Hour), Deadline: now.Add(24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	add("ライブの番組", 2)
	add("タイムフリーの番組", 0)
	code, out, errOut := api.run(t, "jobs")
	if code != 0 {
		t.Fatalf("jobs: %d %q", code, errOut)
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "ライブの番組") && !strings.Contains(line, "waiting（ライブ録音）"):
			t.Errorf("ライブで録るジョブの行 = %q", line)
		case strings.Contains(line, "タイムフリーの番組") && strings.Contains(line, "ライブ録音"):
			t.Errorf("タイムフリーから録るジョブの行 = %q", line)
		}
	}
	if !strings.Contains(out, "ライブの番組") || !strings.Contains(out, "タイムフリーの番組") {
		t.Errorf("jobs = %s", out)
	}
}

// retry: waiting と無い ID は拒むがほかの ID は続け、終了コードは 1。expired は retrying・試行回数 0 になる。
// 録音期限を過ぎたジョブは、retrying でも拒む。
func TestRetry(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	a := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "テスト番組"}
	id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: "テスト番組", Day: domain.BroadcastDayOf(now), State: domain.StateWaiting,
		Airings: []domain.Airing{a}, NextAttemptAt: now, Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	idArg := strconv.FormatInt(id, 10)
	code, out, stderr := api.run(t, "retry", idArg, "99999")
	if code != 1 || !strings.Contains(out, "状態が waiting") || !strings.Contains(out, "ジョブ 99999: 拒んだ（ID が無い）") {
		t.Fatalf("retry (waiting, 無い ID): %d %q %q", code, out, stderr)
	}
	if _, ok, err := api.Store.ClaimDue(ctx, now); err != nil || !ok {
		t.Fatalf("ClaimDue: %v, %v", ok, err)
	}
	if err := api.Store.Expire(ctx, id); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = api.run(t, "retry", "99999", idArg)
	if code != 1 || !strings.Contains(out, fmt.Sprintf("ジョブ %d: 再試行にした", id)) {
		t.Fatalf("retry (無い ID, expired): %d %q %q", code, out, stderr)
	}
	j, err := api.Store.GetJob(ctx, id)
	if err != nil || j.State != domain.StateRetrying || j.Attempts != 0 || time.Since(j.NextAttemptAt) > time.Minute {
		t.Errorf("retry の後: %+v, %v", j, err)
	}
	raw, err := sql.Open("sqlite", api.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := raw.Exec(`UPDATE jobs SET deadline = ? WHERE id = ?`, past, id); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = api.run(t, "retry", idArg)
	if code != 1 || !strings.Contains(out, fmt.Sprintf("ジョブ %d: 拒んだ（録音期限を過ぎている。", id)) {
		t.Fatalf("retry (録音期限切れ): %d %q %q", code, out, stderr)
	}
}

// skip は試行がまだ無い waiting のジョブを録らないにし、record は録るに戻す（ADR 0019）。jobs は確定の前か後かと、人が決めたかを添える。
func TestSkipAndRecord(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	a := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "テスト番組"}
	id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: "テスト番組", Day: domain.BroadcastDayOf(now), State: domain.StateWaiting,
		Airings: []domain.Airing{a}, NextAttemptAt: now.Add(3 * time.Hour), Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	idArg := strconv.FormatInt(id, 10)
	code, out, stderr := api.run(t, "skip", idArg, "99999")
	if code != 1 || !strings.Contains(out, fmt.Sprintf("ジョブ %d: 録らないにした", id)) || !strings.Contains(out, "ジョブ 99999: 拒んだ（ID が無い）") {
		t.Fatalf("skip: %d %q %q", code, out, stderr)
	}
	if j, err := api.Store.GetJob(ctx, id); err != nil || j.State != domain.StateSkipped || !j.ByPerson || j.Settled {
		t.Errorf("skip の後: %+v, %v", j, err)
	}
	code, out, stderr = api.run(t, "jobs")
	if code != 0 || !strings.Contains(out, "skipped（録らない予定・人が決めた）") {
		t.Errorf("jobs: %d %q %q", code, out, stderr)
	}
	// 録らない予定は手動再試行にできない（record を使う）。
	code, out, stderr = api.run(t, "retry", idArg)
	if code != 1 || !strings.Contains(out, "録るなら rajikura record を使う") {
		t.Errorf("retry（録らない予定）: %d %q %q", code, out, stderr)
	}
	// 2 度目の skip は断る。
	code, out, stderr = api.run(t, "skip", idArg)
	if code != 1 || !strings.Contains(out, "録らないにできるのは試行がまだ無い waiting だけ") {
		t.Errorf("skip（skipped）: %d %q %q", code, out, stderr)
	}
	code, out, stderr = api.run(t, "record", idArg)
	if code != 0 || !strings.Contains(out, fmt.Sprintf("ジョブ %d: 録るにした", id)) {
		t.Fatalf("record: %d %q %q", code, out, stderr)
	}
	if j, err := api.Store.GetJob(ctx, id); err != nil || j.State != domain.StateWaiting || !j.ByPerson {
		t.Errorf("record の後: %+v, %v", j, err)
	}
	code, out, stderr = api.run(t, "record", idArg)
	if code != 1 || !strings.Contains(out, "状態が waiting で、録るにできるのは skipped だけ") {
		t.Errorf("record（waiting）: %d %q %q", code, out, stderr)
	}
	if code, _, stderr := api.run(t, "skip"); code != 2 || !strings.Contains(stderr, "使い方: rajikura skip <ID>") {
		t.Errorf("skip（ID なし）: %d %q", code, stderr)
	}
}

// 接続先とトークンは環境変数と設定ファイルからも読む。つながらない・トークンが通らないときは、そのわけで終わる。
func TestClientConnection(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()

	// つながらない（serve が止まっている）。
	stopped := httptest.NewServer(http.NotFoundHandler())
	stopped.Close()
	code, _, errOut := runCmd(t, ctx, "-url", stopped.URL, "-token", "x", "jobs")
	if code != 1 || !strings.Contains(errOut, "serve につながらない") {
		t.Errorf("止まっている serve: %d %q", code, errOut)
	}
	// トークンが通らない。
	code, _, errOut = runCmd(t, ctx, "-url", api.URL, "-token", "wrong", "jobs")
	if code != 1 || !strings.Contains(errOut, "API トークンを受け付けない") {
		t.Errorf("違うトークン: %d %q", code, errOut)
	}

	// 環境変数。
	t.Setenv(client.EnvURL, api.URL)
	t.Setenv(client.EnvToken, api.Token)
	if code, out, errOut := runCmd(t, ctx, "jobs"); code != 0 || !strings.Contains(out, "直近のエラー") {
		t.Errorf("環境変数: %d %q %q", code, out, errOut)
	}
	t.Setenv(client.EnvURL, "")
	t.Setenv(client.EnvToken, "")

	// 設定ファイル（rajikura/client.toml）。
	dir, err := userConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rajikura", "client.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("url = %q\ntoken = %q\n", api.URL, api.Token)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runCmd(t, ctx, "jobs"); code != 0 || !strings.Contains(out, "直近のエラー") || errOut != "" {
		t.Errorf("設定ファイル: %d %q %q", code, out, errOut)
	}
	if runtime.GOOS != "windows" {
		// 権限が 0600 より広ければ、警告を出して続ける（Windows では確かめない）。
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if code, out, errOut := runCmd(t, ctx, "jobs"); code != 0 || !strings.Contains(out, "直近のエラー") ||
			!strings.Contains(errOut, "警告") || !strings.Contains(errOut, "0600 より広い") {
			t.Errorf("広い権限の設定ファイル: %d %q %q", code, out, errOut)
		}
	}
}

// fakeGuide は serve が使う radiko の偽のサーバー（局情報と番組表だけ）。
// 3 日後の放送日にだけ「テスト番組」を返す。
func fakeGuide(t *testing.T, day domain.BroadcastDay) *httptest.Server {
	t.Helper()
	date := fmt.Sprintf("%04d%02d%02d", day.Year, int(day.Month), day.Day)
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/station/region/full.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<region><stations><station><id>TBS</id><name>TBS RADIO</name><tf_max_delay>90</tf_max_delay></station></stations></region>`)
	})
	mux.HandleFunc("/v3/program/station/date/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/program/station/date/"+date+"/TBS.xml" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<radiko><stations><station id="TBS"><progs>
<prog id="X" ft="%s200000" to="%s203000"><title>テスト番組</title></prog>
</progs></station></stations></radiko>`, date, date)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sp(s string) *string { return &s }

// startServe は serve を args で起こし、待ち受けのアドレスがログに出るまで待つ。ログ（stderr）を読む関数と、止めて終了コードを
// 返す関数を返す。
func startServe(t *testing.T, args ...string) (addr string, logs func() string, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, args, &bytes.Buffer{}, lockedWriter{&mu, &errOut})
	}()
	logs = func() string {
		mu.Lock()
		defer mu.Unlock()
		return errOut.String()
	}
	stopped := false
	code := 0
	stop = func() int {
		if stopped {
			return code
		}
		stopped = true
		cancel()
		select {
		case code = <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("serve が終わらない")
		}
		return code
	}
	t.Cleanup(func() { stop() })
	for deadline := time.Now().Add(20 * time.Second); ; {
		if m := regexp.MustCompile(`待ち受ける" listen=(\S+)`).FindStringSubmatch(logs()); m != nil {
			return m[1], logs, stop
		}
		select {
		case c := <-done:
			stopped, code = true, c
			t.Fatalf("serve が終わった（終了コード %d）: %s", c, logs())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("待ち受けのアドレスがログに無い: %s", logs())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// serve を config.toml なしで -data のフォルダから起こし、設定の API・番組の API・jobs を通す。
// serve は番組表を同期し、ctx の取り消しで終わる。serve は番組を作った直後に展開してジョブを作る
// （次の番組表の同期を待たない。保存先が決まる前でも展開する）。
func TestServe(t *testing.T) {
	isolateClient(t)
	day := domain.BroadcastDayOf(time.Now()).AddDays(3)
	useFakeRadiko(t, fakeGuide(t, day))
	// 通知は偽の Discord で受ける。
	var dmu sync.Mutex
	var sent []string
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		dmu.Lock()
		sent = append(sent, b.String())
		dmu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discord.Close()

	// データのフォルダはまだ無い。serve が作る。
	data := filepath.Join(t.TempDir(), "data")
	addr, logs, stop := startServe(t, "-data", data, "serve", "-listen", "127.0.0.1:0")
	// DB と作業用フォルダはデータのフォルダにできる。鍵のファイルは、秘密を保存するまで作らない。
	for _, name := range []string{config.DBFile, config.WorkDirName} {
		if _, err := os.Stat(filepath.Join(data, name)); err != nil {
			t.Errorf("データのフォルダに %s が無い: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, config.KeyFile)); !os.IsNotExist(err) {
		t.Errorf("秘密を保存する前に鍵のファイルがある: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d", resp.StatusCode)
	}

	// パスワードはまだ無い。セットアップコード（データのフォルダの setup-code.txt）を入れて決める。
	setupPassword(t, "http://"+addr, data, logs())

	// API トークンを発行し（Web UI で発行するのと同じ）、CLI から serve の API を呼ぶ。
	st, err := store.Open(context.Background(), filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(context.Background(), "テスト", time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + addr
	cli := func(args ...string) (int, string, string) {
		return runCmd(t, context.Background(), append([]string{"-url", url, "-token", token}, args...)...)
	}
	api := func(method, path, body string, out any) (int, string) {
		t.Helper()
		return callAPI(t, url, token, method, path, body, out)
	}
	getSettings := func() (config.View, string) {
		t.Helper()
		var v config.View
		code, body := api(http.MethodGet, "/api/settings", "", &v)
		if code != http.StatusOK {
			t.Fatalf("GET /api/settings = %d %s", code, body)
		}
		return v, body
	}

	// 保存先がまだ決まっていない。
	if v, body := getSettings(); v.LibrarySet || v.Settings.Notify == nil || *v.Settings.Notify.Channel != config.ChannelNone {
		t.Fatalf("GET /api/settings（保存先の前）: %s", body)
	}

	// 保存先が決まる前でも、番組を作れば展開してジョブを作る（録音だけを待たせる）。
	// fakeGuide の「テスト番組」（放送日 day の 20:00）に当たる毎週の予約。
	show := fmt.Sprintf(`{"name":"テスト番組","weekly":[{"station":"TBS","weekday":%d,"start":"20:00"}]}`, int(day.Weekday()))
	if code, body := api(http.MethodPost, "/api/shows", show, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/shows: %d %s", code, body)
	}
	// 番組の API は展開を待たずに返す（展開役が後で展開する）ので、ジョブが出るまで期限つきで見直す。
	var code int
	var out, stderr string
	for jobsDeadline := time.Now().Add(10 * time.Second); ; {
		code, out, stderr = cli("jobs", "-state", "waiting")
		if code == 0 && strings.Contains(out, "テスト番組") && strings.Contains(out, day.String()) && strings.Contains(out, "waiting") {
			break
		}
		if time.Now().After(jobsDeadline) {
			t.Fatalf("jobs: %d %q %q", code, out, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	code, out, _ = cli("jobs", "-state", "done,expired")
	if code != 0 || strings.Contains(out, "テスト番組") {
		t.Fatalf("jobs -state done,expired: %d %q", code, out)
	}

	// 誤りがあれば何も当てない（保存先も変わらない）。
	lib := filepath.Join(t.TempDir(), "library", "ラジオ")
	jsonString := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	if code, body := api(http.MethodPatch, "/api/settings", `{"library":`+jsonString(lib)+`,"muxer":"go"}`, nil); code != http.StatusBadRequest ||
		!strings.Contains(body, "muxer") || !strings.Contains(body, "は知らない") {
		t.Fatalf("誤りのある PATCH /api/settings: %d %s", code, body)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Fatalf("誤りがあるのに保存先を作った: %v", err)
	}

	// 通知先を決める前のテスト通知は、どこにも送らずに履歴にだけ入れる（200 と noChannel）。
	testNotify := func() int {
		t.Helper()
		code, _ := api(http.MethodPost, "/api/notifications/test", "", nil)
		return code
	}
	if code, body := api(http.MethodPost, "/api/notifications/test", "", nil); code != http.StatusOK || !strings.Contains(body, `"noChannel":true`) {
		t.Fatalf("通知先を決める前のテスト通知 = %d %s、want 200 と noChannel", code, body)
	}

	// 保存先と通知先（秘密の Webhook）を保存する。保存先のフォルダは serve が作る。通知先は再起動しなくても効く
	// （テスト通知が偽の Discord に届く）。
	webhook := jsonString(discord.URL + "/api/webhooks/1/TOPSECRET")
	if code, body := api(http.MethodPatch, "/api/settings", `{"library":`+jsonString(lib)+`,"notify":{"channel":"discord","discord_webhook":`+webhook+`}}`, nil); code != http.StatusOK {
		t.Fatalf("PATCH /api/settings: %d %s", code, body)
	}
	if fi, err := os.Stat(lib); err != nil || !fi.IsDir() {
		t.Fatalf("保存先のフォルダができていない: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, config.KeyFile)); err != nil {
		t.Errorf("秘密を保存したのに鍵のファイルが無い: %v", err)
	}
	if s := testNotify(); s != http.StatusOK {
		t.Fatalf("通知先を決めた後のテスト通知 = %d（設定が再起動なしで効いていない）", s)
	}
	if v, body := getSettings(); !v.LibrarySet || *v.Settings.Notify.Channel != config.ChannelDiscord ||
		!v.Secrets[config.SecretDiscordWebhook].Set || strings.Contains(body, "TOPSECRET") {
		t.Fatalf("GET /api/settings（保存した後）: %s", body)
	}

	// 書いたキーだけが変わる（保存先はそのまま）。秘密の空の文字列は、その秘密を消す。
	if code, body := api(http.MethodPatch, "/api/settings", `{"notify":{"channel":"none","discord_webhook":""}}`, nil); code != http.StatusOK {
		t.Fatalf("PATCH /api/settings（秘密を消す）: %d %s", code, body)
	}
	if v, body := getSettings(); v.Secrets[config.SecretDiscordWebhook].Set || *v.Settings.Notify.Channel != config.ChannelNone ||
		v.Settings.Library == nil || *v.Settings.Library != lib {
		t.Fatalf("GET /api/settings（秘密を消した後）: %s", body)
	}
	// 次の起動で読む秘密が無いように、通知先を戻しておく。
	if code, body := api(http.MethodPatch, "/api/settings", `{"notify":{"channel":"discord","discord_webhook":`+webhook+`}}`, nil); code != http.StatusOK {
		t.Fatalf("PATCH /api/settings（戻す）: %d %s", code, body)
	}

	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("serve の終了後も HTTP が待ち受けている")
	}
	dmu.Lock()
	if len(sent) != 1 || !strings.Contains(sent[0], "テスト通知") {
		t.Errorf("通知 = %q", sent)
	}
	dmu.Unlock()
	if log := logs(); strings.Contains(log, "TOPSECRET") || !strings.Contains(log, "notify=discord") {
		t.Errorf("ログに Webhook の URL が出た、または送り先の種類が無い: %s", log)
	}

	// 再起動しても、DB の設定（復号した秘密を含む）で起動する。
	_, logs, stop = startServe(t, "serve", "-data", data, "-listen", "127.0.0.1:0")
	stop()
	if log := logs(); !strings.Contains(log, "notify=discord") || strings.Contains(log, "復号できない") {
		t.Errorf("再起動したときのログ: %s", log)
	}

	// serve が止まっていれば、CLI はつながらない旨のエラーで終わる。
	if code, _, stderr := cli("jobs"); code != 1 || !strings.Contains(stderr, "serve につながらない") {
		t.Errorf("止まった serve への jobs: %d %q", code, stderr)
	}
}

// 鍵のファイルが無い・合わないときも serve は起動し、復号できない秘密を未設定として扱い、ログと API で分かるようにする。
// 秘密を入れ直すと（鍵のファイルが無ければ作り直して）また使える。
func TestServeWithoutKeyFile(t *testing.T) {
	isolateClient(t)
	useFakeRadiko(t, fakeGuide(t, domain.BroadcastDayOf(time.Now()).AddDays(3)))
	data := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	setStorePassword(t, st, "secret")
	s := &app.Settings{Store: st, Secrets: secret.Load(filepath.Join(data, config.KeyFile)), Build: buildRuntime}
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, config.Patch{Notify: &config.NotifyPatch{Channel: sp(config.ChannelDiscord), DiscordWebhook: sp("https://discord.invalid/api/webhooks/1/TOPSECRET")}}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(ctx, "テスト", time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	for name, breakKey := range map[string]func(path string) error{
		"無い": os.Remove,
		"合わない": func(path string) error {
			return os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := breakKey(filepath.Join(data, config.KeyFile)); err != nil {
				t.Fatal(err)
			}
			addr, logs, stop := startServe(t, "serve", "-data", data, "-listen", "127.0.0.1:0")
			url := "http://" + addr
			if log := logs(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "秘密を復号できない") ||
				!strings.Contains(log, config.SecretDiscordWebhook) || strings.Contains(log, "TOPSECRET") {
				t.Errorf("ログ: %s", log)
			}
			var v config.View
			if code, body := callAPI(t, url, token, http.MethodGet, "/api/settings", "", &v); code != http.StatusOK {
				t.Fatalf("GET /api/settings = %d %s", code, body)
			}
			if len(v.UnreadableSecrets) != 1 || v.UnreadableSecrets[0] != config.SecretDiscordWebhook ||
				!v.Secrets[config.SecretDiscordWebhook].Unreadable || !v.Secrets[config.SecretDiscordWebhook].Set {
				t.Errorf("GET /api/settings = %+v", v)
			}
			// 入れ直すと、復号できない秘密は無くなる。
			var after config.View
			if code, body := callAPI(t, url, token, http.MethodPatch, "/api/settings",
				`{"notify":{"discord_webhook":"https://discord.invalid/api/webhooks/1/TOPSECRET"}}`, &after); code != http.StatusOK {
				t.Fatalf("PATCH /api/settings: %d %s", code, body)
			}
			if len(after.UnreadableSecrets) != 0 || !after.Secrets[config.SecretDiscordWebhook].Set {
				t.Errorf("入れ直した後の PATCH /api/settings の答え = %+v", after)
			}
			if code := stop(); code != 0 {
				t.Fatalf("serve の終了コード = %d: %s", code, logs())
			}
		})
	}
}

// setStorePassword は、DB に Web UI のパスワードを置く（serve の前に、パスワードを決めた後の状態にする）。
func setStorePassword(t *testing.T, st *store.Store, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPasswordHash(context.Background(), string(hash)); err != nil {
		t.Fatal(err)
	}
}

// readSetupCode は、データのフォルダの setup-code.txt からセットアップコードを読み、ログにも同じコードが出ていることを確かめる。
func readSetupCode(t *testing.T, data, log string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, SetupCodeFile))
	if err != nil {
		t.Fatalf("セットアップコードのファイルが無い: %v", err)
	}
	code := strings.TrimSpace(string(b))
	if code == "" || !strings.Contains(log, "level=INFO") || !strings.Contains(log, "setup_code="+code) {
		t.Fatalf("ログにセットアップコード %q が無い: %s", code, log)
	}
	return code
}

// setupPassword は、セットアップコードを入れてパスワード secret を決め、セットアップコードのファイルが消えたことを確かめる。
func setupPassword(t *testing.T, url, data, log string) {
	t.Helper()
	code := readSetupCode(t, data, log)
	r, err := http.Post(url+"/api/setup", "application/json", strings.NewReader(`{"code":"`+code+`","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/setup = %d", r.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(data, SetupCodeFile)); !os.IsNotExist(err) {
		t.Fatalf("パスワードを決めた後もセットアップコードのファイルがある: %v", err)
	}
}

func getStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode
}

// hostStatus は Host を host にして GET を送り、状態コードを返す。
func hostStatus(t *testing.T, url, host string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = host
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode
}

// serve の -allowed-hosts は、serve の前にも後にも書け、並べた名前の Host の Web UI を通す。並べていない名前は 421。
// /healthz は Host を見ない。
func TestServeAllowedHosts(t *testing.T) {
	useFakeRadiko(t, fakeGuide(t, domain.BroadcastDayOf(time.Now()).AddDays(3)))
	for _, args := range [][]string{
		{"-data", t.TempDir(), "-allowed-hosts", "rajikura.example.com, pc.ts.net", "serve", "-listen", "127.0.0.1:0"},
		{"serve", "-data", t.TempDir(), "-listen", "127.0.0.1:0", "-allowed-hosts", "rajikura.example.com,pc.ts.net"},
	} {
		addr, logs, stop := startServe(t, args...)
		url := "http://" + addr
		for _, host := range []string{"rajikura.example.com", "pc.ts.net:8080", addr} {
			if got := hostStatus(t, url+"/", host); got != http.StatusOK {
				t.Errorf("%v: Host %q の / = %d、want 200", args, host, got)
			}
		}
		if got := hostStatus(t, url+"/", "other.example.com"); got != http.StatusMisdirectedRequest {
			t.Errorf("%v: 並べていない名前の / = %d、want 421", args, got)
		}
		if got := hostStatus(t, url+"/healthz", "other.example.com"); got != http.StatusOK {
			t.Errorf("%v: 並べていない名前の /healthz = %d、want 200", args, got)
		}
		if code := stop(); code != 0 {
			t.Fatalf("serve の終了コード = %d: %s", code, logs())
		}
	}
}

// パスワードが無ければ、serve は起動のたびに新しいセットアップコードを作り、setup-code.txt とログに出す。
// 決めるまでは、SPA と /healthz のほかは 503 を返す（API トークンがあっても使えない）。
func TestServeWithoutPassword(t *testing.T) {
	useFakeRadiko(t, fakeGuide(t, domain.BroadcastDayOf(time.Now()).AddDays(3)))
	data := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(context.Background(), "テスト", time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	addr, logs, stop := startServe(t, "-data", data, "-listen", "127.0.0.1:0", "serve")
	url := "http://" + addr
	first := readSetupCode(t, data, logs())
	if got := getStatus(t, url+"/", ""); got != http.StatusOK {
		t.Errorf("/ = %d、want 200（パスワードを決める画面）", got)
	}
	for _, p := range []string{"/api/me", "/api/settings"} {
		if got := getStatus(t, url+p, token); got != http.StatusServiceUnavailable {
			t.Errorf("トークン付きの %s = %d、want 503", p, got)
		}
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}

	// 起動し直すと、新しいコードになる。
	addr, logs, stop = startServe(t, "-data", data, "-listen", "127.0.0.1:0", "serve")
	second := readSetupCode(t, data, logs())
	if second == first {
		t.Errorf("起動し直してもセットアップコードが同じ: %q", second)
	}
	// 前の起動のコードは使えない。
	r, err := http.Post("http://"+addr+"/api/setup", "application/json", strings.NewReader(`{"code":"`+first+`","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("前の起動のコード = %d、want 401", r.StatusCode)
	}
	setupPassword(t, "http://"+addr, data, logs())
	if got := getStatus(t, "http://"+addr+"/api/settings", token); got != http.StatusOK {
		t.Errorf("パスワードを決めた後のトークン付きの /api/settings = %d、want 200", got)
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}

	// パスワードが決まった後に起動したら、セットアップコードを作らない（前の起動で残ったファイルは消す）。
	os.WriteFile(filepath.Join(data, SetupCodeFile), []byte("OLD\n"), 0o600)
	_, logs, stop = startServe(t, "-data", data, "-listen", "127.0.0.1:0", "serve")
	if _, err := os.Stat(filepath.Join(data, SetupCodeFile)); !os.IsNotExist(err) {
		t.Errorf("パスワードがあるのにセットアップコードのファイルがある: %v", err)
	}
	if strings.Contains(logs(), "setup_code=") {
		t.Errorf("パスワードがあるのにセットアップコードをログに出した: %s", logs())
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}
}

// serve -reset-password は、パスワード・ログインのセッション・API トークンを消して起動を続け、セットアップコードを出す。
func TestServeResetPassword(t *testing.T) {
	useFakeRadiko(t, fakeGuide(t, domain.BroadcastDayOf(time.Now()).AddDays(3)))
	data := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	setStorePassword(t, st, "forgotten")
	sid, _, err := st.CreateSession(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(ctx, "テスト", time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	addr, logs, stop := startServe(t, "-data", data, "serve", "-reset-password", "-listen", "127.0.0.1:0")
	url := "http://" + addr
	if !strings.Contains(logs(), "-reset-password") {
		t.Errorf("戻したことがログに無い: %s", logs())
	}
	readSetupCode(t, data, logs())
	if got := getStatus(t, url+"/api/me", token); got != http.StatusServiceUnavailable {
		t.Errorf("戻した後のトークン付きの /api/me = %d、want 503", got)
	}
	setupPassword(t, url, data, logs())
	// 前のトークンは消えている（パスワードを決め直しても使えない）。
	if got := getStatus(t, url+"/api/me", token); got != http.StatusUnauthorized {
		t.Errorf("決め直した後の前のトークン = %d、want 401", got)
	}
	req, _ := http.NewRequest(http.MethodGet, url+"/api/me", nil)
	req.AddCookie(&http.Cookie{Name: server.SessionCookie, Value: sid})
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("決め直した後の前のセッション = %d、want 401", r.StatusCode)
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}
}

// 前の起動の setup-code.txt が 0600 より広い権限で残っていても、書いた後は 0600 で、新しいコードになる。
func TestPrepareSetupCodeRecreatesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ではファイルの権限（0600）を見られない")
	}
	data := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	file := filepath.Join(data, SetupCodeFile)
	if err := os.WriteFile(file, []byte("OLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := prepareSetupCode(ctx, st, config.Boot{DataDir: data}, serveOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || sc == nil {
		t.Fatalf("prepareSetupCode = %v, %v", sc, err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("権限 = %o、want 600", perm)
	}
	if b, _ := os.ReadFile(file); strings.TrimSpace(string(b)) != sc.Code {
		t.Errorf("中身 = %q、want %q", b, sc.Code)
	}
}

// 通知先なし（none）なら通知先を作らない。discord と slack なら Webhook に送る通知先を、email なら SMTP で送る通知先を作る。
// 選んだ通知先の秘密が無い（復号できない）ときは作らない。
func TestNewNotifier(t *testing.T) {
	if n := newNotifier(config.Config{NotifyChannel: config.ChannelNone, DiscordWebhook: "https://discord.invalid/x"}); n != nil {
		t.Fatalf("none のとき %T", n)
	}
	if d, ok := newNotifier(config.Config{NotifyChannel: config.ChannelDiscord, DiscordWebhook: "https://discord.invalid/x"}).(*notify.Discord); !ok || d.WebhookURL != "https://discord.invalid/x" {
		t.Fatalf("discord のとき %+v", d)
	}
	if s, ok := newNotifier(config.Config{NotifyChannel: config.ChannelSlack, SlackWebhook: "https://slack.invalid/x"}).(*notify.Slack); !ok || s.WebhookURL != "https://slack.invalid/x" {
		t.Fatalf("slack のとき %+v", s)
	}
	em, ok := newNotifier(config.Config{
		NotifyChannel: config.ChannelEmail, SMTPHost: "smtp.invalid", SMTPPort: 587, SMTPUsername: "u@example.com",
		SMTPPassword: "pw", SMTPFrom: "f@example.com", SMTPTo: []string{"a@example.com", "b@example.com"},
	}).(*notify.Email)
	if !ok || em.Host != "smtp.invalid" || em.Port != 587 || em.Username != "u@example.com" || em.Password != "pw" ||
		em.From != "f@example.com" || len(em.To) != 2 || em.To[1] != "b@example.com" {
		t.Fatalf("email のとき %+v", em)
	}
	for _, c := range []config.Config{
		{NotifyChannel: config.ChannelDiscord},
		{NotifyChannel: config.ChannelSlack},
		{NotifyChannel: config.ChannelEmail, SMTPHost: "smtp.invalid", SMTPPort: 587, SMTPUsername: "u", SMTPFrom: "f@example.com", SMTPTo: []string{"a@example.com"}},
	} {
		if n := newNotifier(c); n != nil {
			t.Errorf("%s の秘密が無いのに %T", c.NotifyChannel, n)
		}
	}
}

// Jellyfin の API キーが無い（復号できない）ときは、再スキャンを頼まない。
func TestBuildRuntimeJellyfin(t *testing.T) {
	c := config.Default()
	c.JellyfinURL, c.JellyfinLibrary = "http://j:8096", "ラジオ"
	if rt := buildRuntime(c); rt.Rescanner != nil {
		t.Errorf("API キーが無いのに %T", rt.Rescanner)
	}
	c.JellyfinAPIKey = "key"
	if rt := buildRuntime(c); rt.Rescanner == nil {
		t.Error("API キーがあるのに再スキャンを頼まない")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
