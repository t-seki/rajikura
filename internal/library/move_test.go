package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 移し先は、番組のフォルダ名とファイル名の先頭の番組の名前だけを、新しい名前（PathName で整えたもの）に置き換える。局のフォルダ・放送日・
// パートの番号・別名の HHmm はそのまま。番組の名前の形でないパスは false（ADR 0039）。
func TestMovedPath(t *testing.T) {
	root := filepath.Join("lib", "TBSラジオ")
	for _, c := range []struct {
		path, show, want string
	}{
		{filepath.Join(root, "VIVANT悪役会議室", "VIVANT悪役会議室 2026-10-11.m4a"), "VIVANT 悪役会議室",
			filepath.Join(root, "VIVANT 悪役会議室", "VIVANT 悪役会議室 2026-10-11.m4a")},
		{filepath.Join(root, "旧", "旧 2026-10-11-2.m4a"), "新", filepath.Join(root, "新", "新 2026-10-11-2.m4a")},
		{filepath.Join(root, "旧", "旧 2026-10-11 0830.m4a"), "新", filepath.Join(root, "新", "新 2026-10-11 0830.m4a")},
		{filepath.Join(root, "旧", "旧 2026-10-11-1 2330.m4a"), "新", filepath.Join(root, "新", "新 2026-10-11-1 2330.m4a")},
		// 使えない文字は PathName で整える（タグの名前は全文のまま。タグは呼び出し側が書く）。
		{filepath.Join(root, "旧", "旧 2026-10-11.m4a"), "A/B: C", filepath.Join(root, "A_B_ C", "A_B_ C 2026-10-11.m4a")},
		// 名前が同じなら、同じパス。
		{filepath.Join(root, "同じ", "同じ 2026-10-11.m4a"), "同じ", filepath.Join(root, "同じ", "同じ 2026-10-11.m4a")},
	} {
		got, ok := MovedPath(c.path, c.show)
		if !ok || got != c.want {
			t.Errorf("MovedPath(%q, %q) = %q, %v; want %q", c.path, c.show, got, ok, c.want)
		}
	}
	for _, p := range []string{
		filepath.Join(root, "フォルダ", "別の名前 2026-10-11.m4a"), // ファイル名がフォルダ名で始まらない
		filepath.Join(root, "旧", "旧 2026-10-11.mp3"),       // 拡張子が違う
		filepath.Join(root, "旧", "旧 10-11.m4a"),            // 放送日の形でない
		filepath.Join(root, "旧", "旧 2026-10-11 副題.m4a"),    // 後ろに知らないものがある
		filepath.Join(root, "旧", "旧.m4a"),
		"旧 2026-10-11.m4a",
	} {
		if got, ok := MovedPath(p, "新"); ok {
			t.Errorf("MovedPath(%q) = %q, true; want false", p, got)
		}
	}
}

