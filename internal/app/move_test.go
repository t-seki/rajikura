package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
)

// fakeAlbumRewrite は、偽の RewriteAlbum に渡されたもの。
type fakeAlbumRewrite struct{ in, album string }

// fakeAlbumRewriter は、fakeMedia（fake_test.go）の AlbumRewriter の分の記録と、起こす失敗。
type fakeAlbumRewriter struct {
	mu       sync.Mutex
	rewrites []fakeAlbumRewrite
	// err があれば RewriteAlbum はそれを返す。corrupt なら、書いたファイルの音声を元と変える（SameAudio が一致しない）。
	err     error
	corrupt bool
}

// fakeAlbumRewriters は fakeMedia ごとの fakeAlbumRewriter（fakeMedia のフィールドに持たせないので、ここで引く）。
var fakeAlbumRewriters sync.Map

func (m *fakeMedia) albumRewriter() *fakeAlbumRewriter {
	v, _ := fakeAlbumRewriters.LoadOrStore(m, &fakeAlbumRewriter{})
	return v.(*fakeAlbumRewriter)
}

// RewriteAlbum は、in の中身をそのまま out に写し、渡されたものを記録する（AlbumRewriter。SameAudio は chapters_test.go）。
func (m *fakeMedia) RewriteAlbum(ctx context.Context, in, out, album string) error {
	r := m.albumRewriter()
	r.mu.Lock()
	r.rewrites = append(r.rewrites, fakeAlbumRewrite{in: in, album: album})
	err, corrupt := r.err, r.corrupt
	r.mu.Unlock()
	if err != nil {
		return err
	}
	d, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	if corrupt {
		d = append([]byte("壊れた"), d...)
	}
	return os.WriteFile(out, d, 0o644)
}

