package radiko

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Program は番組表の 1 項目（1 放送回）。
//
// 放送回は番組表の id ではなく「放送局 + 開始時刻」で識別する。
// 分割番組は同じ id の項目が複数並ぶため。
type Program struct {
	// ID は番組表の prog の id。分割番組では複数の項目で同じ値になる。
	ID        string
	StationID string
	// Start と End は日本時間。
	Start time.Time
	End   time.Time
	Title string
	// Performer は出演者（pfm）。
	Performer string
	ImageURL  string
	// Description は info の HTML からタグを除きエスケープを戻した平文。info が空なら desc。
	Description string
	// DescriptionParts は Description を、リンクの部品とそうでない部品に分けた並び。
	// text を全部つないだものは Description とちょうど同じ。説明が空なら空。
	DescriptionParts []DescriptionPart
	// TSInNG と TSPlusInNG は、タイムフリーの可否のフラグ（ts_in_ng と tsplus_in_ng）の生の値。
	TSInNG     string
	TSPlusInNG string
}

// DescriptionPart は説明の部品。Href が空でなければリンク（http・https・mailto だけ）。
type DescriptionPart struct {
	Text string
	Href string
}

// BroadcastDay は、時刻 t の放送回が属する放送日（日本時間の 0 時）を返す。
// 放送日は 05:00〜翌 04:59 なので、0〜4 時台は前の日になる。
func BroadcastDay(t time.Time) time.Time {
	t = t.In(JST).Add(-5 * time.Hour)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, JST)
}

type xmlProgram struct {
	ID         string `xml:"id,attr"`
	FT         string `xml:"ft,attr"`
	To         string `xml:"to,attr"`
	Title      string `xml:"title"`
	Pfm        string `xml:"pfm"`
	Info       string `xml:"info"`
	Desc       string `xml:"desc"`
	Img        string `xml:"img"`
	TSInNG     string `xml:"ts_in_ng"`
	TSPlusInNG string `xml:"tsplus_in_ng"`
}

type xmlProgramStation struct {
	ID    string       `xml:"id,attr"`
	Name  string       `xml:"name"`
	Progs []xmlProgram `xml:"progs>prog"`
}

