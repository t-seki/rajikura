//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
)

// Windows のほかで tray を打つと、Windows だけだと言って止まる。
func TestTrayCommandOnlyOnWindows(t *testing.T) {
	for _, args := range [][]string{{"tray", "install", "-url", "http://127.0.0.1:8080"}, {"tray", "uninstall"}, {"tray"}} {
		code, out, errOut := runCmd(t, context.Background(), args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "rajikura tray: Windows だけで使える") {
			t.Errorf("%v: code = %d, stdout = %q, stderr = %q", args, code, out, errOut)
		}
	}
}
