//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
)

// Windows のほかで service を打つと、Windows だけだと言って止まる。
func TestServiceCommandOnlyOnWindows(t *testing.T) {
	for _, args := range [][]string{{"service", "install"}, {"service", "uninstall"}, {"service"}} {
		code, out, errOut := runCmd(t, context.Background(), args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "Windows だけで使える") {
			t.Errorf("%v: code = %d, stdout = %q, stderr = %q", args, code, out, errOut)
		}
	}
}
