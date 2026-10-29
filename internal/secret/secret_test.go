package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 鍵のファイルが無ければ、初めて Seal したときに作る（Linux では 0600）。同じ鍵で読み直せば復号できる。
func TestSealOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "rajikura.key")
	b := Load(path)
	if !errors.Is(b.KeyError(), ErrNoKey) {
		t.Fatalf("KeyError = %v", b.KeyError())
	}
	if _, err := b.Open("x", []byte("whatever-whatever")); !errors.Is(err, ErrNoKey) {
		t.Fatalf("鍵が無いときの Open = %v", err)
	}
	sealed, err := b.Seal("notify.discord_webhook", "https://discord.invalid/TOPSECRET")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("TOPSECRET")) {
		t.Fatal("暗号文に値がそのまま入っている")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("鍵のファイルの権限 = %v", fi.Mode().Perm())
	}
	if b.KeyError() != nil {
		t.Fatalf("鍵を作った後の KeyError = %v", b.KeyError())
	}
	// 読み直した Box でも復号できる。
	got, err := Load(path).Open("notify.discord_webhook", sealed)
	if err != nil || got != "https://discord.invalid/TOPSECRET" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	// 別の秘密の名前では復号できない（暗号文を別の欄に写しても使えない）。
	if _, err := b.Open("notify.slack_webhook", sealed); err == nil {
		t.Fatal("別の名前で復号できた")
	}
	// 同じ値でも、毎回違う暗号文になる。
	again, err := b.Seal("notify.discord_webhook", "https://discord.invalid/TOPSECRET")
	if err != nil || bytes.Equal(again, sealed) {
		t.Fatalf("同じ暗号文: %v", err)
	}
}

// 鍵のファイルを読めない（権限の誤り）ときは、Seal もエラーにして鍵のファイルに触らない。読めない状態は権限を 000 に
// して作る。Windows（権限で読めない状態を chmod で作れない）と、権限によらず読める利用者（root など）では skip する。
func TestSealDoesNotReplaceUnreadableKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では chmod で読めない鍵のファイルを作れない")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rajikura.key")
	sealed, err := Load(path).Seal("a", "古い秘密")
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	b := Load(path)
	if b.KeyError() == nil {
		t.Skip("権限によらず鍵のファイルを読める利用者で動いている")
	}
	if err := b.KeyError(); errors.Is(err, ErrNoKey) || errors.Is(err, ErrMalformedKey) {
		t.Fatalf("KeyError = %v", err)
	}
	if _, err := b.Seal("b", "v"); err == nil {
		t.Fatal("鍵のファイルを読めないのに Seal できた")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, keyData) {
		t.Fatalf("読めない鍵のファイルを置き換えた: %v", err)
	}
	if v, err := Load(path).Open("a", sealed); err != nil || v != "古い秘密" {
		t.Fatalf("前の秘密を復号できない: %q, %v", v, err)
	}
}

// 起動の後に人が鍵のファイルを戻したら、Seal は新しい鍵を作らずに、戻した鍵を読み直して使う（上書きしない）。
func TestSealRereadsRestoredKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rajikura.key")
	b := Load(path)
	if !errors.Is(b.KeyError(), ErrNoKey) {
		t.Fatalf("KeyError = %v", b.KeyError())
	}
	// 別の場所で作った鍵と、それで暗号化した秘密を、鍵のファイルとして戻す。
	backup := filepath.Join(dir, "backup.key")
	old, err := Load(backup).Seal("a", "古い秘密")
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, keyData, 0o600); err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal("b", "新しい秘密")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, keyData) {
		t.Fatalf("戻した鍵のファイルを上書きした: %v", err)
	}
	r := Load(path)
	if v, err := r.Open("a", old); err != nil || v != "古い秘密" {
		t.Fatalf("戻した鍵で古い秘密を復号できない: %q, %v", v, err)
	}
	if v, err := r.Open("b", sealed); err != nil || v != "新しい秘密" {
		t.Fatalf("戻した鍵で新しい秘密を復号できない: %q, %v", v, err)
	}
}

// 合わない鍵では復号できない。鍵として読めないファイルは、Seal で作り直す（合わない鍵は作り直さずに使う）。
func TestWrongAndMalformedKey(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(filepath.Join(dir, "a.key")).Seal("n", "v")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "b.key")
	ob := Load(other)
	if _, err := ob.Seal("n", "w"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(other).Open("n", a); err == nil {
		t.Fatal("合わない鍵で復号できた")
	}

	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bb := Load(bad)
	if bb.KeyError() == nil || errors.Is(bb.KeyError(), ErrNoKey) {
		t.Fatalf("鍵として読めないファイルの KeyError = %v", bb.KeyError())
	}
	sealed, err := bb.Seal("n", "v")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Load(bad).Open("n", sealed); err != nil || got != "v" {
		t.Fatalf("作り直した鍵で復号できない: %q, %v", got, err)
	}
}
