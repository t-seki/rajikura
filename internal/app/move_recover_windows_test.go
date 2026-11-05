package app

// 大文字・小文字を区別しないファイルシステムが要るテスト（大文字・小文字だけの移しの途中で止まった場合の回復。ADR 0039）。区別する
// ファイルシステム（Linux）では元と移し先が同じ実体にならないので、ファイル名の _windows でビルドを Windows に限る（Linux の CI は
// skip を失敗にするので、t.Skip しない）。区別するファイルシステムでの同じ入力は、move_recover_test.go の TestMoveCaseOnlyRename が
// 確かめる。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
)

// 移し先の控えのあるジョブを拾ったら、大文字・小文字だけの移しの途中で止まった状態からも、ADR 0039 の表のとおりに回復する（同じ
// 実体の行と、退避だけが残った行）。どの状態からも、どのファイルも上書きせず、消すのは音声が一致したと確かめた側だけ。
func TestMoveRecoveryStatesCaseOnly(t *testing.T) {
	if !caseInsensitiveFS(t) {
		t.Fatal("一時フォルダのファイルシステムが大文字・小文字を区別する（このテストは区別しないファイルシステムが要る）")
	}
	// setup は、名前を flat にして移し終えた後、FLAT に変えて移しの頼みと控えを付けたところ。
	setup := func(t *testing.T) (e *env, j domain.Job, from, to string, audio []byte) {
		t.Helper()
		e = newMoveEnv(t)
		lower := e.renameShow(flatShow, "flat")
		if !e.runDue() {
			t.Fatal("1 回目の移しをしない")
		}
		j = e.job("flat")
		audio, err := os.ReadFile(j.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		e.renameShow(lower, "FLAT")
		to = filepath.Join(e.app.Library, "TBSラジオ", "FLAT", "FLAT 2026-10-07.m4a")
		if err := e.store.SetOutputMoveTo(e.ctx, j.ID, to); err != nil {
			t.Fatal(err)
		}
		return e, j, j.OutputPath, to, audio
	}
	// files は、番組のフォルダ（古い表記）の folder.jpg のほかの名前（m4a と退避）。
	files := func(t *testing.T, e *env) string {
		t.Helper()
		var out []string
		for _, n := range dirNames(t, filepath.Join(e.app.Library, "TBSラジオ", "flat")) {
			if n != library.FolderImage {
				out = append(out, n)
			}
		}
		return strings.Join(out, "|")
	}
	t.Run("同じ実体で退避が無い", func(t *testing.T) {
		e, j, _, to, audio := setup(t)
		pickRecovered(t, e, "FLAT", j.ID, to, 2, domain.OutputMoved)
		hasFile(t, to, audio)
		if got := files(t, e); got != "FLAT 2026-10-07.m4a" {
			t.Errorf("番組のフォルダ = %s（頭から移し直して、新しい表記のファイル 1 つ）", got)
		}
	})
	t.Run("同じ実体で退避がある", func(t *testing.T) {
		e, j, from, to, audio := setup(t)
		if err := os.Rename(from, library.AsidePath(from)); err != nil {
			t.Fatal(err)
		}
		putFile(t, to, audio)
		pickRecovered(t, e, "FLAT", j.ID, to, 2, domain.OutputMoved)
		hasFile(t, to, audio)
		if got := files(t, e); got != "FLAT 2026-10-07.m4a" {
			t.Errorf("番組のフォルダ = %s（退避を消して、新しい表記のファイル 1 つ）", got)
		}
	})
	t.Run("同じ実体で退避があるが、音声が一致しない", func(t *testing.T) {
		e, j, from, to, audio := setup(t)
		aside := library.AsidePath(from)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		other := append([]byte("別の音声"), audio...)
		putFile(t, to, other)
		m := pickRecovered(t, e, "FLAT", j.ID, from, 2, domain.OutputMoveFailed)
		hasFile(t, to, other)
		hasFile(t, aside, audio)
		if !strings.Contains(m.Error, "両方を残した") {
			t.Errorf("Error = %q（両方を残した理由が無い）", m.Error)
		}
	})
	t.Run("退避した後、置く前に止まった", func(t *testing.T) {
		e, j, from, to, audio := setup(t)
		if err := os.Rename(from, library.AsidePath(from)); err != nil {
			t.Fatal(err)
		}
		pickRecovered(t, e, "FLAT", j.ID, to, 2, domain.OutputMoved)
		hasFile(t, to, audio)
		if got := files(t, e); got != "FLAT 2026-10-07.m4a" {
			t.Errorf("番組のフォルダ = %s（退避を戻して頭から移し直し、新しい表記のファイル 1 つ）", got)
		}
	})
}
