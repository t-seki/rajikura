package radiko

import (
	"context"
	"errors"
	"testing"
)

func TestAreaPrograms(t *testing.T) {
	f := newFake(t)
	got, err := f.client().AreaPrograms(context.Background(), "JP13", jst(2026, 10, 13, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "TBS" || got[0].Name != "TBSラジオ" || got[1].ID != "QRR" {
		t.Fatalf("局 = %+v", got)
	}
	if len(got[0].Programs) != 3 || len(got[1].Programs) != 3 {
		t.Fatalf("放送回の数 = %d, %d", len(got[0].Programs), len(got[1].Programs))
	}
	p := got[0].Programs[0]
	if p.StationID != "TBS" || p.Title != "番組A" || !p.Start.Equal(jst(2026, 10, 13, 5, 0, 0)) ||
		!p.End.Equal(jst(2026, 10, 13, 6, 30, 0)) || p.Performer != "出演者A　ゲスト：出演者X（芸人）" || p.TSInNG != "0" {
		t.Fatalf("放送回 = %+v", p)
	}
	if q := got[1].Programs[1]; q.StationID != "QRR" || q.Title != "番組D 6時～7時" {
		t.Fatalf("放送回 = %+v", q)
	}
}

func TestAreaProgramsUnexpectedStatusIsTemporary(t *testing.T) {
	f := newFake(t)
	_, err := f.client().AreaPrograms(context.Background(), "JP13", jst(2026, 10, 14, 0, 0, 0))
	if !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v", err)
	}
}
