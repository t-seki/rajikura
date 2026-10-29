//go:build windows

package server

import (
	"path/filepath"
	"testing"
)

// UNC の共有の根は filepath.Dir が自分自身を返すので、parent を空にする条件（Dir(p) == p）に当たる。
func TestFoldersUNCShareRootIsItsOwnParent(t *testing.T) {
	for _, p := range []string{`\\nas\radio`, `\\nas`} {
		if filepath.Dir(p) != p {
			t.Errorf("filepath.Dir(%q) = %q, want itself", p, filepath.Dir(p))
		}
	}
	if got := filepath.Dir(`\\nas\radio\sub`); got != `\\nas\radio\` {
		t.Errorf("filepath.Dir of a share child = %q", got)
	}
}