// moveEnv は、局のフォルダの下に古い名前のフォルダと保存したファイルを置く。
func moveEnv(t *testing.T) (from, to string) {
	t.Helper()
	station := filepath.Join(t.TempDir(), "TBSラジオ")
	from = filepath.Join(station, "旧", "旧 2026-10-11.m4a")
	to = filepath.Join(station, "新", "新 2026-10-11.m4a")
	if err := os.MkdirAll(filepath.Dir(from), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(from, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return from, to
}

// Move は write に to と同じフォルダ（無ければ作る）の partial を渡し、書いたものを to に置いて from を消す。
func TestMove(t *testing.T) {
	from, to := moveEnv(t)
	var partial string
	err := Move(from, to, func(p string) error {
		partial = p
		if filepath.Dir(p) != filepath.Dir(to) || !strings.HasSuffix(p, partialExt) {
			t.Errorf("partial = %q", p)
		}
		return os.WriteFile(p, []byte("new"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(to); err != nil || string(b) != "new" {
		t.Errorf("to = %q, %v", b, err)
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("from が残っている: %v", err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial が残っている: %v", err)
	}
}

// 移し先にファイルがある・保存先にファイルが無い・write が失敗したときは、どのファイルも消さず、上書きもしない。作った移し先の
// フォルダは、空なら消す。
func TestMoveRefuses(t *testing.T) {
	t.Run("移し先にファイルがある", func(t *testing.T) {
		from, to := moveEnv(t)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, []byte("hand"), 0o644); err != nil {
			t.Fatal(err)
		}
		called := false
		err := Move(from, to, func(string) error { called = true; return nil })
		if !errors.Is(err, ErrTargetExists) || called {
			t.Errorf("err = %v, called = %v", err, called)
		}
		for p, want := range map[string]string{from: "old", to: "hand"} {
			if b, err := os.ReadFile(p); err != nil || string(b) != want {
				t.Errorf("%s = %q, %v; want %q", p, b, err, want)
			}
		}
	})
	t.Run("書いている間に移し先にファイルができた", func(t *testing.T) {
		from, to := moveEnv(t)
		err := Move(from, to, func(p string) error { return os.WriteFile(to, []byte("hand"), 0o644) })
		if !errors.Is(err, ErrTargetExists) {
			t.Errorf("err = %v", err)
		}
		if b, _ := os.ReadFile(to); string(b) != "hand" {
			t.Errorf("移し先を上書きした: %q", b)
		}
		if b, _ := os.ReadFile(from); string(b) != "old" {
			t.Errorf("from = %q", b)
		}
		assertNoPartials(t, filepath.Dir(to))
	})
	t.Run("保存先にファイルが無い", func(t *testing.T) {
		from, to := moveEnv(t)
		if err := os.Remove(from); err != nil {
			t.Fatal(err)
		}
		if err := Move(from, to, func(string) error { return nil }); !errors.Is(err, ErrNoFile) {
			t.Errorf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Dir(to)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("移し先のフォルダを作った: %v", err)
		}
	})
	t.Run("write が失敗した", func(t *testing.T) {
		from, to := moveEnv(t)
		boom := errors.New("boom")
		if err := Move(from, to, func(p string) error { return boom }); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		if b, _ := os.ReadFile(from); string(b) != "old" {
			t.Errorf("from = %q", b)
		}
		if _, err := os.Stat(filepath.Dir(to)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("作った移し先のフォルダが残っている: %v", err)
		}
	})
}

func assertNoPartials(t *testing.T, dir string) {
	t.Helper()
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), partialExt) {
			t.Errorf("partial が残っている: %s", e.Name())
		}
	}
}

// 古いフォルダに残ったのが folder.jpg だけなら、新しいフォルダへ移し（あれば古い方を消し）、古いフォルダを消す。何も残っていなくても
// 消す。ほかのファイルがあれば触らない。
func TestTidyOldFolder(t *testing.T) {
	setup := func(t *testing.T, oldFiles, newFiles map[string]string) (string, string) {
		t.Helper()
		base := t.TempDir()
		old, now := filepath.Join(base, "旧"), filepath.Join(base, "新")
		for dir, files := range map[string]map[string]string{old: oldFiles, now: newFiles} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return old, now
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	t.Run("folder.jpg だけ", func(t *testing.T) {
		old, now := setup(t, map[string]string{FolderImage: "old-img"}, map[string]string{"新 2026-10-11.m4a": "x"})
		if ok, err := TidyOldFolder(old, now); !ok || err != nil {
			t.Fatalf("TidyOldFolder = %v, %v", ok, err)
		}
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("古いフォルダが残っている: %v", err)
		}
		if got := read(filepath.Join(now, FolderImage)); got != "old-img" {
			t.Errorf("新しいフォルダの folder.jpg = %q", got)
		}
	})
	t.Run("新しいフォルダにも folder.jpg がある", func(t *testing.T) {
		old, now := setup(t, map[string]string{FolderImage: "old-img"}, map[string]string{FolderImage: "new-img"})
		if ok, err := TidyOldFolder(old, now); !ok || err != nil {
			t.Fatalf("TidyOldFolder = %v, %v", ok, err)
		}
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("古いフォルダが残っている: %v", err)
		}
		if got := read(filepath.Join(now, FolderImage)); got != "new-img" {
			t.Errorf("新しいフォルダの folder.jpg を上書きした: %q", got)
		}
	})
	t.Run("空", func(t *testing.T) {
		old, now := setup(t, nil, nil)
		if ok, err := TidyOldFolder(old, now); !ok || err != nil {
			t.Fatalf("TidyOldFolder = %v, %v", ok, err)
		}
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("古いフォルダが残っている: %v", err)
		}
	})
	t.Run("ほかのファイルがある", func(t *testing.T) {
		old, now := setup(t, map[string]string{FolderImage: "old-img", "旧 2026-10-04.m4a": "keep"}, nil)
		if ok, err := TidyOldFolder(old, now); ok || err != nil {
			t.Fatalf("TidyOldFolder = %v, %v", ok, err)
		}
		if read(filepath.Join(old, FolderImage)) != "old-img" || read(filepath.Join(old, "旧 2026-10-04.m4a")) != "keep" {
			t.Error("古いフォルダに触った")
		}
		if _, err := os.Stat(filepath.Join(now, FolderImage)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("folder.jpg を写した: %v", err)
		}
	})
}
