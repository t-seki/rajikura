package library

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
)

func TestTruncateArtist(t *testing.T) {
	jp := func(n int) string { return strings.Repeat("あ", n) } // 3 バイト × n
	tests := []struct {
		name string
		in   string
		want string
		cut  bool
	}{
		{"空", "", "", false},
		{"242 バイトはそのまま", strings.Repeat("a", 242), strings.Repeat("a", 242), false},
		{"ちょうど 250 バイトはそのまま", strings.Repeat("a", 250), strings.Repeat("a", 250), false},
		{"251 バイトは切り詰める", strings.Repeat("a", 251), strings.Repeat("a", 247) + "…", true},
		{"303 バイトは切り詰める", strings.Repeat("a", 303), strings.Repeat("a", 247) + "…", true},
		{"マルチバイトは文字の途中で切らない", jp(100), jp(82) + "…", true}, // 246 + 3 = 249 バイト
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := TruncateArtist(tt.in)
			if got != tt.want || cut != tt.cut {
				t.Errorf("TruncateArtist = %q, %v; want %q, %v", got, cut, tt.want, tt.cut)
			}
			if len(got) > MaxArtistBytes || !utf8.ValidString(got) {
				t.Errorf("上限超過か不正な UTF-8: %d バイト", len(got))
			}
		})
	}
}

func TestJellyfinLayoutTruncatesArtist(t *testing.T) {
	long := strings.Repeat("a", 303)
	l := Jellyfin{}.Layout(Input{ShowName: "番組", Station: "局", BroadcastDay: day(2026, 10, 11), Performers: long, Description: "説明"})
	if l.Tags.Artist != strings.Repeat("a", 247)+"…" {
		t.Errorf("Artist = %q", l.Tags.Artist)
	}
	if l.Tags.Comment != "説明" {
		t.Errorf("Comment が変わっている: %q", l.Tags.Comment)
	}
}

func TestJellyfinLayoutFullArtist(t *testing.T) {
	in := func(p string) Input {
		return Input{ShowName: "番組", Station: "局", BroadcastDay: day(2026, 10, 11), Performers: p}
	}
	long := strings.Repeat("a", 303)
	tests := []struct {
		name   string
		j      Jellyfin
		in     string
		artist string
		cut    bool
	}{
		{"切り詰める・250 バイト以下", Jellyfin{}, strings.Repeat("a", 250), strings.Repeat("a", 250), false},
		{"切り詰める・251 バイト以上", Jellyfin{}, long, strings.Repeat("a", 247) + "…", true},
		{"全文・250 バイト以下", Jellyfin{FullArtist: true}, strings.Repeat("a", 250), strings.Repeat("a", 250), false},
		{"全文・251 バイト以上", Jellyfin{FullArtist: true}, long, long, false},
		{"切り詰める・空", Jellyfin{}, "", "", false},
		{"全文・空", Jellyfin{FullArtist: true}, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := tt.j.Layout(in(tt.in))
			if l.Tags.Artist != tt.artist || l.ArtistTruncated != tt.cut {
				t.Errorf("Artist = %q, ArtistTruncated = %v; want %q, %v", l.Tags.Artist, l.ArtistTruncated, tt.artist, tt.cut)
			}
		})
	}
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestJellyfinLayout(t *testing.T) {
	base := Input{
		ShowName:     "オールナイト",
		Station:      "TBSラジオ",
		BroadcastDay: day(2026, 10, 11),
		Performers:   "出演者",
		Description:  "説明",
	}
	tests := []struct {
		name  string
		mod   func(*Input)
		rel   string
		title string
		track int
	}{
		// ADR 0003 の表の 5 行。その日の放送回が 2 つ以上なら、連結しない番組のジョブにはパート番号が付く（domain.Expand）。
		{"分割番組でない・1 つ", func(*Input) {}, filepath.Join("TBSラジオ", "オールナイト", "オールナイト 2026-10-11.m4a"), "2026-10-11", 1011},
		{"分割番組でない・2 つ以上", func(in *Input) { in.Part = 2 }, filepath.Join("TBSラジオ", "オールナイト", "オールナイト 2026-10-11-2.m4a"), "2026-10-11 (2)", 1011},
		{"分割番組でつなぐ", func(in *Input) { in.Split = domain.SplitJoin }, filepath.Join("TBSラジオ", "オールナイト", "オールナイト 2026-10-11.m4a"), "2026-10-11", 10110},
		{"分割番組でつながない・2 つ以上", func(in *Input) { in.Split, in.Part = domain.SplitSeparate, 2 }, filepath.Join("TBSラジオ", "オールナイト", "オールナイト 2026-10-11-2.m4a"), "2026-10-11 (2)", 10112},
		{"分割番組でつながない・1 つ", func(in *Input) { in.Split = domain.SplitSeparate }, filepath.Join("TBSラジオ", "オールナイト", "オールナイト 2026-10-11.m4a"), "2026-10-11", 10110},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.mod(&in)
			l := Jellyfin{}.Layout(in)
			if got := l.RelPath(); got != tt.rel {
				t.Errorf("RelPath = %q, want %q", got, tt.rel)
			}
			want := media.Tags{
				AlbumArtist: "TBSラジオ",
				Album:       "オールナイト",
				Artist:      "出演者",
				Title:       tt.title,
				Date:        "2026-10-11",
				Disc:        2026,
				Track:       tt.track,
				Genre:       "ラジオ",
				Comment:     "説明",
			}
			if l.Tags != want {
				t.Errorf("Tags = %+v, want %+v", l.Tags, want)
			}
		})
	}
}