type xmlPrograms struct {
	Stations []xmlProgramStation `xml:"stations>station"`
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// plainText は HTML からタグを除き、エスケープを戻して前後の空白を落とす。
func plainText(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

var (
	anchorRe = regexp.MustCompile(`(?is)<a(?:\s[^>]*?)?\shref\s*=\s*(?:"([^"]*)"|'([^']*)')[^>]*>(.*?)</a\s*>`)
	// 平文の中の URL（ASCII の非空白の連なり。<>" は含めない）とメールアドレス。
	plainLinkRe = regexp.MustCompile(`https?://[\x21\x23-\x3b\x3d\x3f-\x7e]+|[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
)

// safeHref は href のエスケープを戻し、scheme が http・https・mailto のときだけ返す。
func safeHref(raw string) (string, bool) {
	h := strings.TrimSpace(html.UnescapeString(raw))
	l := strings.ToLower(h)
	for _, pre := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(l, pre) && len(h) > len(pre) {
			if strings.IndexFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return "", false
			}
			return h, true
		}
	}
	return "", false
}

// splitPlainLinks は平文の中の URL・メールアドレスをリンクの部品に分ける。
func splitPlainLinks(s string) []DescriptionPart {
	var out []DescriptionPart
	last := 0
	for _, m := range plainLinkRe.FindAllStringIndex(s, -1) {
		cand := s[m[0]:m[1]]
		isURL := strings.HasPrefix(cand, "http://") || strings.HasPrefix(cand, "https://")
		if isURL {
			cand = strings.TrimRight(cand, ".,;:!?)]")
			if _, ok := safeHref(cand); !ok {
				continue
			}
		}
		href := cand
		if !isURL {
			href = "mailto:" + cand
		}
		end := m[0] + len(cand)
		if m[0] > last {
			out = append(out, DescriptionPart{Text: s[last:m[0]]})
		}
		out = append(out, DescriptionPart{Text: cand, Href: href})
		last = end
	}
	if last < len(s) {
		out = append(out, DescriptionPart{Text: s[last:]})
	}
	return out
}

// descriptionParts は info の HTML を説明の部品に分ける。text を全部つないだものは plainText(info) と同じ。
func descriptionParts(info string) []DescriptionPart {
	var parts []DescriptionPart
	text := func(raw string) {
		parts = append(parts, splitPlainLinks(html.UnescapeString(tagRe.ReplaceAllString(raw, "")))...)
	}
	last := 0
	for _, sub := range anchorRe.FindAllStringSubmatchIndex(info, -1) {
		hrefRaw := ""
		if sub[2] >= 0 {
			hrefRaw = info[sub[2]:sub[3]]
		} else {
			hrefRaw = info[sub[4]:sub[5]]
		}
		inner := html.UnescapeString(tagRe.ReplaceAllString(info[sub[6]:sub[7]], ""))
		text(info[last:sub[0]])
		if href, ok := safeHref(hrefRaw); ok {
			parts = append(parts, DescriptionPart{Text: inner, Href: href})
		} else {
			parts = append(parts, DescriptionPart{Text: inner})
		}
		last = sub[1]
	}
	text(info[last:])
	parts = trimParts(parts)
	// text をつないだものが plainText と違う入力（引用符の中の > や、タグをまたぐ文字参照）は、リンクの無い 1 つに戻す。
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	if want := plainText(info); b.String() != want {
		if want == "" {
			return nil
		}
		return []DescriptionPart{{Text: want}}
	}
	return parts
}

// trimParts は、つないだ text の前後の空白を落とし、空になった部品を除く。
func trimParts(parts []DescriptionPart) []DescriptionPart {
	for i := range parts {
		parts[i].Text = strings.TrimLeftFunc(parts[i].Text, unicode.IsSpace)
		if parts[i].Text != "" {
			break
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		parts[i].Text = strings.TrimRightFunc(parts[i].Text, unicode.IsSpace)
		if parts[i].Text != "" {
			break
		}
	}
	var out []DescriptionPart
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		// リンクでない部品が並んだら 1 つにまとめる。
		if n := len(out); n > 0 && p.Href == "" && out[n-1].Href == "" {
			out[n-1].Text += p.Text
			continue
		}
		out = append(out, p)
	}
	return out
}

// parseTime は YYYYMMDDhhmmss を日本時間として読む。
func parseTime(s string) (time.Time, error) {
	return time.ParseInLocation(timeLayout, strings.TrimSpace(s), JST)
}

// parsePrograms は番組表の XML を読む。
func parsePrograms(data []byte, stationID string) ([]Program, error) {
	var doc xmlPrograms
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse program xml: %v", ErrTemporary, err)
	}
	var out []Program
	for _, st := range doc.Stations {
		ps, err := parseStationPrograms(st, stationID)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	return out, nil
}

// parseStationPrograms は 1 局の放送回を読む。stationID が空なら局の id 属性を使う。
func parseStationPrograms(st xmlProgramStation, stationID string) ([]Program, error) {
	sid := stationID
	if sid == "" {
		sid = strings.TrimSpace(st.ID)
	}
	var out []Program
	for _, p := range st.Progs {
		start, err := parseTime(p.FT)
		if err != nil {
			return nil, fmt.Errorf("%w: program %q ft %q: %v", ErrTemporary, p.ID, p.FT, err)
		}
		end, err := parseTime(p.To)
		if err != nil {
			return nil, fmt.Errorf("%w: program %q to %q: %v", ErrTemporary, p.ID, p.To, err)
		}
		desc := plainText(p.Info)
		parts := descriptionParts(p.Info)
		if desc == "" {
			desc = strings.TrimSpace(p.Desc)
			parts = trimParts(splitPlainLinks(p.Desc))
		}
		out = append(out, Program{
			ID:               p.ID,
			StationID:        sid,
			Start:            start,
			End:              end,
			Title:            strings.TrimSpace(p.Title),
			Performer:        strings.TrimSpace(p.Pfm),
			ImageURL:         strings.TrimSpace(p.Img),
			Description:      desc,
			DescriptionParts: parts,
			TSInNG:           strings.TrimSpace(p.TSInNG),
			TSPlusInNG:       strings.TrimSpace(p.TSPlusInNG),
		})
	}
	return out, nil
}

// Programs は、放送局 stationID の放送日 day の番組表を返す。
// EnableProgramCache を有効にしていると、最大 60 秒古い版が返りうる（cache.go）。
// day は暦日だけを使う（放送日は 05:00〜翌 04:59 の範囲）。
func (c *Client) Programs(ctx context.Context, stationID string, day time.Time) ([]Program, error) {
	u := fmt.Sprintf("%s/v3/program/station/date/%s/%s.xml",
		strings.TrimRight(c.ProgramHost, "/"), day.In(JST).Format("20060102"), url.PathEscape(stationID))
	data, err := c.getProgramXML(ctx, u, day)
	if err != nil {
		return nil, err
	}
	return parsePrograms(data, stationID)
}

// AreaStation は、エリアの日別番組表に出てくる 1 局の放送回。
type AreaStation struct {
	ID   string
	Name string
	// Programs は、その放送日の放送回。放送日の 5 時台から翌日の 4 時台までが並ぶ。
	Programs []Program
}

// AreaPrograms は、エリア areaID の放送日 day の、エリアの全局の番組表を返す
// （/v3/program/date/<YYYYMMDD>/<エリア ID>.xml。認証は要らない）。局ごとの Programs と同じ形の放送回を、局ごとに返す。
// EnableProgramCache を有効にしていると、最大 60 秒古い版が返りうる（cache.go）。
// day は暦日だけを使う。
func (c *Client) AreaPrograms(ctx context.Context, areaID string, day time.Time) ([]AreaStation, error) {
	u := fmt.Sprintf("%s/v3/program/date/%s/%s.xml",
		strings.TrimRight(c.ProgramHost, "/"), day.In(JST).Format("20060102"), url.PathEscape(areaID))
	data, err := c.getProgramXML(ctx, u, day)
	if err != nil {
		return nil, err
	}
	var doc xmlPrograms
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse area program xml: %v", ErrTemporary, err)
	}
	out := make([]AreaStation, 0, len(doc.Stations))
	for _, st := range doc.Stations {
		ps, err := parseStationPrograms(st, "")
		if err != nil {
			return nil, err
		}
		out = append(out, AreaStation{ID: strings.TrimSpace(st.ID), Name: strings.TrimSpace(st.Name), Programs: ps})
	}
	return out, nil
}

// Station は放送局。
type Station struct {
	ID   string
	Name string
	// TFMaxDelay はタイムフリーが公開されるまでの最大の遅れ（秒）。
	TFMaxDelay int
}

type xmlStation struct {
	ID         string `xml:"id"`
	Name       string `xml:"name"`
	TFMaxDelay int    `xml:"tf_max_delay"`
}

type xmlStations struct {
	Stations []xmlStation `xml:"stations>station"`
}

// Stations は full.xml から局 ID・局名・tf_max_delay を読む。
func (c *Client) Stations(ctx context.Context) ([]Station, error) {
	data, err := c.getXML(ctx, strings.TrimRight(c.ProgramHost, "/")+"/v3/station/region/full.xml")
	if err != nil {
		return nil, err
	}
	var doc xmlStations
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse stations xml: %v", ErrTemporary, err)
	}
	out := make([]Station, 0, len(doc.Stations))
	for _, s := range doc.Stations {
		out = append(out, Station{ID: strings.TrimSpace(s.ID), Name: strings.TrimSpace(s.Name), TFMaxDelay: s.TFMaxDelay})
	}
	return out, nil
}
