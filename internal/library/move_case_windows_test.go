package library

// 大文字・小文字を区別しないファイルシステムが要るテスト（大文字・小文字だけが違う移しの、元と移し先が同じ実体になる場合。ADR 0039）。
// 区別するファイルシステム（Linux）では同じ実体にならないので、ファイル名の _windows でビルドを Windows に限る（Linux の CI は skip を
// 失敗にするので、t.Skip しない）。区別するファイルシステムでの同じ入力は、move_recover_test.go の TestMoveCaseOnly が確かめる。

import (
	"os"
	"path/filepath"
	"testing"
)

// requireCaseInsensitive は、t.TempDir() のファイルシステムが大文字・小文字を区別すれば、テストを落とす（Windows の NTFS は区別しない
// ので、区別するならテストの前提が崩れている）。
func requireCaseInsensitive(t *testing.T) {
	t.Helper()
	if !caseInsensitive(t) {
		t.Fatal("一時フォルダのファイルシステムが大文字・小文字を区別する（このテストは区別しないファイルシステムが要る）")
	}
}

// 区別しないファイルシステムで、退避の名前にもう何かがあれば、大文字・小文字だけの移しはせず、どのファイルにも触らない（place も
// 呼ばない）。
func TestMoveCaseOnlyAsideTaken(t *testing.T) {
	requireCaseInsensitive(t)
	_, from, to := caseEnv(t)
	aside := AsidePath(from)
	writeFile(t, aside, "前の退避")
	if err := Move(from, to, func(p string) error { return os.WriteFile(p, []byte("new"), 0o644) }, noPlace(t)); err == nil {
		t.Fatal("退避の名前にファイルがあるのに移した")
	}
	for p, want := range map[string]string{from: "old", aside: "前の退避"} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", p, b, err, want)
		}
	}
	assertNoPartials(t, filepath.Dir(from))
}

// 途中で止まった大文字・小文字だけの移しの回復（ADR 0039 の表の、同じ実体の行と退避だけが残った行）。
func TestRecoverMoveCaseOnly(t *testing.T) {
	requireCaseInsensitive(t)
	setup := func(t *testing.T) (from, to, aside string) {
		t.Helper()
		_, from, to = caseEnv(t)
		return from, to, AsidePath(from)
	}
	t.Run("同じ実体で退避が無い", func(t *testing.T) {
		from, to, aside := setup(t)
		rec, err := RecoverMove(from, to, sameBytes)
		checkRecover(t, rec, err, recoverWant{rec: RecoverRedo, files: map[string]string{from: "old", aside: ""}})
	})
	t.Run("同じ実体で退避がある", func(t *testing.T) {
		from, to, aside := setup(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, "old")
		rec, err := RecoverMove(from, to, sameBytes)
		checkRecover(t, rec, err, recoverWant{rec: RecoverPlaced, files: map[string]string{to: "old", aside: ""}})
	})
	t.Run("同じ実体で退避があるが、音声が一致しない", func(t *testing.T) {
		from, to, aside := setup(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, "別の音声")
		rec, err := RecoverMove(from, to, sameBytes)
		checkRecover(t, rec, err, recoverWant{err: true, files: map[string]string{to: "別の音声", aside: "old"}})
	})
	t.Run("退避した後、置く前に止まった", func(t *testing.T) {
		from, to, aside := setup(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		rec, err := RecoverMove(from, to, sameBytes)
		checkRecover(t, rec, err, recoverWant{rec: RecoverRedo, files: map[string]string{from: "old", aside: ""}})
		if got := names(t, filepath.Dir(from)); len(got) != 1 || got[0] != "abc 2026-10-11.m4a" {
			t.Errorf("退避を元の名前に戻していない: %v", got)
		}
	})
}

// 大文字・小文字だけが違う移しの後、古いフォルダと新しいフォルダが同じ実体なら、片付けない（移したファイルのあるフォルダを消さない）。
func TestTidyOldFolderSameFolder(t *testing.T) {
	requireCaseInsensitive(t)
	station, from, _ := caseEnv(t)
	if err := os.Remove(from); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(station, "abc", FolderImage), "img")
	if ok, err := TidyOldFolder(filepath.Join(station, "abc"), filepath.Join(station, "ABC")); ok || err != nil {
		t.Errorf("TidyOldFolder = %v, %v", ok, err)
	}
	if b, err := os.ReadFile(filepath.Join(station, "abc", FolderImage)); err != nil || string(b) != "img" {
		t.Errorf("folder.jpg = %q, %v", b, err)
	}
}
