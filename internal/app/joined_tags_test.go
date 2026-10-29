package app

import (
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
)

func TestJoinDistinct(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"A"}, "A"},
		{[]string{"A", "B"}, "A / B"},
		{[]string{" A ", "", "  ", "B"}, "A / B"},
		{[]string{"A", "B", " A"}, "A / B"},
		{[]string{"A、B", "A"}, "A、B / A"},
		{[]string{"", " "}, ""},
	}
	for _, c := range cases {
		if got := joinDistinct(c.in, " / "); got != c.want {
			t.Errorf("joinDistinct(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// artist は、ほかの回の先頭部分になっている出演者を向きを問わず落とす。
func TestJoinPerformers(t *testing.T) {
	const a, ax, ay = "A", "A　ゲスト：X", "A　ゲスト：Y"
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"同じ", []string{a, a}, a},
		{"後半だけゲスト", []string{a, ax}, ax},
		{"前半だけゲスト", []string{ax, a}, ax},
		{"ゲストが違う", []string{ax, ay}, ax + " / " + ay},
		{"別の出演者", []string{"A", "B"}, "A / B"},
		{"空を挟む", []string{a, "", ax}, ax},
		{"3 つ以上", []string{a, ax, ax + " ほか"}, ax + " ほか"},
		{"空白を落として比べる", []string{" A ", "A　ゲスト：X "}, ax},
	}
	for _, c := range cases {
		if got := joinPerformers(c.in); got != c.want {
			t.Errorf("%s: joinPerformers(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// 連結したジョブで前半だけにゲストがいる日も、artist にパーソナリティが 2 回並ばない。comment は完全に同じものだけをまとめる。
func TestSaveJoinedGuestOnlyInFirst(t *testing.T) {
	a, b := flat1, flat2
	a.pfm, a.info = "向井慧（パンサー）　ゲスト：安斉星来", "前半"
	b.pfm, b.info = "向井慧（パンサー）", "前半 ゲストあり"
	artist, comment := savedTags(t, a, news, b)
	if artist != a.pfm || comment != "前半\n\n前半 ゲストあり" {
		t.Errorf("artist = %q, comment = %q", artist, comment)
	}
}

func savedTags(t *testing.T, ps ...prog) (artist, comment string) {
	t.Helper()
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, ps...)
	e.putShows(flatShow)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if len(e.media.tags) != 1 {
		t.Fatalf("tags = %+v", e.media.tags)
	}
	return e.media.tags[0].Artist, e.media.tags[0].Comment
}

// 連結したジョブの artist と comment は、放送回ごとのものを開始時刻の順につなぐ。
func TestSaveJoinedArtistAndComment(t *testing.T) {
	a, b := flat1, flat2
	a.pfm, a.info = "向井", "前半"
	b.pfm, b.info = " ゲスト ", "後半"
	artist, comment := savedTags(t, a, news, b)
	if artist != "向井 / ゲスト" || comment != "前半\n\n後半" {
		t.Errorf("artist = %q, comment = %q", artist, comment)
	}
}

// 同じ出演者・説明は 1 つだけ残し、空は使わない。
func TestSaveJoinedDistinctAndEmpty(t *testing.T) {
	a, b := flat1, flat2
	a.pfm, a.info = "向井", "同じ説明"
	b.pfm, b.info = "向井", "同じ説明"
	artist, comment := savedTags(t, a, news, b)
	if artist != "向井" || comment != "同じ説明" {
		t.Errorf("artist = %q, comment = %q", artist, comment)
	}
	b.pfm = ""
	artist, _ = savedTags(t, a, news, b)
	if artist != "向井" {
		t.Errorf("空を除いた artist = %q", artist)
	}
}

// つないだ後で 250 バイトに切り詰める。
func TestSaveJoinedArtistTruncated(t *testing.T) {
	a, b := flat1, flat2
	a.pfm = strings.Repeat("あ", 50)
	b.pfm = strings.Repeat("い", 50)
	artist, _ := savedTags(t, a, news, b)
	if len(artist) > library.MaxArtistBytes || !strings.HasSuffix(artist, "…") || !strings.HasPrefix(artist, a.pfm+" / ") {
		t.Errorf("artist = %q (%d バイト)", artist, len(artist))
	}
}

// 録音期限を過ぎて取れた放送回だけを保存するときは、取れた放送回の出演者・説明だけをつなぐ。
func TestSaveJoinedOnlyFetchedAirings(t *testing.T) {
	a, b := flat1, flat2
	a.pfm, a.info = "向井", "前半"
	b.pfm, b.info = "ゲスト", "後半"
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, a, news, b)
	e.putShows(flatShow)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	if j = e.job(flatShow.Name); j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
	if len(e.media.tags) != 1 || e.media.tags[0].Artist != "向井" || e.media.tags[0].Comment != "前半" {
		t.Errorf("tags = %+v", e.media.tags)
	}
}