// 放送回の番組名が番組名と違えば title に足す（ADR 0007）。ファイル名・album・track は変えない。
func TestJellyfinLayoutAiringTitle(t *testing.T) {
	const ann0 = "オールナイトニッポン0(ZERO)"
	tests := []struct {
		name   string
		show   string
		part   int
		titles []string
		title  string
	}{
		{"同じ", ann0, 0, []string{ann0}, "2026-10-17"},
		{"違う", ann0, 0, []string{"オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～"},
			"2026-10-17 オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～"},
		{"前方一致でも違う", ann0, 0, []string{"オールナイトニッポン0(ZERO) feat. YAMANO BIG BAND JAZZ CONTEST"},
			"2026-10-17 オールナイトニッポン0(ZERO) feat. YAMANO BIG BAND JAZZ CONTEST"},
		{"パートあり・同じ", ann0, 1, []string{ann0}, "2026-10-17 (1)"},
		{"パートあり・違う", ann0, 1, []string{"○○のオールナイトニッポン0(ZERO)"}, "2026-10-17 (1) ○○のオールナイトニッポン0(ZERO)"},
		{"分割番組を連結", "パンサー向井の#ふらっと", 0, []string{"パンサー向井の#ふらっと (1)", "パンサー向井の#ふらっと (2)"}, "2026-10-17"},
		{"連結で違う放送回が 2 つ", "番組", 0, []string{"A特番", "番組", "B特番"}, "2026-10-17 A特番 / B特番"},
		{"連結で同じ番組名", "番組", 0, []string{"A特番 (1)", "A特番 (2)"}, "2026-10-17 A特番"},
		{"分の付く時間帯の印", "ますだおかだ岡田圭右とアンタッチャブル柴田英嗣のおかしば", 0,
			[]string{"ますだおかだ岡田圭右とアンタッチャブル柴田英嗣のおかしば 12時～12時55分"}, "2026-10-17"},
		{"末尾の印を落とすと空", "番組", 0, []string{"(1)"}, "2026-10-17"},
		{"放送回の番組名が無い", "番組", 0, nil, "2026-10-17"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Input{ShowName: tt.show, Station: "局", BroadcastDay: day(2026, 10, 17), Part: tt.part, AiringTitles: tt.titles}
			l := Jellyfin{}.Layout(in)
			if l.Tags.Title != tt.title {
				t.Errorf("Title = %q, want %q", l.Tags.Title, tt.title)
			}
			plain := Jellyfin{}.Layout(Input{ShowName: tt.show, Station: "局", BroadcastDay: day(2026, 10, 17), Part: tt.part})
			if l.RelPath() != plain.RelPath() || l.Tags.Album != plain.Tags.Album || l.Tags.Track != plain.Tags.Track {
				t.Errorf("title 以外が変わった: %+v, want %+v", l, plain)
			}
		})
	}
}

