package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// place が失敗したら、置かずに partial を消し、from を残す。
func TestMovePlaceFails(t *testing.T) {
	from, to := moveEnv(t)
	boom := errors.New("控えを書けない")
	err := Move(from, to, func(p string) error { return os.WriteFile(p, []byte("new"), 0o644) }, func() error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if b, _ := os.ReadFile(from); string(b) != "old" {
		t.Errorf("from = %q", b)
	}
	if _, err := os.Stat(filepath.Dir(to)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("移し先のフォルダ（partial）が残っている: %v", err)
	}
}

// caseInsensitive は、t.TempDir() のファイルシステムが名前の大文字・小文字を区別しないか（小文字の名前のファイルが、大文字の名前で
// 同じ実体に見えるか）。
func caseInsensitive(t *testing.T) bool {
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

// caseEnv は、局のフォルダの下に小文字の名前のフォルダと保存したファイルを置き、大文字の名前の移し先を返す。
func caseEnv(t *testing.T) (station, from, to string) {
	t.Helper()
	station = filepath.Join(t.TempDir(), "TBSラジオ")
	from = filepath.Join(station, "abc", "abc 2026-10-11.m4a")
	to = filepath.Join(station, "ABC", "ABC 2026-10-11.m4a")
	writeFile(t, from, "old")
	return station, from, to
}

func names(t *testing.T, dir string) []string {
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

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 大文字・小文字だけが違う移し（ADR 0039）。区別しないファイルシステムでは、移し先が元と同じ実体なので、元を退避の名前に付け直して
// 2 段で移す。ファイル名は新しい表記になり、フォルダは古い表記のまま、ファイルは 1 つだけ残る。区別するファイルシステムでは、同じ入力が
// 普通の移しになる（新しい表記のフォルダに置き、元を消す）。
func TestMoveCaseOnly(t *testing.T) {
	station, from, to := caseEnv(t)
	placed := 0
	err := Move(from, to, func(p string) error { return os.WriteFile(p, []byte("new"), 0o644) }, func() error {
		placed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if placed != 1 {
		t.Errorf("place の回数 = %d, want 1", placed)
	}
	if b, err := os.ReadFile(to); err != nil || string(b) != "new" {
		t.Errorf("to = %q, %v", b, err)
	}
	if caseInsensitive(t) {
		if got := names(t, station); len(got) != 1 || got[0] != "abc" {
			t.Errorf("局のフォルダ = %v, want [abc]（フォルダは古い表記のまま）", got)
		}
		if got := names(t, filepath.Join(station, "abc")); len(got) != 1 || got[0] != "ABC 2026-10-11.m4a" {
			t.Errorf("番組のフォルダ = %v, want [ABC 2026-10-11.m4a]（ファイル名は新しい表記、1 つだけ）", got)
		}
		return
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("from が残っている: %v", err)
	}
	if got := names(t, filepath.Join(station, "ABC")); len(got) != 1 || got[0] != "ABC 2026-10-11.m4a" {
		t.Errorf("新しいフォルダ = %v", got)
	}
}

// 区別しないファイルシステムで、退避の名前にもう何かがあれば、大文字・小文字だけの移しはせず、どのファイルにも触らない（place も
// 呼ばない）。
func TestMoveCaseOnlyAsideTaken(t *testing.T) {
	if !caseInsensitive(t) {
		t.Skip("ファイルシステムが大文字・小文字を区別する（退避の名前を使わない）")
	}
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

// 退避の名前は from と同じフォルダの rajikura-move.<元のファイル名>.orig で、CleanPartials が消す名前に当たらない（起動時の掃除で、
// 退避した元のファイルを消さない）。
func TestAsidePathNotCleaned(t *testing.T) {
	_, from, _ := caseEnv(t)
	aside := AsidePath(from)
	if filepath.Dir(aside) != filepath.Dir(from) || filepath.Base(aside) != "rajikura-move.abc 2026-10-11.m4a.orig" {
		t.Errorf("AsidePath = %q", aside)
	}
	writeFile(t, aside, "old")
	if n, err := CleanPartials(filepath.Dir(filepath.Dir(from))); n != 0 || err != nil {
		t.Errorf("CleanPartials = %d, %v", n, err)
	}
	if _, err := os.Stat(aside); err != nil {
		t.Errorf("退避を消した: %v", err)
	}
}

// sameBytes は、RecoverMove の same の偽物（中身のバイト列が同じなら一致）。
func sameBytes(a, b string) error {
	x, err := os.ReadFile(a)
	if err != nil {
		return err
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return err
	}
	if string(x) != string(y) {
		return errors.New("一致しない")
	}
	return nil
}

// 途中で止まった移しの回復（ADR 0039 の表）。どの状態からも、どのファイルも上書きせず、消すのは音声が一致したと確かめた側だけ。
func TestRecoverMove(t *testing.T) {
	type want struct {
		rec   Recovery
		err   bool
		files map[string]string // パス → 中身（"" は無いこと）
	}
	check := func(t *testing.T, rec Recovery, err error, w want) {
		t.Helper()
		if (err != nil) != w.err || err == nil && rec != w.rec {
			t.Errorf("RecoverMove = %v, %v; want %v, err %v", rec, err, w.rec, w.err)
		}
		for p, data := range w.files {
			b, rerr := os.ReadFile(p)
			switch {
			case data == "" && !errors.Is(rerr, os.ErrNotExist):
				t.Errorf("%s が残っている: %q, %v", filepath.Base(p), b, rerr)
			case data != "" && (rerr != nil || string(b) != data):
				t.Errorf("%s = %q, %v; want %q", filepath.Base(p), b, rerr, data)
			}
		}
	}
	t.Run("置く前に止まった", func(t *testing.T) {
		from, to := moveEnv(t)
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverRedo, files: map[string]string{from: "old", to: ""}})
	})
	t.Run("元を消した後、記録の前に止まった", func(t *testing.T) {
		from, to := moveEnv(t)
		writeFile(t, to, "old")
		if err := os.Remove(from); err != nil {
			t.Fatal(err)
		}
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverPlaced, files: map[string]string{from: "", to: "old"}})
	})
	t.Run("置いた後、元を消す前に止まった", func(t *testing.T) {
		from, to := moveEnv(t)
		writeFile(t, to, "old")
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverPlaced, files: map[string]string{from: "", to: "old"}})
	})
	t.Run("置いた後、元を消す前に止まったが、音声が一致しない", func(t *testing.T) {
		from, to := moveEnv(t)
		writeFile(t, to, "別の音声")
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{err: true, files: map[string]string{from: "old", to: "別の音声"}})
		if err != nil && !strings.Contains(err.Error(), "両方を残した") {
			t.Errorf("err = %v（両方を残した理由が無い）", err)
		}
	})
	t.Run("両方とも無い", func(t *testing.T) {
		from, to := moveEnv(t)
		if err := os.Remove(from); err != nil {
			t.Fatal(err)
		}
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverNoFile, files: map[string]string{from: "", to: ""}})
	})

	// ここから下は大文字・小文字だけの移しの途中（区別しないファイルシステムだけで起きる）。
	insensitive := caseInsensitive(t)
	caseOnly := func(t *testing.T) (from, to, aside string) {
		t.Helper()
		if !insensitive {
			t.Skip("ファイルシステムが大文字・小文字を区別する（大文字・小文字だけの移しは普通の移しになる）")
		}
		_, from, to = caseEnv(t)
		return from, to, AsidePath(from)
	}
	t.Run("同じ実体で退避が無い", func(t *testing.T) {
		from, to, aside := caseOnly(t)
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverRedo, files: map[string]string{from: "old", aside: ""}})
	})
	t.Run("同じ実体で退避がある", func(t *testing.T) {
		from, to, aside := caseOnly(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, "old")
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverPlaced, files: map[string]string{to: "old", aside: ""}})
	})
	t.Run("同じ実体で退避があるが、音声が一致しない", func(t *testing.T) {
		from, to, aside := caseOnly(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, "別の音声")
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{err: true, files: map[string]string{to: "別の音声", aside: "old"}})
	})
	t.Run("退避した後、置く前に止まった", func(t *testing.T) {
		from, to, aside := caseOnly(t)
		if err := os.Rename(from, aside); err != nil {
			t.Fatal(err)
		}
		rec, err := RecoverMove(from, to, sameBytes)
		check(t, rec, err, want{rec: RecoverRedo, files: map[string]string{from: "old", aside: ""}})
		if got := names(t, filepath.Dir(from)); len(got) != 1 || got[0] != "abc 2026-10-11.m4a" {
			t.Errorf("退避を元の名前に戻していない: %v", got)
		}
	})
}

// 大文字・小文字だけが違う移しの後、古いフォルダと新しいフォルダが同じ実体なら、片付けない（移したファイルのあるフォルダを消さない）。
func TestTidyOldFolderSameFolder(t *testing.T) {
	if !caseInsensitive(t) {
		t.Skip("ファイルシステムが大文字・小文字を区別する")
	}
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
