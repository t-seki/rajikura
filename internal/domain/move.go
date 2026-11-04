package domain

import (
	"errors"
	"fmt"
	"time"
)

// 保存先の移し（用語集。docs/adr/0039-saved-files-follow-show-rename.md）。rajikura が、ジョブの保存したファイルを、今の番組の名前の
// フォルダ・ファイル名に移し、アルバムの名前も今の番組の名前にする。番組の名前を変えると起き（store.ReplaceShow が頼みの印を付ける）、
// 人もジョブ 1 件ずつ頼める（CheckOutputMove）。移すのは録音のループ（internal/app の moveOutputs）。

// ErrOutputMoveNoOutput は、保存先の無いジョブ（まだ保存していない）には保存先の移しを頼めないこと。errors.Is で見分ける。
var ErrOutputMoveNoOutput = errors.New("保存先が無いジョブは移せない")

// ErrOutputMoveRunning は、録音中（running）のジョブには保存先の移しを頼めないこと。errors.Is で見分ける。
var ErrOutputMoveRunning = errors.New("録音中のジョブは移せない")

// CheckOutputMove は、人が頼む保存先の移しを、ジョブ j に受け付けるかを決める（ADR 0039）。受け付けるのは保存先のあるジョブだけで、
// 状態は問わない（録り直しを待つ retrying、録り直しが録れなかった expired も受け付ける）。保存先が無ければ ErrOutputMoveNoOutput、
// 録音中（running）なら ErrOutputMoveRunning。
func CheckOutputMove(j Job) error {
	if j.OutputPath == "" {
		return ErrOutputMoveNoOutput
	}
	if j.State == StateRunning {
		return fmt.Errorf("%w（%s）", ErrOutputMoveRunning, j.State)
	}
	return nil
}

// OutputMoveResult は、保存先の移しの結果。
type OutputMoveResult string

const (
	// OutputMoved は、移した（保存先の記録を移し先に付け替えた）。
	OutputMoved OutputMoveResult = "moved"
	// OutputMoveTargetExists は、移し先に同じ名前のファイルがあるので移さなかった（どちらのファイルも消さず、上書きもしない）。
	OutputMoveTargetExists OutputMoveResult = "target_exists"
	// OutputMoveNoFile は、保存先にファイルが無いので移さなかった（保存先の記録は変えない）。
	OutputMoveNoFile OutputMoveResult = "no_file"
	// OutputMoveNotShowPath は、保存先のパスが番組の名前の形（{局}/{番組}/{番組} YYYY-MM-DD…）でないので移さなかった。
	OutputMoveNotShowPath OutputMoveResult = "not_show_path"
	// OutputMoveFailed は、移せなかった（アルバムの名前を書き換えられない、音声のサンプルが元と一致しない、書けないなど。元のファイルは
	// 残る）。
	OutputMoveFailed OutputMoveResult = "failed"
)

// OutputMove は、保存先の移しの履歴の 1 行（移しのたびに 1 行残す。試行の履歴・チャプターの書き込みの履歴とは別。ADR 0039）。
type OutputMove struct {
	ID    int64
	JobID int64
	At    time.Time
	// From は移す前の保存先、To は移し先（保存先のパスが番組の名前の形でなく、移し先を組めなかったときは空）。
	From, To string
	Result   OutputMoveResult
	// Error は移せなかった理由（OutputMoveFailed のときだけ）。
	Error string
}