func TestJellyfinTrackSingleDigitMonth(t *testing.T) {
	l := Jellyfin{}.Layout(Input{ShowName: "s", Station: "k", BroadcastDay: day(2026, 1, 5), Part: 1, Split: domain.SplitSeparate})
	if l.Tags.Track != 1051 {
		t.Fatalf("track = %d, want 1051", l.Tags.Track)
	}
	l = Jellyfin{}.Layout(Input{ShowName: "s", Station: "k", BroadcastDay: day(2026, 1, 5)})
	if l.Tags.Track != 105 {
		t.Fatalf("track = %d, want 105", l.Tags.Track)
	}
}

func TestJellyfinSanitizesPath(t *testing.T) {
	l := Jellyfin{}.Layout(Input{ShowName: "A/B: C?", Station: "局*", BroadcastDay: day(2026, 10, 11)})
	want := filepath.Join("局_", "A_B_ C_", "A_B_ C_ 2026-10-11.m4a")
	if got := l.RelPath(); got != want {
		t.Fatalf("RelPath = %q, want %q", got, want)
	}
	// タグは整形しない。
	if l.Tags.Album != "A/B: C?" || l.Tags.AlbumArtist != "局*" {
		t.Fatalf("tags were sanitized: %+v", l.Tags)
	}
}

func TestJellyfinFolderAndFileUseSameShowName(t *testing.T) {
	l := Jellyfin{}.Layout(Input{ShowName: "番組. ", Station: "局", BroadcastDay: day(2026, 10, 11)})
	if want := filepath.Join("局", "番組", "番組 2026-10-11.m4a"); l.RelPath() != want {
		t.Fatalf("RelPath = %q, want %q", l.RelPath(), want)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := [][2]string{
		{"普通", "普通"},
		{`a<b>c:d"e/f\g|h?i*j`, "a_b_c_d_e_f_g_h_i_j"},
		{"a\x01b\x7f", "a_b_"},
		{"  spaced  ", "spaced"},
		{"dots...", "dots"},
		{"dot . ", "dot"},
		{"", "_"},
		{"   ", "_"},
		{"...", "_"},
		{"CON", "_CON"},
		{"nul", "_nul"},
		{"Com1.txt", "_Com1.txt"},
		{"LPT9", "_LPT9"},
		{"CONSOLE", "CONSOLE"},
		{"COM0", "COM0"},
	}
	for _, tt := range tests {
		if got := SanitizeName(tt[0]); got != tt[1] {
			t.Errorf("SanitizeName(%q) = %q, want %q", tt[0], got, tt[1])
		}
	}
}

func TestPathNameTruncates(t *testing.T) {
	// 3 バイト文字で境界が文字の途中に来る（219 は 3 の倍数なので、先頭に 1 バイト文字を足してずらす）。
	for _, prefix := range []string{"", "a", "ab"} {
		in := prefix + strings.Repeat("あ", 100)
		got := PathName(in)
		if len(got) > MaxPathNameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
			t.Errorf("prefix %q: PathName = %q (%d bytes)", prefix, got, len(got))
		}
	}
	if got := PathName(strings.Repeat("a", 219)); len(got) != 219 || strings.HasSuffix(got, "…") {
		t.Errorf("219 bytes was changed: %d bytes", len(got))
	}
	if got := PathName(strings.Repeat("a", 220)); len(got) != 219 || !strings.HasSuffix(got, "…") {
		t.Errorf("220 bytes: %d bytes", len(got))
	}
}

