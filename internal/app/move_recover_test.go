package app

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// caseInsensitiveFS は、t.TempDir() のファイルシステムが名前の大文字・小文字を区別しないか（小文字の名前のファイルが、大文字の名前で
// 同じ実体に見えるか）。
func caseInsensitiveFS(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	lower := filepath.Join(dir, "probe-a")
	if err := os.WriteFile(lower, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := os.Lstat(lower)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Lstat(filepath.Join(dir, "PROBE-A"))
	return err == nil && os.SameFile(a, b)
}

// dirNames は dir の中の名前（partial などの残骸を見つけるため、全部）。
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// 名前の違いがパスに使えない文字の置き換え・末尾のピリオドの除去だけで、移し先が今の保存先と同じなら、その場でアルバムの名前を今の
// 番組の名前に書き換え、履歴に album_rewritten を 1 行残し（From と To は同じパス）、再スキャンを頼む。名前が変わっていないのに人が
// 頼んだときも 1 回書き換える。ファイルは 1 つのまま、音声は元のまま（ADR 0039）。
func TestMoveRewritesAlbumInPlace(t *testing.T) {
	e := newMoveEnv(t)
	r := &fakeRescanner{}
	e.app.Rescanner = r
	j := e.job(flatShow.Name)
	before, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(j.OutputPath)
	entries := dirNames(t, dir)
	at := afterBroadcast.Add(time.Hour)
	e.clock.Set(at)
	name := flatShow.Name + "."
	e.renameShow(flatShow, name)
	if !e.runDue() {
		t.Fatal("移しを拾わない")
	}
	got := e.job(name)
	if got.OutputPath != j.OutputPath || !got.MoveRequestedAt.IsZero() || got.MoveTo != "" {
		t.Errorf("書き換えた後のジョブ = %+v", got)
	}
	if rw := e.media.albumRewriter(); len(rw.rewrites) != 1 || rw.rewrites[0] != (fakeAlbumRewrite{in: j.OutputPath, album: name}) {
		t.Errorf("RewriteAlbum = %+v", rw.rewrites)
	}
	if after, err := os.ReadFile(j.OutputPath); err != nil || !bytes.Equal(after, before) {
		t.Errorf("書き換えたファイル = %q, %v", after, err)
	}
	if got := dirNames(t, dir); strings.Join(got, "|") != strings.Join(entries, "|") {
		t.Errorf("フォルダの中身 = %v, want %v（partial が残っていない・ファイルが増えていない）", got, entries)
	}
	ms := e.outputMoves(j.ID)
	if len(ms) != 1 || ms[0].Result != domain.OutputAlbumRewritten || ms[0].From != j.OutputPath || ms[0].To != j.OutputPath ||
		!ms[0].At.Equal(at) {
		t.Errorf("移しの履歴 = %+v", ms)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	// 名前が変わっていないのに頼まれても、1 回書き換える。
	if err := e.store.RequestOutputMove(e.ctx, j.ID, "", at); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("頼み直しを拾わない")
	}
	if ms := e.outputMoves(j.ID); len(ms) != 2 || ms[1].Result != domain.OutputAlbumRewritten {
		t.Errorf("頼み直しの後の移しの履歴 = %+v", ms)
	}
	if rw := e.media.albumRewriter(); len(rw.rewrites) != 2 {
		t.Errorf("RewriteAlbum = %+v", rw.rewrites)
	}
	if r.calls != 2 {
		t.Errorf("再スキャンの回数 = %d, want 2", r.calls)
	}
}

// その場の書き換えが失敗した（音声が元と一致しないときも）ら、failed にして元のファイルをそのまま残す（partial も残さない）。
func TestMoveRewriteInPlaceFails(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		corrupt bool
	}{{"書き換えが失敗した", errors.New("書けない"), false}, {"音声が元と一致しない", nil, true}} {
		t.Run(c.name, func(t *testing.T) {
			e := newMoveEnv(t)
			r := &fakeRescanner{}
			e.app.Rescanner = r
			j := e.job(flatShow.Name)
			before, err := os.ReadFile(j.OutputPath)
			if err != nil {
				t.Fatal(err)
			}
			entries := dirNames(t, filepath.Dir(j.OutputPath))
			rw := e.media.albumRewriter()
			rw.err, rw.corrupt = c.err, c.corrupt
			e.renameShow(flatShow, flatShow.Name+".")
			if !e.runDue() {
				t.Fatal("移しを拾わない")
			}
			if after, err := os.ReadFile(j.OutputPath); err != nil || !bytes.Equal(after, before) {
				t.Errorf("元のファイル = %q, %v", after, err)
			}
			if got := dirNames(t, filepath.Dir(j.OutputPath)); strings.Join(got, "|") != strings.Join(entries, "|") {
				t.Errorf("フォルダの中身 = %v, want %v", got, entries)
			}
			if ms := e.outputMoves(j.ID); len(ms) != 1 || ms[0].Result != domain.OutputMoveFailed || ms[0].Error == "" {
				t.Errorf("移しの履歴 = %+v", ms)
			}
			if r.calls != 0 {
				t.Errorf("再スキャンの回数 = %d, want 0", r.calls)
			}
		})
	}
}

