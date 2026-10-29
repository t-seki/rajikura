package app

import (
	"path/filepath"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// 放送回の番組名が番組の name と違えば、保存の title に足す（ADR 0007）。album とファイル名は name のまま。
func TestSaveAiringTitleInTitle(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	show := renamedSuppin()
	e.putRenamedSuppin(show)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(show.Name)
	if j.State != domain.StateDone || filepath.Base(j.OutputPath) != show.Name+" 2026-10-07.m4a" {
		t.Fatalf("job = %+v", j)
	}
	if len(e.media.tags) != 1 {
		t.Fatalf("tags = %+v", e.media.tags)
	}
	if got := e.media.tags[0]; got.Title != "2026-10-07 イモトアヤコのすっぴんしゃん" || got.Album != show.Name {
		t.Errorf("tags = %+v", got)
	}
}
