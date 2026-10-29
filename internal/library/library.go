// Package library は保存先の名前（パス）、m4a のタグ、ファイルの置き方を決める。
//
// 命名とタグは [Preset] が決める。今あるのは Jellyfin プリセットだけ（ADR 0002。title は、放送回の番組名が
// 番組の名前と違えば足す。ADR 0007）。置き方は [Store]
// （partial に書いてから確定、衝突時は別名）と、録り直しで前の保存先を上書きする [StoreOver]（ADR 0025）と
// [CleanPartials]（起動時の残骸の掃除）。番組のフォルダの画像 folder.jpg は [StoreFolderImage]
// が置く（保存のたびに上書き）。
package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
)

const (
	ext        = ".m4a"
	partialExt = ".partial.m4a"
)

// Input は保存先の名前とタグを計算するための入力。
type Input struct {
	ShowName     string           // 番組名（保存先の名前）
	Station      string           // 局の表示名（radiko の局名。空なら局 ID）
	BroadcastDay time.Time        // 放送日（5 時区切り済み。年月日だけを使う）
	Part         int              // パート番号。0 ならパートなし
	Split        domain.SplitMode // 番組の分割番組の設定（split。ADR 0003）。track の決め方に使う
	// Performers は出演者。連結したジョブでは、保存する放送回の出演者を開始時刻の順に " / " でつないだもの
	// （前後の空白を落とし、空と、同じものと、ほかの回の先頭部分になっているものを除く。つなぐのは呼び出し側）。
	// 全文を渡す。artist には TruncateArtist で MaxArtistBytes 以内に切り詰めて書く。Jellyfin.FullArtist なら全文。
	Performers string
	// Description は放送回の説明。連結したジョブでは、保存する放送回の説明を開始時刻の順に空行でつないだもの
	// （前後の空白を落とし、空と重複を除く。つなぐのは呼び出し側）。
	Description string
	// AiringTitles は、保存する放送回の番組表の番組名（放送回の番組名）を開始時刻の順に並べたもの。
	// Show と違う番組名を title に足す（ADR 0007）。
	AiringTitles []string
}

// Layout は保存先のルートからの相対的な置き場所とタグ。
type Layout struct {
	Dir  string     // ルートからのフォルダ（局/番組）。整形済み
	Base string     // 拡張子を除くファイル名。整形済み
	Tags media.Tags // m4a に書くタグ
	// ArtistTruncated は、artist に出演者を切り詰めて書いたか。
	ArtistTruncated bool
}

// RelPath はルートからの相対パス（拡張子つき）を返す。
func (l Layout) RelPath() string {
	return filepath.Join(l.Dir, l.Base+ext)
}

// Preset は命名とタグの付け方。MVP では Jellyfin だけを作る。
type Preset interface {
	Layout(in Input) Layout
}

// Jellyfin は Jellyfin の音楽ライブラリ向けのプリセット（ADR 0002。title は、放送回の番組名が番組の名前と
// 違えば足す。ADR 0007）。
type Jellyfin struct {
	// FullArtist が true なら、artist に出演者を切り詰めずに全文で書く（設定の truncate_artist = false）。
	// 零値は切り詰める。
	FullArtist bool
}

// Layout は Preset を実装する。
//
// パスは {局}/{番組}/{番組} YYYY-MM-DD.m4a、パートがあれば放送日の後に -N を付ける。
// フォルダとファイルで、整形した同じ番組名を使う（長ければ [PathName] で切り詰める。タグは全文）。
// track は、分割番組（つなぐ・つながない）の番組なら、パートがあれば MMDD×10+N、無ければ MMDD×10（その日の放送回が
// 1 つだけでも）。つないだ日とつながなかった日のファイルが、同じアルバムで放送日順に並ぶため（ADR 0002）。分割番組でない番組は、
// パートがあっても MMDD（まれに 2 回あった日だけが年末より後ろに並ばないように。ADR 0003）。
// artist は出演者を [TruncateArtist] で [MaxArtistBytes] 以内に切り詰めたもの（FullArtist なら全文）。
// title は YYYY-MM-DD（パートがあれば YYYY-MM-DD (N)）で、放送回の番組名が番組名と違えば、
// その後に空白を挟んで放送回の番組名を足す（[AiringTitlesSuffix]。ADR 0007）。
func (j Jellyfin) Layout(in Input) Layout {
	d := in.BroadcastDay
	date := d.Format("2006-01-02")
	title := date
	show := PathName(in.ShowName)
	base := show + " " + date
	if in.Part > 0 {
		title = fmt.Sprintf("%s (%d)", date, in.Part)
		base = fmt.Sprintf("%s-%d", base, in.Part)
	}
	if extra := AiringTitlesSuffix(in.ShowName, in.AiringTitles); extra != "" {
		title += " " + extra
	}
	mmdd := int(d.Month())*100 + d.Day()
	track := mmdd
	if in.Split != domain.SplitNone {
		track = mmdd*10 + in.Part
	}
	artist, cut := in.Performers, false
	if !j.FullArtist {
		artist, cut = TruncateArtist(in.Performers)
	}
	return Layout{
		Dir:             filepath.Join(PathName(in.Station), show),
		Base:            base,
		ArtistTruncated: cut,
		Tags: media.Tags{
			AlbumArtist: in.Station,
			Album:       in.ShowName,
			Artist:      artist,
			Title:       title,
			Date:        date,
			Disc:        d.Year(),
			Track:       track,
			Genre:       "ラジオ",
			Comment:     in.Description,
		},
	}
}