// 英字の大文字・小文字だけの名前の変更（ADR 0039）。大文字・小文字を区別しないファイルシステムでは、ファイル名が新しい表記になり、
// アルバムの名前が今の名前になり、ファイルは 1 つだけ残る（フォルダは古い表記のまま。保存先の記録は新しい表記のパス）。区別する
// ファイルシステムでは、同じ入力が普通の移しになる（新しい表記のフォルダへ移し、古いフォルダを片付ける）。
func TestMoveCaseOnlyRename(t *testing.T) {
	e := newMoveEnv(t)
	lower := e.renameShow(flatShow, "flat")
	if !e.runDue() {
		t.Fatal("1 回目の移しをしない")
	}
	j := e.job("flat")
	station := filepath.Join(e.app.Library, "TBSラジオ")
	if j.OutputPath != filepath.Join(station, "flat", "flat 2026-10-07.m4a") {
		t.Fatalf("1 回目の移しの後: %+v", j)
	}
	before, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRescanner{}
	e.app.Rescanner = r
	e.renameShow(lower, "FLAT")
	if !e.runDue() {
		t.Fatal("大文字・小文字だけの移しをしない")
	}
	want := filepath.Join(station, "FLAT", "FLAT 2026-10-07.m4a")
	got := e.job("FLAT")
	if got.OutputPath != want || got.MoveTo != "" || !got.MoveRequestedAt.IsZero() {
		t.Errorf("移した後のジョブ = %+v", got)
	}
	if after, err := os.ReadFile(want); err != nil || !bytes.Equal(after, before) {
		t.Errorf("移したファイル = %q, %v", after, err)
	}
	rw := e.media.albumRewriter()
	if n := len(rw.rewrites); n != 2 || rw.rewrites[1].album != "FLAT" {
		t.Errorf("RewriteAlbum = %+v", rw.rewrites)
	}
	if ms := e.outputMoves(j.ID); len(ms) != 2 || ms[1].Result != domain.OutputMoved || ms[1].From != j.OutputPath || ms[1].To != want {
		t.Errorf("移しの履歴 = %+v", ms)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	var folders []string
	for _, n := range dirNames(t, station) {
		if strings.EqualFold(n, "flat") {
			folders = append(folders, n)
		}
	}
	if caseInsensitiveFS(t) {
		if len(folders) != 1 || folders[0] != "flat" {
			t.Errorf("番組のフォルダ = %v, want [flat]（フォルダは古い表記のまま）", folders)
		}
		if got := dirNames(t, filepath.Join(station, "flat")); strings.Join(got, "|") != "FLAT 2026-10-07.m4a|folder.jpg" {
			t.Errorf("番組のフォルダの中身 = %v（ファイル名は新しい表記で 1 つだけ、folder.jpg は残る）", got)
		}
		return
	}
	if len(folders) != 1 || folders[0] != "FLAT" {
		t.Errorf("番組のフォルダ = %v, want [FLAT]（区別するファイルシステムでは普通の移し）", folders)
	}
	if got := dirNames(t, filepath.Join(station, "FLAT")); strings.Join(got, "|") != "FLAT 2026-10-07.m4a|folder.jpg" {
		t.Errorf("新しいフォルダの中身 = %v", got)
	}
}

// 移した後、結果を書く（Store.FinishOutputMove）のが失敗しても、次の見回りで、移し先の控えから保存先を移した先に付け替える（ADR 0039）。
// ファイルは移した先に 1 つだけ残り、消えない。
func TestMoveRecoversAfterFinishFails(t *testing.T) {
	e := newMoveEnv(t)
	j := e.job(flatShow.Name)
	before, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(filepath.Dir(e.app.Library), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_output_moves BEFORE INSERT ON output_moves BEGIN SELECT RAISE(ABORT, '書けない'); END`); err != nil {
		t.Fatal(err)
	}
	e.renameShow(flatShow, "ふらっと（新）")
	to := filepath.Join(e.app.Library, "TBSラジオ", "ふらっと（新）", "ふらっと（新） 2026-10-07.m4a")
	if _, err := e.app.RunDue(e.ctx); err == nil {
		t.Fatal("結果を書けないのにエラーにならない")
	}
	stuck := e.job("ふらっと（新）")
	if stuck.OutputPath != j.OutputPath || stuck.MoveTo != to || stuck.MoveRequestedAt.IsZero() {
		t.Fatalf("結果を書けなかった後のジョブ = %+v（保存先は前のまま、控えと頼みの印が残る）", stuck)
	}
	if _, err := os.Stat(j.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("元のファイルが残っている（移していない）: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_output_moves`); err != nil {
		t.Fatal(err)
	}
	r := &fakeRescanner{}
	e.app.Rescanner = r
	if !e.runDue() {
		t.Fatal("控えのあるジョブを拾わない")
	}
	got := e.job("ふらっと（新）")
	if got.OutputPath != to || got.MoveTo != "" || !got.MoveRequestedAt.IsZero() {
		t.Errorf("回復した後のジョブ = %+v", got)
	}
	if after, err := os.ReadFile(to); err != nil || !bytes.Equal(after, before) {
		t.Errorf("移したファイル = %q, %v", after, err)
	}
	if ms := e.outputMoves(j.ID); len(ms) != 1 || ms[0].Result != domain.OutputMoved || ms[0].From != j.OutputPath || ms[0].To != to {
		t.Errorf("移しの履歴 = %+v", ms)
	}
	if rw := e.media.albumRewriter(); len(rw.rewrites) != 1 {
		t.Errorf("RewriteAlbum = %+v（回復では書き直さない）", rw.rewrites)
	}
	if _, err := os.Stat(filepath.Dir(j.OutputPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("古いフォルダが残っている: %v", err)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	if e.runDue() {
		t.Error("回復した後も何かをした")
	}
}

// putFile は path にフォルダごと data を書く。
func putFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// hasFile は path の中身が want かを確かめる。
func hasFile(t *testing.T, path string, want []byte) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, want) {
		t.Errorf("%s = %q, %v; want %q", filepath.Base(path), b, err, want)
	}
}

// noFile は path が無いことを確かめる。
func noFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s が残っている: %v", filepath.Base(path), err)
	}
}

