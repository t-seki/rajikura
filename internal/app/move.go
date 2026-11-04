package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/media"
)

// AlbumRewriter は、保存した m4a のアルバムの名前（album のタグ）だけの書き換え（保存先の移し。ADR 0039）。*media.Tools と
// media.Builtin が満たす（設定 muxer に従う）。Media がこれを満たさなければ、保存先の移しは移せない（失敗として履歴に残す）。
type AlbumRewriter interface {
	// RewriteAlbum は、保存した m4a in の album のタグを album に置き換えたものを out に書く（media.Builtin.RewriteAlbum）。
	RewriteAlbum(ctx context.Context, in, out, album string) error
	// SameAudio は、m4a a と b の音声のサンプルが数とバイト列で同じか（違えば media.ErrAudioMismatch を包む）。
	SameAudio(ctx context.Context, a, b string) error
}

// moveOutputs は、保存先の移し（用語集。docs/adr/0039-saved-files-follow-show-rename.md）を 1 件だけ行い、行ったら true を返す。
// 頼まれたジョブ（Store.DueOutputMoves。保存先があり、録音中でないもの。番組の名前の変更と人の頼みが印を付ける）のうち、頼んだ時刻の
// 古いものの保存したファイルを、今の番組の名前のフォルダ・ファイル名（library.MovedPath）へ移す。タイムフリーの録音のループ（RunDue）が、
// 録るジョブを拾う前に呼ぶ（保存先のファイルを書く者を録音のループ 1 つに保つ。録り直しを待つジョブのファイルを先に移し、その後の
// 録り直しが、移し先を前の保存先として上書きできるようにする）。
//
//   - 移し先が今の保存先と同じなら、何もしない（履歴に残さず、頼みの印だけを消す）。
//   - 移すときは、同じフォルダの partial にアルバムの名前を今の番組の名前に書き換えたものを書き、音声のサンプルが元と一致することを
//     確かめてから移し先に置き、元のファイルを消す（library.Move）。ほかのタグ・画像・チャプターはファイルのまま。
//   - 保存先のパスが番組の名前の形でない・保存先にファイルが無い・移し先に同じ名前のファイルがある・移せないときは移さない（どの
//     ファイルも消さない。保存先の記録は変えない）。
//   - 移したら、保存先の記録を移し先に付け替え（Store.FinishOutputMove）、古いフォルダに残ったのが folder.jpg だけなら片付け
//     （library.TidyOldFolder）、ライブラリの再スキャンを頼む（rescan）。
//   - 結果は保存先の移しの履歴に 1 行残し（移すものが無かったときは残さない）、頼みの印を消す。状態・試行回数・欠落の印は変えず、試行を
//     起こさない。通知はしない（人の操作から起きる）。音声のサンプルが元と一致しなかったときは Error、ほかの移さなかった・移せなかった
//     ときは Warn でログに出す。
//
// DB に書けなければエラーを返す（移せなかったことはエラーにしない）。取り消されたら何も書かずに返す（頼みの印は残る）。
// 足したら直す: 保存の後の処理
func (a *App) moveOutputs(ctx context.Context, cur live) (bool, error) {
	jobs, err := a.Store.DueOutputMoves(ctx)
	if err != nil || len(jobs) == 0 {
		return false, err
	}
	job := jobs[0]
	log := a.log().With("job", job.ID, "show", job.ShowName, "day", job.Day, "part", job.Part)
	m := domain.OutputMove{JobID: job.ID, From: job.OutputPath}
	record := true
	to, ok := library.MovedPath(job.OutputPath, job.ShowName)
	switch {
	case !ok:
		m.Result = domain.OutputMoveNotShowPath
		log.Warn("保存先のパスが番組の名前の形でないので、保存先を移さない", "path", job.OutputPath)
	case filepath.Clean(to) == filepath.Clean(job.OutputPath):
		record = false
		log.Debug("保存先がもう今の番組の名前なので、移さない", "path", job.OutputPath)
	default:
		m.To = to
		err := a.moveOutput(ctx, cur, job, to)
		switch {
		case err == nil:
			m.Result = domain.OutputMoved
			log.Info("保存先を今の番組の名前に移した", "from", job.OutputPath, "to", to)
		case ctx.Err() != nil:
			return false, nil
		case errors.Is(err, library.ErrNoFile):
			m.Result = domain.OutputMoveNoFile
			log.Warn("保存先にファイルが無いので、保存先を移さない", "path", job.OutputPath)
		case errors.Is(err, library.ErrTargetExists):
			m.Result = domain.OutputMoveTargetExists
			log.Warn("移し先に同じ名前のファイルがあるので、保存先を移さない（どちらのファイルも消さない）", "from", job.OutputPath, "to", to)
		default:
			m.Result, m.Error = domain.OutputMoveFailed, err.Error()
			if errors.Is(err, media.ErrAudioMismatch) {
				log.Error("アルバムの名前を書き換えたファイルの音声が元と一致しないので、保存先を移さずに元のファイルを残す", "path", job.OutputPath, "err", err)
			} else {
				log.Warn("保存先を移せないので、元のファイルを残す", "from", job.OutputPath, "to", to, "err", err)
			}
		}
	}
	m.At = a.now()
	if err := a.Store.FinishOutputMove(context.WithoutCancel(ctx), m, record, job.MoveRequestedAt); err != nil {
		return false, err
	}
	if m.Result == domain.OutputMoved {
		old, now := filepath.Dir(m.From), filepath.Dir(to)
		if tidied, err := library.TidyOldFolder(old, now); err != nil {
			log.Warn("移した後の古いフォルダを片付けられない", "dir", old, "err", err)
		} else if tidied {
			log.Info("移した後に古いフォルダに残ったのが folder.jpg だけだったので、片付けた", "dir", old)
		}
		a.rescan(ctx, cur, log)
	}
	return ctx.Err() == nil, nil
}

// moveOutput は、job の保存したファイルを to へ移す（library.Move）。partial には、アルバムの名前をジョブの今の番組の名前（job.ShowName。
// Jellyfin のプリセットの album と同じ全文）に書き換えたものを書き、音声のサンプルが元と一致しなければ media.ErrAudioMismatch を包んだ
// エラーにする（移さない）。
func (a *App) moveOutput(ctx context.Context, cur live, job domain.Job, to string) error {
	return library.Move(job.OutputPath, to, func(partial string) error {
		rw, ok := cur.media.(AlbumRewriter)
		if !ok {
			return errors.New("設定 muxer の格納が、保存したファイルのアルバムの名前の書き換えに対応していない")
		}
		if err := rw.RewriteAlbum(ctx, job.OutputPath, partial, job.ShowName); err != nil {
			return err
		}
		return rw.SameAudio(ctx, job.OutputPath, partial)
	})
}
