package radiko

import (
	"reflect"
	"strings"
	"testing"
)

func joinParts(ps []DescriptionPart) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.Text)
	}
	return b.String()
}

func TestDescriptionParts(t *testing.T) {
	tests := []struct {
		name string
		info string
		want []DescriptionPart
	}{
		{"リンクが無い", "<p>ただの説明 &amp; 続き</p>", []DescriptionPart{{Text: "ただの説明 & 続き"}}},
		{"空", "  ", nil},
		{"a と u の入れ子", `前<br>
<a href="https://example.com/p?a=1&amp;b=2"><u>こちら</u></a>
後`, []DescriptionPart{
			{Text: "前\n"}, {Text: "こちら", Href: "https://example.com/p?a=1&b=2"}, {Text: "\n後"},
		}},
		{"mailto", `<a href="mailto:x@example.com">連絡</a>`, []DescriptionPart{{Text: "連絡", Href: "mailto:x@example.com"}}},
		{"http", `<a href='http://example.com/'>x</a>`, []DescriptionPart{{Text: "x", Href: "http://example.com/"}}},
		{"javascript は文字だけ残す", `<a href="javascript:alert(1)">押す</a>`, []DescriptionPart{{Text: "押す"}}},
		{"相対パスは文字だけ残す", `<a href="/x">相対</a>と<a href="">空</a>`, []DescriptionPart{{Text: "相対と空"}}},
		{"href が無い a はタグとして落とす", `<a name="x">文字</a>`, []DescriptionPart{{Text: "文字"}}},
		{"閉じタグが無い a はタグとして落とす", `<a href="https://example.com/">文字`, []DescriptionPart{{Text: "文字"}}},
		{"a の外の URL", "詳細は https://example.com/a?b=1。続き", []DescriptionPart{
			{Text: "詳細は "}, {Text: "https://example.com/a?b=1", Href: "https://example.com/a?b=1"}, {Text: "。続き"},
		}},
		{"URL の末尾の記号は除く", "(https://example.com/x).", []DescriptionPart{
			{Text: "("}, {Text: "https://example.com/x", Href: "https://example.com/x"}, {Text: ")."},
		}},
		{"メールアドレス", "宛先 a.b@example.co.jp まで", []DescriptionPart{
			{Text: "宛先 "}, {Text: "a.b@example.co.jp", Href: "mailto:a.b@example.co.jp"}, {Text: " まで"},
		}},
		{"http で始まるメールアドレス", "httpd@example.com と https-admin@example.co.jp", []DescriptionPart{
			{Text: "httpd@example.com", Href: "mailto:httpd@example.com"}, {Text: " と "},
			{Text: "https-admin@example.co.jp", Href: "mailto:https-admin@example.co.jp"},
		}},
		{"data-href は href とみなさない", `<a data-href="https://evil.example/" href="/x">t</a>`, []DescriptionPart{{Text: "t"}}},
		{"引用符の中の > は平文に戻す", `<a href="https://e.com/?a>b">文字</a>`, []DescriptionPart{{Text: plainText(`<a href="https://e.com/?a>b">文字</a>`)}}},
		{"タグをまたぐ文字参照は平文に戻す", `&am<a href="https://e.com/">p;</a>`, []DescriptionPart{{Text: plainText(`&am<a href="https://e.com/">p;</a>`)}}},
		{"@アカウントはリンクにしない", "X は @account で", []DescriptionPart{{Text: "X は @account で"}}},
		{"a の中の URL は a の href のまま", `<a href="https://a.example/">https://b.example/</a>`, []DescriptionPart{
			{Text: "https://b.example/", Href: "https://a.example/"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := descriptionParts(tt.info)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parts = %#v, want %#v", got, tt.want)
			}
			// 部品の text をつないだものは、今の平文とちょうど同じ。
			if j := joinParts(got); j != plainText(tt.info) {
				t.Errorf("joined = %q, plainText = %q", j, plainText(tt.info))
			}
		})
	}
}

func TestParseProgramsDescriptionParts(t *testing.T) {
	data := []byte(`<radiko><stations><station id="TBS"><progs>
<prog id="1" ft="20261017050000" to="20261017060000"><title>a</title>
<info>&lt;none&gt;<![CDATA[ <a href="https://example.com/"><u>こちら</u></a><br>
続きは @acct か a@example.com ]]></info></prog>
<prog id="2" ft="20261017060000" to="20261017070000"><title>b</title><info></info><desc> 詳細 http://example.com/d </desc></prog>
</progs></station></stations></radiko>`)
	ps, err := parsePrograms(data, "TBS")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if j := joinParts(p.DescriptionParts); j != p.Description {
			t.Errorf("%s: joined = %q, description = %q", p.ID, j, p.Description)
		}
	}
	wantLinks := map[string][]string{
		"1": {"https://example.com/", "mailto:a@example.com"},
		"2": {"http://example.com/d"},
	}
	for _, p := range ps {
		var hrefs []string
		for _, dp := range p.DescriptionParts {
			if dp.Href != "" {
				hrefs = append(hrefs, dp.Href)
			}
		}
		if !reflect.DeepEqual(hrefs, wantLinks[p.ID]) {
			t.Errorf("%s: hrefs = %v, want %v", p.ID, hrefs, wantLinks[p.ID])
		}
	}
}
