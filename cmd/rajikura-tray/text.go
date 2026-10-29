package main

import (
	"html"
	"strings"
)

// cdata は、go-toast が CDATA に入れる文字列（トーストの題と本文）から、CDATA を閉じる並び（]]>）を崩す。
func cdata(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// toastLaunch は、トーストを押したときに開く URL を、go-toast が XML の属性にそのまま入れる形に直す
// （go-toast は属性の値をエスケープしないので、& などを呼ぶ側で直す）。
func toastLaunch(u string) string {
	return html.EscapeString(u)
}

// autostartCommand は、ログイン時に起動するために HKCU の Run に書くコマンド（exe を引用符で囲み、-url を渡されて起動したなら同じ -url を付ける）。
func autostartCommand(exe, flagURL string) string {
	cmd := `"` + exe + `"`
	if flagURL != "" {
		cmd += ` -url ` + flagURL
	}
	return cmd
}

// linkName は、連携の画面に出す名前（<PC 名>\<ユーザー名>）。
func linkName(host, user string) string {
	return host + `\` + user
}
