//go:build !windows

package main

import (
	"fmt"
	"os"
)

// main は、Windows のほかでは使えないと言って止まる（トレイは Windows だけ。ADR 0023）。
func main() {
	fmt.Fprintln(os.Stderr, "rajikura-tray: Windows だけで使える（Windows のトレイ。docs/windows.md の「トレイ」）")
	os.Exit(2)
}
