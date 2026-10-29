package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 録り直しの保存は、選んだ名前が前の保存先と同じパスなら上書きし、別名のファイルは増えない。2 回録り直しても失敗しない
// （Store のままだと 1 回目は別名、2 回目はエラーになる）。Windows（NTFS）でも rename で既にあるファイルを置き換えることを、
// CI の Windows の job で確かめる（ADR 0025）。
func TestStoreOverReplacesPrev(t *testing.T) {
	root := t.TempDir()
	res, err := Store(root, layout(), first, writeString("one"))
	if err != nil {
		t.Fatal(err)
	}
	prev := res.Path
	for _, content := range []string{"two", "three"} {
		got, err := StoreOver(root, layout(), first, prev, writeString(content))
		if err != nil {
			t.Fatalf("%s: %v", content, err)
		}
		if got.Path != prev || got.Renamed || !got.Replaced {
			t.Errorf("%s: Result = %+v, want Path=%q Replaced", content, got, prev)
		}
		if b, err := os.ReadFile(prev); err != nil || string(b) != content {
			t.Errorf("%s: 前の保存先の中身 = %q, %v", content, b, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "局", "番組"))
	if len(entries) != 1 {
		t.Errorf("entries = %v, want 1 file", entries)
	}
}

// 前の保存先が別名（" HHmm" つき）で、l の名前はほかのジョブのファイルが使っているときは、別名の前の保存先を上書きし、
// ほかのジョブのファイルは触らない。
func TestStoreOverReplacesPrevAltName(t *testing.T) {
	root := t.TempDir()
	if _, err := Store(root, layout(), first, writeString("other")); err != nil {
		t.Fatal(err)
	}
	res, err := Store(root, layout(), first, writeString("one"))
	if err != nil || !res.Renamed {
		t.Fatalf("別名で保存できない: %+v, %v", res, err)
	}
	got, err := StoreOver(root, layout(), first, res.Path, writeString("two"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != res.Path || !got.Renamed || !got.Replaced {
		t.Errorf("Result = %+v, want Path=%q Renamed Replaced", got, res.Path)
	}
	if b, _ := os.ReadFile(res.Path); string(b) != "two" {
		t.Errorf("別名の前の保存先 = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "局", "番組", "番組 2026-10-11.m4a")); string(b) != "other" {
		t.Errorf("ほかのジョブのファイルが変わった: %q", b)
	}
}

// 前の保存先が l の名前と違うパスなら、Store と同じ決まりで保存し、前のファイルも、同じ名前のほかのファイルも残す。
func TestStoreOverOtherPrevKeepsFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Store(root, layout(), first, writeString("other")); err != nil {
		t.Fatal(err)
	}
	prev := filepath.Join(root, "別の局", "番組", "番組 2026-10-11.m4a")
	if err := os.MkdirAll(filepath.Dir(prev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prev, []byte("prev"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := StoreOver(root, layout(), first, prev, writeString("new"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Renamed || got.Replaced {
		t.Errorf("Result = %+v, want Renamed, not Replaced", got)
	}
	if b, _ := os.ReadFile(prev); string(b) != "prev" {
		t.Errorf("前のファイル = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "局", "番組", "番組 2026-10-11.m4a")); string(b) != "other" {
		t.Errorf("同じ名前のほかのファイル = %q", b)
	}
}

// 書くのに失敗したら、前の保存先は消えずに残る。
func TestStoreOverWriteFailureKeepsPrev(t *testing.T) {
	root := t.TempDir()
	res, err := Store(root, layout(), first, writeString("one"))
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	_, err = StoreOver(root, layout(), first, res.Path, func(p string) error {
		if err := os.WriteFile(p, []byte("half"), 0o644); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(res.Path); string(b) != "one" {
		t.Errorf("前の保存先 = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "局", "番組"))
	if len(entries) != 1 {
		t.Errorf("entries = %v, want 1 file", entries)
	}
}
