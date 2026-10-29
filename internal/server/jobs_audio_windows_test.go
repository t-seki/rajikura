//go:build windows

package server

import (
	"strings"
	"testing"
)

// Windows では、大文字と小文字だけが違う library と保存先でも出す。
// Linux の CI は skip されたテストを失敗にするので、skip ではなく build tag で Windows に限る。
func TestAudioPathAllowedWindowsCase(t *testing.T) {
	lib := t.TempDir()
	path := strings.ToUpper(lib) + `\番組A\a.m4a`
	if !audioPathAllowed(lib, path) {
		t.Errorf("大文字と小文字だけが違うパスを断った: %q と %q", lib, path)
	}
	if !audioPathAllowed(`\\NAS\share`, `\\nas\share\x\a.m4a`) {
		t.Error("UNC の大文字と小文字だけの違いを断った")
	}
}
