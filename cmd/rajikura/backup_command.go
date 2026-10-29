package main

// DB のバックアップを受け取るサブコマンド（rajikura backup）。ほかの CLI と同じく、serve の HTTP API をトークンで呼ぶ
// （GET /api/backup）。DB は開かない（docs/adr/0017-backup-by-db-copy.md）。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/store"
)

// backupNow は、-o を省いたときのファイルの名前に使う時刻。テストで固定する。
var backupNow = time.Now

// backupCommand は serve からバックアップを受け取り、-o のファイル（省けば今のフォルダの rajikura-<日時>.db）に書く。
// 受け取り終えるまでは同じフォルダの一時のファイルに書き、終えたら名前を変える（途中で切れたファイルを残さない）。
// 書き先にファイルがあれば置き換える。
func backupCommand(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rajikura backup", flag.ContinueOnError)
	out := fs.String("o", "", "書き出すファイル（既定は今のフォルダの rajikura-<日時>.db）")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "使い方: rajikura backup [-o <file>]")
		return errUsage
	}
	path := *out
	if path == "" {
		path = store.BackupFileName(backupNow())
	}
	c, err := connect()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rajikura-backup-*.tmp")
	if err != nil {
		return err
	}
	n, err := c.Backup(ctx, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	fmt.Fprintf(stdout, "バックアップを %s に書いた（%d バイト。秘密の鍵は含まない）\n", path, n)
	return nil
}