// MaxArtistBytes は m4a の artist に書く出演者のバイト数の上限。
// Jellyfin は artist の文字列から人物・アーティストのフォルダ名を作り、ext4 のファイル名は
// 255 バイトまでなので、超えると PathTooLongException で人物・アーティストのメタデータを作れない。
// 255 以下は今通っているので変えず、Jellyfin がそのフォルダ名をほかのファイル名にも使うか
// 確かめていない分の余裕として 5 バイト取って 250 にする。
const MaxArtistBytes = 250

const artistEllipsis = "…"

// TruncateArtist は出演者 s が MaxArtistBytes を超えるとき、文字の途中で切らずに切り詰めて
// 末尾に … を付け、… を含めて MaxArtistBytes 以内にする。超えなければそのまま返す。
// 切り詰めたら true を返す。
func TruncateArtist(s string) (string, bool) {
	if len(s) <= MaxArtistBytes {
		return s, false
	}
	n := MaxArtistBytes - len(artistEllipsis)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + artistEllipsis, true
}

// AiringTitlesSuffix は、放送回の番組名 titles のうち番組名 show と違うものを、title に足す形にする。
// 同じかどうかは domain.SameTitle（末尾の印を落として正規化したうえでの完全一致）で決める。
// 足す番組名は原文どおりで、末尾の印と前後の空白だけを落とす（domain.TrimTrailingMark）。
// 落として空になるものは足さない。重複（正規化して同じもの）は最初のものだけを残し、
// titles の順に " / " でつなぐ。足すものが無ければ空。
func AiringTitlesSuffix(show string, titles []string) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range titles {
		if domain.SameTitle(t, show) {
			continue
		}
		disp := domain.TrimTrailingMark(t)
		key := domain.NormalizeTitle(domain.StripTrailingMark(t))
		if disp == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, disp)
	}
	return strings.Join(out, " / ")
}

// MaxPathNameBytes はパスに入れる名前（局名、番組の名前）のバイト数の上限。
// ext4 のファイル名は 255 バイトまでで、いちばん長くなる一時ファイルの名前
// `名前 YYYY-MM-DD-N.<乱数>.partial.m4a` の名前以外の部分は 36 バイト
// （` YYYY-MM-DD` 11 + `-N` 2 + `.` 1 + 乱数 最大 10 + `.partial.m4a` 12。
// パート番号は 1 桁までが前提）なので、255 − 36 = 219 にする。
const MaxPathNameBytes = 219

// PathName は [SanitizeName] で整えた名前を、MaxPathNameBytes を超えるとき、文字の途中で
// 切らずに切り詰めて末尾に … を付け、… を含めて MaxPathNameBytes 以内にする。
// … で終わるので、末尾の空白・ピリオドの整形は崩れない。超えなければそのまま返す。
func PathName(s string) string {
	s = SanitizeName(s)
	if len(s) <= MaxPathNameBytes {
		return s
	}
	n := MaxPathNameBytes - len(artistEllipsis)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + artistEllipsis
}

// SanitizeName はファイル名・フォルダ名として使えるように整える。
// Windows で使えない文字（<>:"/\|?* と制御文字）は _ にし、前後の空白と末尾のピリオドを
// 除く。空になったら _ にする。Windows の予約名（CON、NUL、COM1 など。大文字・小文字は
// 区別せず、拡張子があっても予約名）なら先頭に _ を付ける。
func SanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	stem, _, _ := strings.Cut(s, ".")
	if reservedNames[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return "_" + s
	}
	return s
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// Result は Store の結果。
type Result struct {
	Path    string // 確定したファイルのパス（root を含む）
	RelPath string // root からの相対パス
	Renamed bool   // 同名のファイルがあり、別名（HHmm つき）にしたか
	// Replaced は、前の保存先（StoreOver の prev）と同じパスの、既にあるファイルを上書きしたか。
	Replaced bool
}

