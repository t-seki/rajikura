//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
)

// runTrayCommand は、Windows のほかでは使えないと言って止まる。
// 足したら直す: service の操作
func runTrayCommand(_ context.Context, _ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "rajikura tray: Windows だけで使える（Windows のトレイだけを入れる・消すサブコマンド。docs/windows.md）")
	return 2
}
