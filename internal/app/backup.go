package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// 自動のバックアップ（docs/backup.md の「自動のバックアップ」。docs/adr/0017-backup-by-db-copy.md）。
//
// 設定 backup_dir があれば、serve が BackupCheckInterval（既定 1 時間）ごとに見て、前に取れてから backupEvery（24 時間）が
// 過ぎていれば（まだ取れていなければ）、backup_dir に rajikura-<日時>.db で取る（store.Store.Backup と同じ写し。ログインの
// セッションと秘密の鍵は含まない）。決まった時刻には取らない（PC が眠っていても、起きた後の見回りで拾う）。
// 前に取れた日時は DB（app_state）に持ち、取る先を変えたら忘れる（Settings.Apply）。
// 取った後、名前が backupNamePattern に合うファイルだけを、新しいものから backup_keep 個残して消す（ほかのファイルには触らない）。
// 取れなかったらサービスの異常（notify.KindBackup）の始まりを通知し（続けて失敗している間は始まりの 1 回だけ）、
// 次に取れたら復旧を通知する。取れない異常が続いている間に backup_dir を空にしたときも、止めたことを復旧として通知する。
// 失敗したら前に取れた日時は変えないので、次の見回り（1 時間後）でまた取ろうとする。

const (
	// DefaultBackupCheckInterval は、バックアップを取る時期かを見る間。
	DefaultBackupCheckInterval = time.Hour
	// backupEvery は、前に取れてから、次を取るまでの間。
	backupEvery = 24 * time.Hour
)

// backupNamePattern は、世代として数えて消してよい、バックアップのファイルの名前（store.BackupFileName の形）。
var backupNamePattern = regexp.MustCompile(`^rajikura-\d{8}-\d{6}\.db$`)

// runBackups は ctx が取り消されるまで、起きてすぐと BackupCheckInterval ごとに BackupIfDue を呼ぶ。
// 同じ見回りで、再スキャンの頼む先が無くなっていないか（closeRescanIfGone。run.go）も見る。
func (a *App) runBackups(ctx context.Context) {
	t := time.NewTicker(cmp.Or(a.BackupCheckInterval, DefaultBackupCheckInterval))
	defer t.Stop()
	for {
		a.BackupIfDue(ctx)
		// 再スキャンの頼む先が無くなっていれば、録音の保存を待たずに、続いている再スキャンの失敗の異常を閉じる。
		a.closeRescanIfGone(ctx, a.current(), a.log())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// BackupIfDue は、設定の backup_dir があり、前に取れてから 24 時間が過ぎていれば（まだ取れていなければ）バックアップを取る。
// 取れなかったら、ログに出し、サービスの異常の始まりを通知する。取れたら、古い世代を消し、異常が続いていれば復旧を通知する。
func (a *App) BackupIfDue(ctx context.Context) {
	cur := a.current()
	if cur.backupDir == "" {
		// バックアップを取れない異常が続いている間に取る先を空にしたら、取れる日は来ないので、止めたことを復旧として閉じる。
		a.addAnomaly(ctx, a.log(), notify.KindRecovered, notify.KindBackup,
			"【復旧】バックアップの取る先（backup_dir）を空にして、自動のバックアップを止めた。取れなかった異常は終わりにするが、DB のバックアップは取れていない。")
		return
	}
	last, err := a.Store.LastBackupAt(ctx)
	if err != nil {
		if ctx.Err() == nil {
			a.log().Error("前にバックアップを取れた日時を DB から読めない", "err", err)
		}
		return
	}
	now := a.now()
	if !last.IsZero() && now.Sub(last) < backupEvery {
		return
	}
	log := a.log().With("backup_dir", cur.backupDir)
	path, err := a.takeBackup(ctx, cur.backupDir, now)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Error("バックアップを取れない（1 時間後にまた取ろうとする）", "err", err)
		a.addAnomaly(ctx, log, notify.KindBackup, notify.KindBackup,
			"【バックアップの失敗】自動のバックアップを取れない。DB を失ったときに戻せなくなる。バックアップの取る先（backup_dir）を確かめる。\nバックアップの取る先: "+
				cur.backupDir+"\nエラー: "+oneLine(err.Error()))
		return
	}
	log.Info("バックアップを取った", "file", path)
	if err := a.Store.SetLastBackupAt(ctx, now); err != nil {
		log.Warn("バックアップを取れた日時を DB に記録できない（次の見回りでまた取る）", "err", err)
	}
	if removed, err := pruneBackups(cur.backupDir, cur.backupKeep); err != nil {
		log.Warn("古いバックアップを消せない", "err", err)
	} else if len(removed) > 0 {
		log.Info("古いバックアップを消した", "removed", removed, "keep", cur.backupKeep)
	}
	a.addAnomaly(ctx, log, notify.KindRecovered, notify.KindBackup, "【復旧】自動のバックアップを取れた。")
}

// takeBackup は dir に now の名前でバックアップを取る。別の名前で取ってから改名するので、途中で止まっても、世代として数える
// 名前の不完全なファイルは残らない。取ったファイルのパスを返す。
func (a *App) takeBackup(ctx context.Context, dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("取る先 %s を作れない: %w", dir, err)
	}
	final := filepath.Join(dir, store.BackupFileName(now))
	if _, err := os.Stat(final); err == nil {
		return "", fmt.Errorf("バックアップの書き先 %s がもうある", final)
	}
	tmp := final + ".partial"
	// 前に途中で止まったときの残りは、このファイル（世代として数えない名前）なので消してよい。
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := a.Store.Backup(ctx, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("バックアップを %s に改名できない: %w", final, err)
	}
	return final, nil
}

// pruneBackups は dir の中の、名前が backupNamePattern に合う通常のファイルだけを、名前の新しいものから keep 個残して消す
// （名前の辞書順が取った時刻の順）。消したファイルの名前を返す。ほかのファイル・フォルダには触らない。
func pruneBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && backupNamePattern.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	if len(names) <= keep {
		return nil, nil
	}
	var removed []string
	var errs []error
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, n)
	}
	return removed, errors.Join(errs...)
}