// pickRecovered は、控えのあるジョブを拾わせ、ジョブの保存先が path で、控えと頼みの印が消え、移しの履歴の n 行目（1 から）が result
// であることを確かめて、その行を返す。
func pickRecovered(t *testing.T, e *env, show string, id int64, path string, n int, result domain.OutputMoveResult) domain.OutputMove {
	t.Helper()
	if !e.runDue() {
		t.Fatal("控えのあるジョブを拾わない")
	}
	got := e.job(show)
	if got.OutputPath != path || got.MoveTo != "" || !got.MoveRequestedAt.IsZero() {
		t.Errorf("ジョブ = %+v, want 保存先 %s", got, path)
	}
	ms := e.outputMoves(id)
	if len(ms) != n || ms[n-1].Result != result {
		t.Fatalf("移しの履歴 = %+v, want %d 行目が %s", ms, n, result)
	}
	return ms[n-1]
}

// 移し先の控えのあるジョブを拾ったら、控えの先とファイルの様子から、ADR 0039 の表のとおりに回復する（元と移し先が別の実体の行。
// 同じ実体の行は move_recover_windows_test.go）。どの状態からも、どのファイルも上書きせず、消すのは音声が一致したと確かめた側だけ。
func TestMoveRecoveryStates(t *testing.T) {
	newName := "ふらっと（新）"
	type state struct {
		e        *env
		job      domain.Job
		from, to string
		audio    []byte
	}
	// setup は、名前を変えて移しの頼みと移し先の控えを付けたところ（ファイルはまだ動かしていない）。
	setup := func(t *testing.T) state {
		t.Helper()
		e := newMoveEnv(t)
		j := e.job(flatShow.Name)
		b, err := os.ReadFile(j.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		e.renameShow(flatShow, newName)
		to := filepath.Join(e.app.Library, "TBSラジオ", newName, newName+" 2026-10-07.m4a")
		if err := e.store.SetOutputMoveTo(e.ctx, j.ID, to); err != nil {
			t.Fatal(err)
		}
		return state{e: e, job: j, from: j.OutputPath, to: to, audio: b}
	}
	after := func(t *testing.T, s state, path string, result domain.OutputMoveResult) domain.OutputMove {
		t.Helper()
		return pickRecovered(t, s.e, newName, s.job.ID, path, 1, result)
	}

	t.Run("置く前に止まった", func(t *testing.T) {
		s := setup(t)
		m := after(t, s, s.to, domain.OutputMoved)
		hasFile(t, s.to, s.audio)
		noFile(t, s.from)
		if m.From != s.from || m.To != s.to {
			t.Errorf("移しの履歴 = %+v", m)
		}
		if rw := s.e.media.albumRewriter(); len(rw.rewrites) != 1 {
			t.Errorf("RewriteAlbum = %+v（頭から移し直す）", rw.rewrites)
		}
	})
	t.Run("元を消した後、記録の前に止まった", func(t *testing.T) {
		s := setup(t)
		putFile(t, s.to, s.audio)
		if err := os.Remove(s.from); err != nil {
			t.Fatal(err)
		}
		after(t, s, s.to, domain.OutputMoved)
		hasFile(t, s.to, s.audio)
		if rw := s.e.media.albumRewriter(); len(rw.rewrites) != 0 {
			t.Errorf("RewriteAlbum = %+v（付け替えるだけ）", rw.rewrites)
		}
	})
	t.Run("置いた後、元を消す前に止まった", func(t *testing.T) {
		s := setup(t)
		putFile(t, s.to, s.audio)
		after(t, s, s.to, domain.OutputMoved)
		hasFile(t, s.to, s.audio)
		noFile(t, s.from)
	})
	t.Run("置いた後、元を消す前に止まったが、音声が一致しない", func(t *testing.T) {
		s := setup(t)
		other := append([]byte("別の音声"), s.audio...)
		putFile(t, s.to, other)
		m := after(t, s, s.from, domain.OutputMoveFailed)
		hasFile(t, s.from, s.audio)
		hasFile(t, s.to, other)
		if !strings.Contains(m.Error, "両方を残した") {
			t.Errorf("Error = %q（両方を残した理由が無い）", m.Error)
		}
	})
	t.Run("両方とも無い", func(t *testing.T) {
		s := setup(t)
		if err := os.Remove(s.from); err != nil {
			t.Fatal(err)
		}
		after(t, s, s.from, domain.OutputMoveNoFile)
		noFile(t, s.to)
	})
}