// renameShow は番組 old の名前を name に変える（Web UI の編集と同じく store.ReplaceShow）。
func (e *env) renameShow(old domain.Show, name string) domain.Show {
	e.t.Helper()
	s := old
	s.Name = name
	if err := e.store.ReplaceShow(e.ctx, old.Name, s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) outputMoves(id int64) []domain.OutputMove {
	e.t.Helper()
	ms, err := e.store.OutputMoves(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ms
}

// newMoveEnv は、ふらっと（2 つの放送回をつなぐ分割番組）とすっぴんしゃんを録り終えた環境を返す。ふらっとのフォルダには folder.jpg を置く。
func newMoveEnv(t *testing.T) *env {
	t.Helper()
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	for e.runDue() {
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || j.OutputPath == "" {
		t.Fatalf("job = %+v", j)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(j.OutputPath), "folder.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

// 番組の名前を変えると、録音済みのジョブも新しい名前に付け替わり、録音のループが保存したファイルを今の名前のフォルダ・ファイル名に移す
// （ADR 0039）。アルバムの名前は今の番組の名前に書き換え、音声は取り直さない。保存先の記録を付け替え、古いフォルダに残った folder.jpg を
// 新しいフォルダへ移して古いフォルダを消し、再スキャンを頼む。試行は起こさず（状態・試行回数はそのまま）、通知はしない。履歴に 1 行残す。
func TestRenameMovesSavedFile(t *testing.T) {
	e := newMoveEnv(t)
	r := &fakeRescanner{}
	e.app.Rescanner = r
	j := e.job(flatShow.Name)
	before, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	kinds, attempts := e.kinds(), len(e.attempts(j.ID))
	other := e.job(suppinShow.Name)

	at := afterBroadcast.Add(time.Hour)
	e.clock.Set(at)
	renamed := e.renameShow(flatShow, "ふらっと（新）")
	if got := e.job(renamed.Name); got.ID != j.ID || got.MoveRequestedAt.IsZero() || got.OutputPath != j.OutputPath {
		t.Fatalf("名前を変えた直後: %+v", got)
	}
	if !e.runDue() {
		t.Fatal("保存先を移さない")
	}
	want := filepath.Join(e.app.Library, "TBSラジオ", "ふらっと（新）", "ふらっと（新） 2026-10-07.m4a")
	got := e.job(renamed.Name)
	if got.OutputPath != want || got.State != domain.StateDone || got.Attempts != j.Attempts || !got.MoveRequestedAt.IsZero() {
		t.Fatalf("移した後: %+v", got)
	}
	if after, err := os.ReadFile(want); err != nil || !bytes.Equal(after, before) {
		t.Errorf("移したファイル = %q, %v", after, err)
	}
	oldDir := filepath.Dir(j.OutputPath)
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("古いフォルダが残っている: %v", err)
	}
	if img, err := os.ReadFile(filepath.Join(filepath.Dir(want), "folder.jpg")); err != nil || string(img) != "img" {
		t.Errorf("新しいフォルダの folder.jpg = %q, %v", img, err)
	}
	rw := e.media.albumRewriter()
	if len(rw.rewrites) != 1 || rw.rewrites[0] != (fakeAlbumRewrite{in: j.OutputPath, album: "ふらっと（新）"}) {
		t.Errorf("RewriteAlbum = %+v", rw.rewrites)
	}
	ms := e.outputMoves(j.ID)
	if len(ms) != 1 || ms[0].Result != domain.OutputMoved || ms[0].From != j.OutputPath || ms[0].To != want || !ms[0].At.Equal(at) {
		t.Errorf("移しの履歴 = %+v", ms)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	if n := len(e.attempts(j.ID)); n != attempts {
		t.Errorf("試行の数 = %d, want %d", n, attempts)
	}
	if n := e.fake.playlistCount(flat1.start); n != 1 {
		t.Errorf("入手の回数 = %d, want 1（音声は取り直さない）", n)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("保存先の移しで通知した: %v → %v", kinds, after)
	}
	// ほかの番組のジョブは触らない。頼みは 1 回で消える。
	if o := e.job(suppinShow.Name); o.OutputPath != other.OutputPath || !o.MoveRequestedAt.IsZero() {
		t.Errorf("ほかの番組のジョブ: %+v", o)
	}
	if e.runDue() {
		t.Error("移した後も何かをした")
	}
	// 続けてもう一度名前を変えても、移すときの名前に揃う。
	e.renameShow(renamed, "ふらっと")
	if !e.runDue() {
		t.Fatal("2 回目の名前の変更で移さない")
	}
	if got := e.job("ふらっと"); got.OutputPath != filepath.Join(e.app.Library, "TBSラジオ", "ふらっと", "ふらっと 2026-10-07.m4a") {
		t.Errorf("2 回目の移しの後: %+v", got)
	}
}

// 移し先に同じ名前のファイルがある・保存先にファイルが無い・保存先のパスが番組の名前の形でない・書き換えが失敗した・音声が元と
// 一致しないときは、移さず、どのファイルも消さず、保存先の記録も変えず、履歴に理由が残る（ADR 0039）。古いフォルダにほかのファイルが
// あれば、移しても古いフォルダには触らない。
func TestMoveRefusals(t *testing.T) {
	newPath := func(e *env) string {
		return filepath.Join(e.app.Library, "TBSラジオ", "ふらっと（新）", "ふらっと（新） 2026-10-07.m4a")
	}
	t.Run("移し先に同じ名前のファイルがある", func(t *testing.T) {
		e := newMoveEnv(t)
		j := e.job(flatShow.Name)
		to := newPath(e)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, []byte("人が動かしたファイル"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.renameShow(flatShow, "ふらっと（新）")
		if !e.runDue() {
			t.Fatal("移しを拾わない")
		}
		got := e.job("ふらっと（新）")
		if got.OutputPath != j.OutputPath || !got.MoveRequestedAt.IsZero() {
			t.Errorf("ジョブ = %+v", got)
		}
		if b, _ := os.ReadFile(to); string(b) != "人が動かしたファイル" {
			t.Errorf("移し先を上書きした: %q", b)
		}
		if _, err := os.Stat(j.OutputPath); err != nil {
			t.Errorf("元のファイルを消した: %v", err)
		}
		if ms := e.outputMoves(j.ID); len(ms) != 1 || ms[0].Result != domain.OutputMoveTargetExists || ms[0].To != to {
			t.Errorf("移しの履歴 = %+v", ms)
		}
	})
	t.Run("保存先にファイルが無い", func(t *testing.T) {
		e := newMoveEnv(t)
		j := e.job(flatShow.Name)
		if err := os.Remove(j.OutputPath); err != nil {
			t.Fatal(err)
		}
		e.renameShow(flatShow, "ふらっと（新）")
		if !e.runDue() {
			t.Fatal("移しを拾わない")
		}
		if got := e.job("ふらっと（新）"); got.OutputPath != j.OutputPath {
			t.Errorf("保存先を変えた: %+v", got)
		}
		if _, err := os.Stat(filepath.Dir(newPath(e))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("新しいフォルダを作った: %v", err)
		}
		if ms := e.outputMoves(j.ID); len(ms) != 1 || ms[0].Result != domain.OutputMoveNoFile {
			t.Errorf("移しの履歴 = %+v", ms)
		}
	})
	t.Run("保存先のパスが番組の名前の形でない", func(t *testing.T) {
		e := newMoveEnv(t)
		odd := filepath.Join(e.app.Library, "よそ", "手で付けた名前.m4a")
		id, err := e.store.InsertJob(e.ctx, domain.Job{ShowName: "古い名前", Day: day, State: domain.StateDone,
			Airings: []domain.Airing{{StationID: "TBS", Start: suppin.start, End: suppin.end}}, NextAttemptAt: afterBroadcast,
			Deadline: afterBroadcast.Add(7 * 24 * time.Hour), OutputPath: odd})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.RequestOutputMove(e.ctx, id, suppinShow.Name, afterBroadcast); err != nil {
			t.Fatal(err)
		}
		if !e.runDue() {
			t.Fatal("移しを拾わない")
		}
		if ms := e.outputMoves(id); len(ms) != 1 || ms[0].Result != domain.OutputMoveNotShowPath || ms[0].To != "" || ms[0].From != odd {
			t.Errorf("移しの履歴 = %+v", ms)
		}
		got, _ := e.store.GetJob(e.ctx, id)
		if got.OutputPath != odd || got.ShowName != suppinShow.Name || !got.MoveRequestedAt.IsZero() {
			t.Errorf("ジョブ = %+v（番組は付け替わり、保存先は変えない）", got)
		}
	})
	for _, c := range []struct {
		name    string
		err     error
		corrupt bool
	}{{"書き換えが失敗した", errors.New("書けない"), false}, {"音声が元と一致しない", nil, true}} {
		t.Run(c.name, func(t *testing.T) {
			e := newMoveEnv(t)
			j := e.job(flatShow.Name)
			rw := e.media.albumRewriter()
			rw.err, rw.corrupt = c.err, c.corrupt
			e.renameShow(flatShow, "ふらっと（新）")
			if !e.runDue() {
				t.Fatal("移しを拾わない")
			}
			if got := e.job("ふらっと（新）"); got.OutputPath != j.OutputPath {
				t.Errorf("保存先を変えた: %+v", got)
			}
			if _, err := os.Stat(j.OutputPath); err != nil {
				t.Errorf("元のファイルを消した: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(newPath(e))); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("新しいフォルダ（partial）が残っている: %v", err)
			}
			ms := e.outputMoves(j.ID)
			if len(ms) != 1 || ms[0].Result != domain.OutputMoveFailed || ms[0].Error == "" {
				t.Errorf("移しの履歴 = %+v", ms)
			}
		})
	}
	t.Run("古いフォルダにほかのファイルがある", func(t *testing.T) {
		e := newMoveEnv(t)
		j := e.job(flatShow.Name)
		keep := filepath.Join(filepath.Dir(j.OutputPath), "パンサー向井のふらっと 2026-09-30.m4a")
		if err := os.WriteFile(keep, []byte("前の回"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.renameShow(flatShow, "ふらっと（新）")
		if !e.runDue() {
			t.Fatal("移さない")
		}
		if got := e.job("ふらっと（新）"); got.OutputPath != newPath(e) {
			t.Errorf("ジョブ = %+v", got)
		}
		for _, p := range []string{keep, filepath.Join(filepath.Dir(j.OutputPath), "folder.jpg")} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("古いフォルダの %s に触った: %v", filepath.Base(p), err)
			}
		}
	})
}

// 名前を変えた後に録り直すと、今の番組の分割の設定を引いて、新しい名前のフォルダに保存する（ADR 0039）。名前を変えたときに録り直しを
// 待っていたジョブ（retrying）も、先に保存したファイルを移すので、録り直しは移し先を上書きし、古いフォルダに取り残しが出ない。
func TestRenameThenRerecord(t *testing.T) {
	for _, order := range []string{"移してから録り直す", "録り直しを待つ間に名前を変える"} {
		t.Run(order, func(t *testing.T) {
			e := newMoveEnv(t)
			j := e.job(flatShow.Name)
			now := afterBroadcast.Add(time.Hour)
			e.clock.Set(now)
			if order == "録り直しを待つ間に名前を変える" {
				if err := e.store.Rerecord(e.ctx, j.ID, domain.MembershipFree, nil, now); err != nil {
					t.Fatal(err)
				}
			}
			e.renameShow(flatShow, "ふらっと（新）")
			if order == "移してから録り直す" {
				if !e.runDue() {
					t.Fatal("移さない")
				}
				if err := e.store.Rerecord(e.ctx, j.ID, domain.MembershipFree, nil, now); err != nil {
					t.Fatal(err)
				}
			}
			for e.runDue() {
			}
			want := filepath.Join(e.app.Library, "TBSラジオ", "ふらっと（新）", "ふらっと（新） 2026-10-07.m4a")
			got := e.job("ふらっと（新）")
			if got.State != domain.StateDone || got.OutputPath != want {
				t.Fatalf("録り直しの後: %+v, attempts = %+v", got, e.attempts(j.ID))
			}
			if n := e.fake.playlistCount(flat1.start); n != 2 {
				t.Errorf("入手の回数 = %d, want 2（録り直した）", n)
			}
			tags := e.media.tags[len(e.media.tags)-1]
			// 分割番組の track は MMDD×10（番組を引けなければ分割番組でないものとして MMDD になる）。
			if tags.Album != "ふらっと（新）" || tags.Track != 10070 {
				t.Errorf("録り直しのタグ = %+v", tags)
			}
			if _, err := os.Stat(filepath.Dir(j.OutputPath)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("古いフォルダが残っている: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(want))
			if err != nil {
				t.Fatal(err)
			}
			var m4a []string
			for _, en := range entries {
				if filepath.Ext(en.Name()) == ".m4a" {
					m4a = append(m4a, en.Name())
				}
			}
			if len(m4a) != 1 {
				t.Errorf("新しいフォルダの m4a = %v（別名を作った？）", m4a)
			}
		})
	}
}

// 本物の格納（builtin と、あれば ffmpeg）でも、保存先の移しはアルバムの名前を今の番組の名前に書き換え、音声のサンプルは元と同じ。
func TestMoveWithRealMedia(t *testing.T) {
	type real interface {
		Media
		AlbumRewriter
	}
	cases := []struct {
		name string
		m    func(t *testing.T) real
	}{
		{"builtin", func(t *testing.T) real { return media.Builtin{} }},
		{"ffmpeg", func(t *testing.T) real {
			for _, n := range []string{"ffmpeg", "ffprobe"} {
				if _, err := exec.LookPath(n); err != nil {
					t.Skipf("%s が無い", n)
				}
			}
			return media.New("", "")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.m(t)
			e := newFlowEnv(t)
			e.app.Media = m
			e.fake.set(func(f *fakeRadiko) { f.segment = synthADTS(234, 3) })
			if c.name == "ffmpeg" {
				seg := filepath.Join(t.TempDir(), "seg.aac")
				out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
					"-f", "lavfi", "-i", "sine=d=5", "-c:a", "aac", "-f", "adts", seg).CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				data, err := os.ReadFile(seg)
				if err != nil {
					t.Fatal(err)
				}
				e.fake.set(func(f *fakeRadiko) { f.segment = data })
			}
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			for e.runDue() {
			}
			j := e.job(flatShow.Name)
			if j.State != domain.StateDone {
				t.Fatalf("job = %+v, attempts = %+v", j, e.attempts(j.ID))
			}
			orig := filepath.Join(t.TempDir(), "orig.m4a")
			b, err := os.ReadFile(j.OutputPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(orig, b, 0o644); err != nil {
				t.Fatal(err)
			}
			e.renameShow(flatShow, "ふらっと（新）")
			if !e.runDue() {
				t.Fatal("移さない")
			}
			got := e.job("ふらっと（新）")
			if ms := e.outputMoves(j.ID); len(ms) != 1 || ms[0].Result != domain.OutputMoved {
				t.Fatalf("移しの履歴 = %+v", ms)
			}
			if err := m.SameAudio(e.ctx, orig, got.OutputPath); err != nil {
				t.Errorf("音声のサンプルが元と違う: %v", err)
			}
			after, err := os.ReadFile(got.OutputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(after, []byte("ふらっと（新）")) || bytes.Contains(after, []byte(flatShow.Name)) {
				t.Error("アルバムの名前が今の番組の名前になっていない（古い名前が残っている）")
			}
			if c.name == "ffmpeg" {
				out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=album,title", "-of", "default=nw=1",
					got.OutputPath).Output()
				if err != nil {
					t.Fatal(err)
				}
				if s := string(out); !strings.Contains(s, "album=ふらっと（新）") || !strings.Contains(s, "title=2026-10-07") {
					t.Errorf("tags = %q", s)
				}
			}
		})
	}
}
