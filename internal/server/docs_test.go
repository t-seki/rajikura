package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/api.md の「Web UI の JSON API」の節に、登録した /api/ のルートが全部書かれていることを確かめる。
// ルートは非テストの .go から `mux.Handle(Func)("<メソッド> /api/..."` を拾う。
// docs には、ルートを「`メソッド パターン`」か「`メソッド パターン?クエリ...`」の形で書く。
// メソッドの無いパターン（/api/ の 404 の受け皿）は対象外。
func TestAPIRoutesAreDocumented(t *testing.T) {
	section := apiSection(t)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\.Handle(?:Func)?\("([A-Z]+ /api/[^"]*)"`)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			found++
			route := m[1]
			if !strings.Contains(section, "`"+route+"`") && !strings.Contains(section, "`"+route+"?") {
				t.Errorf("%s の %q が docs/api.md の「Web UI の JSON API」の節に書かれていない", f, route)
			}
		}
	}
	if found == 0 {
		t.Fatal("/api/ のルートを 1 つも拾えなかった（拾う正規表現が登録の書き方と合っていない）")
	}
}

// server.go の冒頭コメントの /api/ の項目（SPA など、ほかの項目は含めない）に、
// /api/ のルートを載せている非テストの .go が全部出てくることを確かめる。
// ファイルを足したのに列挙が古いままになるのを防ぐ。ルートの拾い方は TestAPIRoutesAreDocumented と同じ。
// 拾えないのは、引数を改行した登録、定数・生文字列で書いた登録、メソッドの無い登録。
func TestServerDocListsAPIFiles(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "\npackage server")
	if i < 0 {
		t.Fatal("server.go に package 宣言が無い")
	}
	// 冒頭コメントの /api/ の項目（`//   - /api/` の行から、次の `//   - ` の行の前まで。継続行を含む）。
	var item []string
	in := false
	for _, line := range strings.Split(src[:i], "\n") {
		switch {
		case strings.HasPrefix(line, "//   - /api/"):
			in = true
		case strings.HasPrefix(line, "//   - "):
			in = false
		}
		if in {
			item = append(item, line)
		}
	}
	if len(item) == 0 {
		t.Fatal("server.go の冒頭コメントに /api/ の項目が無い")
	}
	doc := strings.Join(item, "\n")

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\.Handle(?:Func)?\("[A-Z]+ /api/`)
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		s, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !re.Match(s) {
			continue
		}
		found++
		// 直前が英数字・_ のものは別のファイル名の一部（gui.go に対する ui.go など）なので当てない。 code-ref-ok: gui.go
		if !regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(f)).MatchString(doc) {
			t.Errorf("%s は /api/ のルートを載せているが、server.go の冒頭コメントの /api/ の項目に出てこない", f)
		}
	}
	if found == 0 {
		t.Fatal("/api/ のルートを載せるファイルを 1 つも拾えなかった")
	}
}

// apiSection は docs/api.md の「## Web UI の JSON API」の節（次の「## 」の見出しの前まで。「### 」の小見出しを含む）を返す。
func apiSection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "\n## Web UI の JSON API")
	if i < 0 {
		t.Fatal("docs/api.md に「## Web UI の JSON API」の節が無い")
	}
	rest := s[i+1:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// web/src/Shell.tsx のナビの項目（`const pages = [...]`）の並びが、
// docs/web-ui.md の「ナビは「…」「…」の順で」の記述と同じ並びであることを確かめる。
// ナビに項目を足して docs を直し忘れる（または逆）のを防ぐ。
func TestNavOrderIsDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "Shell.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const pages = \[([^\]]*)\]`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("web/src/Shell.tsx に `const pages = [...]` が無い（拾う正規表現が書き方と合っていない）")
	}
	var want []string
	for _, s := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		want = append(want, s[1])
	}
	if len(want) == 0 {
		t.Fatal("ナビの項目を 1 つも拾えなかった")
	}

	d, err := os.ReadFile(filepath.Join("..", "..", "docs", "web-ui.md"))
	if err != nil {
		t.Fatal(err)
	}
	dm := regexp.MustCompile(`ナビは((?:「[^」]+」)+)の順で`).FindStringSubmatch(string(d))
	if dm == nil {
		t.Fatal("docs/web-ui.md に「ナビは「…」「…」の順で」の記述が無い")
	}
	var got []string
	for _, s := range regexp.MustCompile(`「([^」]+)」`).FindAllStringSubmatch(dm[1], -1) {
		got = append(got, s[1])
	}
	if strings.Join(got, "/") != strings.Join(want, "/") {
		t.Errorf("docs/web-ui.md のナビの順 %v が web/src/Shell.tsx の pages %v と違う", got, want)
	}
}
