package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// 設定の値は 1 行で置き換え、秘密は名前ごとに当てる（nil は消す。書かなかった秘密は残す）。
func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	body, secrets, err := s.Settings(ctx)
	if err != nil || body != "" || len(secrets) != 0 {
		t.Fatalf("まだ保存していない: %q, %v, %v", body, secrets, err)
	}
	if err := s.SaveSettings(ctx, `{"library":"/a"}`, map[string][]byte{"a": []byte("1"), "b": []byte("2")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, `{"library":"/b"}`, map[string][]byte{"a": nil, "c": []byte("3")}); err != nil {
		t.Fatal(err)
	}
	body, secrets, err = s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if body != `{"library":"/b"}` || !reflect.DeepEqual(secrets, map[string][]byte{"b": []byte("2"), "c": []byte("3")}) {
		t.Fatalf("got %q, %q", body, secrets)
	}
}
