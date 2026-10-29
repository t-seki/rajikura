// Package web は Web UI の SPA（ビルドの結果）を埋め込む。
//
// Vite の出力先は web/dist で、ビルドの結果は git に入れない（.gitignore は dist/.gitkeep だけ残す）。
// dist/.gitkeep があるので、Node でビルドしていなくても go:embed が通り、go build と go test に Node は要らない。
// dist に index.html が無いときは、「ビルドされていない」と出す placeholder/index.html を使う。
// Vite でビルドするときは、dist/.gitkeep が消えないように emptyOutDir を false にする（vite.config.ts）。
// 古い成果物は、pnpm build の前段の scripts/clean-dist.mjs が .gitkeep 以外を消す。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

//go:embed placeholder
var placeholder embed.FS

// FS は SPA のファイルの根を返す。dist/index.html があれば dist、無ければ仮のファイル。
func FS() fs.FS {
	if f, err := fs.Sub(dist, "dist"); err == nil {
		if _, err := fs.Stat(f, "index.html"); err == nil {
			return f
		}
	}
	f, err := fs.Sub(placeholder, "placeholder")
	if err != nil {
		panic(err)
	}
	return f
}
