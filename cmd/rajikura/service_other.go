//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/t-seki/rajikura/internal/config"
)

// serveHost は serve を動かす（Windows のほかは、サービスとしての動き方を持たない）。
func serveHost(ctx context.Context, boot config.Boot, opts serveOptions, stdout, stderr io.Writer) error {
	return serve(ctx, boot, opts, stdout, stderr)
}

// runServiceCommand は、Windows のほかでは使えないと言って止まる。
// 足したら直す: service の操作
func runServiceCommand(_ context.Context, _ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "rajikura service: Windows だけで使える（Windows のサービスとトレイを入れる・消すサブコマンド。docs/windows.md）")
	return 2
}
