package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIconICOIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range iconStates {
		b := iconICO(st)
		le := binary.LittleEndian
		if le.Uint16(b[0:]) != 0 || le.Uint16(b[2:]) != 1 || int(le.Uint16(b[4:])) != len(iconSizes) {
			t.Fatalf("%v: ICONDIR が正しくない", st)
		}
		for i, n := range iconSizes {
			e := b[6+16*i:]
			if int(e[0]) != n || int(e[1]) != n || le.Uint16(e[6:]) != 32 {
				t.Fatalf("%v: %d 番目の大きさ", st, i)
			}
			size, off := le.Uint32(e[8:]), le.Uint32(e[12:])
			if int(off+size) > len(b) {
				t.Fatalf("%v: %d 番目がはみ出す", st, i)
			}
			if le.Uint32(b[off:]) != 40 || int(int32(le.Uint32(b[off+8:]))) != 2*n {
				t.Fatalf("%v: %d 番目の BITMAPINFOHEADER", st, i)
			}
		}
		seen[string(b)] = true
	}
	if len(seen) != len(iconStates) {
		t.Fatal("状態ごとに違う絵にする")
	}
}

func TestAppIconPNG(t *testing.T) {
	b, err := appIconPNG()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
}

func TestToastText(t *testing.T) {
	if got := cdata("a]]>b"); strings.Contains(strings.ReplaceAll(got, "]]]]><![CDATA[>", ""), "]]>") {
		t.Fatalf("CDATA を閉じる並びが残る: %q", got)
	}
	if got := toastLaunch("http://h/x?a=1&b=<2>"); got != "http://h/x?a=1&amp;b=&lt;2&gt;" {
		t.Fatalf("属性に直していない: %q", got)
	}
}

func TestAutostartCommand(t *testing.T) {
	exe := `C:\Program Files\rajikura\rajikura-tray.exe`
	if got := autostartCommand(exe, ""); got != `"C:\Program Files\rajikura\rajikura-tray.exe"` {
		t.Fatalf("-url なし: %s", got)
	}
	if got := autostartCommand(exe, "http://127.0.0.1:18080"); got != `"C:\Program Files\rajikura\rajikura-tray.exe" -url http://127.0.0.1:18080` {
		t.Fatalf("-url あり: %s", got)
	}
	if got := linkName("PC1", "taro"); got != `PC1\taro` {
		t.Fatalf("名前: %s", got)
	}
}

func TestLogFileRotatesKeepingOne(t *testing.T) {
	dir := t.TempDir()
	path := trayLogPath(dir)
	l, err := openLogFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("a", 59) + "\n")
	for i := 0; i < 5; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "tray.log,tray.log.1" {
		t.Fatalf("残すのは tray.log と tray.log.1 だけ: %v", names)
	}
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(filepath.Dir(path), n))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 100 {
			t.Errorf("%s が %d バイト", n, fi.Size())
		}
	}
}

func TestLogMaxIsOneMegabyte(t *testing.T) {
	if logMaxBytes != 1<<20 {
		t.Fatalf("1 MB で替える: %d", logMaxBytes)
	}
}