func TestStoreLongName(t *testing.T) {
	name := "あ" + strings.Repeat("い", 200) + "。" + strings.Repeat("a", 5)
	l := Jellyfin{}.Layout(Input{ShowName: name, Station: "局", BroadcastDay: day(2026, 10, 11), Part: 9})
	if l.Tags.Album != name {
		t.Fatal("album tag was truncated")
	}
	if filepath.Base(l.Dir) != PathName(name) || !strings.HasPrefix(l.Base, filepath.Base(l.Dir)+" ") {
		t.Fatalf("folder and file names differ: %q, %q", l.Dir, l.Base)
	}
	root := t.TempDir()
	var partial string
	res, err := Store(root, l, first, func(p string) error {
		partial = p
		return os.WriteFile(p, []byte("audio"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filepath.Base(partial)) > 255 {
		t.Errorf("partial name is %d bytes", len(filepath.Base(partial)))
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Error(err)
	}
}

func layout() Layout {
	return Jellyfin{}.Layout(Input{ShowName: "番組", Station: "局", BroadcastDay: day(2026, 10, 11)})
}

var first = time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC)

func writeString(s string) func(string) error {
	return func(p string) error { return os.WriteFile(p, []byte(s), 0o644) }
}

func TestStoreCommitsPartial(t *testing.T) {
	root := t.TempDir()
	var sawPartial string
	res, err := Store(root, layout(), first, func(p string) error {
		sawPartial = p
		return os.WriteFile(p, []byte("audio"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if b := filepath.Base(sawPartial); !strings.HasPrefix(b, "番組 2026-10-11.") || !strings.HasSuffix(b, ".partial.m4a") {
		t.Errorf("partial = %q", sawPartial)
	}
	if res.Renamed {
		t.Error("Renamed = true")
	}
	want := filepath.Join(root, "局", "番組", "番組 2026-10-11.m4a")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	if res.RelPath != layout().RelPath() {
		t.Errorf("RelPath = %q", res.RelPath)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "audio" {
		t.Errorf("final file: %q, %v", b, err)
	}
	if _, err := os.Stat(sawPartial); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial remains: %v", err)
	}
}

func TestStoreFinalFileIsWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	res, err := Store(t.TempDir(), layout(), first, func(p string) error {
		return os.WriteFile(p, []byte("x"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
}

func TestStoreWriteFailureLeavesNothing(t *testing.T) {
	root := t.TempDir()
	boom := errors.New("boom")
	_, err := Store(root, layout(), first, func(p string) error {
		if err := os.WriteFile(p, []byte("half"), 0o644); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "局", "番組"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover entries: %v", entries)
	}
}

func TestStoreCollisionUsesAltName(t *testing.T) {
	root := t.TempDir()
	if _, err := Store(root, layout(), first, writeString("one")); err != nil {
		t.Fatal(err)
	}
	res, err := Store(root, layout(), first, writeString("two"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Renamed {
		t.Error("Renamed = false")
	}
	want := filepath.Join(root, "局", "番組", "番組 2026-10-11 2300.m4a")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	// 元のファイルは上書きされない。
	orig := filepath.Join(root, "局", "番組", "番組 2026-10-11.m4a")
	if b, _ := os.ReadFile(orig); string(b) != "one" {
		t.Errorf("original overwritten: %q", b)
	}
}

func TestStoreCollisionTwiceIsError(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := Store(root, layout(), first, writeString("x")); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Store(root, layout(), first, writeString("three"))
	if err == nil {
		t.Fatal("want error")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "局", "番組"))
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want the 2 existing files only", entries)
	}
}

func TestStorePartialNameIsUniqueAndCleanable(t *testing.T) {
	root := t.TempDir()
	var names []string
	for i := 0; i < 2; i++ {
		_, err := Store(root, layout(), first, func(p string) error {
			names = append(names, p)
			if !strings.HasSuffix(p, ".partial.m4a") {
				t.Errorf("partial %q does not end with .partial.m4a", p)
			}
			return os.WriteFile(p, []byte("x"), 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if names[0] == names[1] {
		t.Fatalf("partial names are not unique: %q", names)
	}
	// 残骸は CleanPartials で拾える。
	stale := filepath.Join(root, "局", "番組", "番組 2026-10-11.abc.partial.m4a")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := CleanPartials(root); err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}

func TestCleanPartials(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "局", "番組")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "番組 2026-10-11.m4a")
	for _, f := range []string{
		keep,
		filepath.Join(dir, "番組 2026-10-11.partial.m4a"),
		filepath.Join(root, "x.partial.m4a"),
	} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CleanPartials(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed = %d, want 2", n)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("finished file removed: %v", err)
	}
}

func TestCleanPartialsMissingRoot(t *testing.T) {
	n, err := CleanPartials(filepath.Join(t.TempDir(), "none"))
	if err != nil || n != 0 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}