// Store は l の場所に m4a を置く。write には partial のパス（同じフォルダの、ユニークな
// 接尾辞つきの *.partial.m4a）を渡すので、そこへ書かせる。write が成功したらリネームして確定し、失敗したら partial を消す。
// 同じ名前のファイルが既にあれば上書きせず、firstStart の " HHmm" を付けた別名にする。
// それもあればエラーにする。前の保存先を上書きする録り直しの保存は [StoreOver]。
//
// 同じ保存先への Store の呼び出しは直列であること（呼び出し側が保証する）。確認から
// リネームまでの間に、並行する呼び出しが同じ名前を取ると上書きしうる。
// firstStart は日本時間の最初の放送回の開始時刻を渡す（別名の HHmm になる）。
func Store(root string, l Layout, firstStart time.Time, write func(partial string) error) (Result, error) {
	return StoreOver(root, l, firstStart, "", write)
}

// StoreOver は [Store] と同じく l の場所に m4a を置くが、選んだ名前（l の名前か、" HHmm" を付けた別名）が prev と同じパスなら、
// 別名にせずに prev を上書きする（録り直し。ADR 0025）。prev はそのジョブの前の保存先（root を含むパス。前に保存していなければ空で、
// 空なら [Store] と同じ）。上書きは write が成功した後のリネームで入れ替えるので、prev が消えるのは新しいファイルが確定するときだけ
// （write が失敗すれば prev は残る）。prev と違う、同じ名前のほかのファイルには、[Store] と同じ別名の決まりを当てる。
// 名前が prev と違うパスになったら（局名や保存先が変わったなど）、prev は消さずに残す。
func StoreOver(root string, l Layout, firstStart time.Time, prev string, write func(partial string) error) (Result, error) {
	dir := filepath.Join(root, l.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("library: %w", err)
	}

	f, err := os.CreateTemp(dir, l.Base+".*"+partialExt)
	if err != nil {
		return Result{}, fmt.Errorf("library: %w", err)
	}
	partial := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(partial)
		return Result{}, fmt.Errorf("library: %w", err)
	}
	if err := write(partial); err != nil {
		os.Remove(partial)
		return Result{}, err
	}

	final, renamed, replaced, err := pickName(dir, l.Base, firstStart, prev)
	if err != nil {
		os.Remove(partial)
		return Result{}, err
	}
	// CreateTemp は 0600 で作る。別ユーザーやコンテナの Jellyfin が読めるよう 0644 にする。
	if err := os.Chmod(partial, 0o644); err != nil {
		os.Remove(partial)
		return Result{}, fmt.Errorf("library: %w", err)
	}
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		return Result{}, fmt.Errorf("library: %w", err)
	}
	rel, err := filepath.Rel(root, final)
	if err != nil {
		rel = final
	}
	return Result{Path: final, RelPath: rel, Renamed: renamed, Replaced: replaced}, nil
}

// pickName は最終ファイル名を選ぶ。l の名前、" HHmm" を付けた別名の順に、空いているか prev と同じパスのものを選ぶ。
// renamed は別名にしたか、replaced は prev と同じパスの、既にあるファイルを上書きするか。
func pickName(dir, base string, firstStart time.Time, prev string) (final string, renamed, replaced bool, err error) {
	first := filepath.Join(dir, base+ext)
	alt := filepath.Join(dir, base+" "+firstStart.Format("1504")+ext)
	for i, cand := range []string{first, alt} {
		free, err := notExists(cand)
		if err != nil {
			return "", false, false, err
		}
		if free {
			return cand, i == 1, false, nil
		}
		if prev != "" && filepath.Clean(prev) == cand {
			return cand, i == 1, true, nil
		}
	}
	return "", false, false, fmt.Errorf("library: %s and %s already exist", first, alt)
}

func notExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, fmt.Errorf("library: %w", err)
}

// CleanPartials は root 以下の *.partial.m4a と *.partial.jpg の残骸を消し、消した数を返す。
// 起動時に呼ぶ。root が無ければ何もしない。個々の削除や走査の失敗では止まらず続行し、
// 失敗は errors.Join でまとめて返す。
func CleanPartials(root string) (int, error) {
	n := 0
	var errs []error
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() || !(strings.HasSuffix(d.Name(), partialExt) || strings.HasSuffix(d.Name(), partialImageExt)) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			return nil
		}
		n++
		return nil
	})
	if err := errors.Join(errs...); err != nil {
		return n, fmt.Errorf("library: %w", err)
	}
	return n, nil
}
