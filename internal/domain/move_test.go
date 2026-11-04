package domain

import (
	"errors"
	"testing"
)

// 人が頼む保存先の移しは、保存先のあるジョブを状態によらず受け付け、録音中と保存先の無いジョブを断る（ADR 0039）。
func TestCheckOutputMove(t *testing.T) {
	for _, st := range []State{StateWaiting, StateRetrying, StateDone, StateExpired, StateSkipped} {
		if err := CheckOutputMove(Job{State: st, OutputPath: "/library/a.m4a"}); err != nil {
			t.Errorf("%s: %v", st, err)
		}
	}
	if err := CheckOutputMove(Job{State: StateRunning, OutputPath: "/library/a.m4a"}); !errors.Is(err, ErrOutputMoveRunning) {
		t.Errorf("running: %v", err)
	}
	if err := CheckOutputMove(Job{State: StateDone}); !errors.Is(err, ErrOutputMoveNoOutput) {
		t.Errorf("保存先が無い: %v", err)
	}
}
