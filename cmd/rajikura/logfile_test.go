package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "rajikura.log")
	// 10 バイトを超えそうになったら回し、今のファイルを含めて 3 本まで残す。
	r, err := openRotatingFile(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"aaaaa\n", "bbb\n", "ccccc\n", "ddddd\n", "eeeee\n"} {
		if _, err := r.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"rajikura.log":   "eeeee\n",
		"rajikura.log.1": "ddddd\n",
		"rajikura.log.2": "ccccc\n",
	}
	for name, w := range want {
		b, err := os.ReadFile(filepath.Join(dir, "logs", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != w {
			t.Errorf("%s = %q, want %q", name, b, w)
		}
	}
	// 3 本より古いもの（aaaaa と bbb の入ったファイル）は消える。
	if _, err := os.Stat(filepath.Join(dir, "logs", "rajikura.log.3")); !os.IsNotExist(err) {
		t.Errorf("rajikura.log.3 が残っている（err = %v）", err)
	}
}

func TestRotatingFileAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rajikura.log")
	if err := os.WriteFile(path, []byte("12345678\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 前の起動のファイルの大きさも数える（9 バイトに 3 バイトを足すと 10 を超えるので回す）。
	r, err := openRotatingFile(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("ab\n")); err != nil {
		t.Fatal(err)
	}
	// 1 回の書き込みが上限より大きくても、割らずに書く。
	long := strings.Repeat("x", 20) + "\n"
	if _, err := r.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(cur) != long || string(old) != "ab\n" {
		t.Errorf("今 = %q、.1 = %q", cur, old)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Errorf("rajikura.log.2 がある（err = %v）", err)
	}
}

// 回すときに今のファイルを閉じられなくても、閉じかけのハンドルに書かず、開き直して今のファイルに書き続ける。
func TestRotatingFileReopensWhenCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rajikura.log")
	r, err := openRotatingFile(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Write([]byte("aaaaaaaa\n")); err != nil {
		t.Fatal(err)
	}
	// 先に閉じておき、rotate の Close を失敗させる。
	r.f.Close()
	if _, err := r.Write([]byte("bbbb\n")); err != nil {
		t.Fatalf("閉じられなかった後の書き込みが失敗した: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "aaaaaaaa\nbbbb\n" {
		t.Errorf("rajikura.log = %q", b)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("rajikura.log.1 がある（err = %v）", err)
	}
}

// 回す途中で閉じた後に開き直せなくても、書けないままにならず、開けるようになった後の Write で開き直して書く。
func TestRotatingFileRecoversAfterReopenFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rajikura.log")
	r, err := openRotatingFile(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Write([]byte("aaaaaaaa\n")); err != nil {
		t.Fatal(err)
	}
	// rotate の Close を失敗させ（先に閉じておく）、開き直しも失敗させる（ファイルの場所をフォルダにする）。
	r.f.Close()
	if err := os.Rename(path, filepath.Join(dir, "moved.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Write([]byte("bbbb\n")); err == nil {
			t.Fatalf("%d 回目: 開けない場所に書けた", i+1)
		} else if err == os.ErrClosed { // Close を呼んだ後の返し方（1 回目は rotate の Close の誤りが os.ErrClosed を包むので、包んだものは許す）
			t.Fatalf("%d 回目: os.ErrClosed になった（Close を呼んでいないのに閉じた扱い）", i+1)
		}
	}
	// 開けるようになったら、次の Write で開き直して書く。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("cccc\n")); err != nil {
		t.Fatalf("開けるようになった後も書けない: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "cccc\n" {
		t.Errorf("rajikura.log = %q", b)
	}
}

func TestRotatingFileWriteAfterClose(t *testing.T) {
	r, err := openRotatingFile(filepath.Join(t.TempDir(), "x.log"), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := r.Write([]byte("x")); err == nil {
		t.Error("閉じた後に書けた")
	}
}
