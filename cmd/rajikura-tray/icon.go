package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// アイコンの絵は、ファイルを置かずにここで描く。地は web/public/favicon.svg（rajikura のロゴ）で、状態は右下の小さな丸（バッジ）で重ねる。
// 地はどの状態でも同じ（通知領域のほかのアイコンの中で見つけやすくするため）。色だけに頼らないよう、バッジの中の印と、待機中だけバッジが無いことで状態を分ける。
//
//	待機中: バッジなし / 録音中: 赤の丸に白の点 / 異常あり: 橙の丸に白の「!」 / 止まっている: 灰の丸に白の横棒 / 未設定: 灰の輪（中は白）
//
// 16 と 24 は、favicon を簡略にした絵（紺の角丸の地・屋根・ラジオの箱・つまみだけ。アンテナ・横線・電波・柱は落とす）を地にする。
// 32 以上は favicon をそのまま描く（電波の 2 本の弧は折れ線で近似する）。状態の名前はツールチップにも出す。

var (
	colorRecording = color.NRGBA{0xe0, 0x31, 0x31, 0xff}
	colorTrouble   = color.NRGBA{0xf0, 0x8c, 0x00, 0xff}
	colorStopped   = color.NRGBA{0x86, 0x8e, 0x96, 0xff}
	colorWhite     = color.NRGBA{0xff, 0xff, 0xff, 0xff}

	// favicon の色（web/public/favicon.svg の値を写したもの。変えるときは両方を直す）。
	colorFaviconNavy  = color.NRGBA{0x17, 0x2b, 0x45, 0xff} // #172B45
	colorFaviconCream = color.NRGBA{0xf5, 0xf3, 0xe9, 0xff} // #F5F3E9
	colorFaviconWave  = color.NRGBA{0xf4, 0xa2, 0x61, 0xff} // #F4A261
)

// iconSizes は、ICO に入れる大きさ（通知領域は DPI に応じて 16〜32 を使う）。
var iconSizes = []int{16, 24, 32, 48}

// simpleIconMax 以下の大きさは、favicon の簡略版を地にする。
const simpleIconMax = 24

// canvas は、画素を重ね塗りする（cov は 0〜1 の覆われ方）。
type canvas struct {
	img *image.NRGBA
}

func (c canvas) paint(x, y int, col color.NRGBA, cov float64) {
	if cov <= 0 {
		return
	}
	d := c.img.NRGBAAt(x, y)
	sa := cov
	da := float64(d.A) / 255
	oa := sa + da*(1-sa)
	mix := func(s, dv uint8) uint8 {
		return uint8(math.Round((float64(s)*sa + float64(dv)*da*(1-sa)) / oa))
	}
	c.img.SetNRGBA(x, y, color.NRGBA{mix(col.R, d.R), mix(col.G, d.G), mix(col.B, d.B), uint8(math.Round(oa * 255))})
}

// fill は、全画素で inside（境界からの距離。内側が正。画素の中心の座標を渡す）を評価して col で塗る。
func (c canvas) fill(col color.NRGBA, inside func(px, py float64) float64) {
	n := c.img.Bounds().Dx()
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c.paint(x, y, col, coverage(inside(float64(x)+0.5, float64(y)+0.5)))
		}
	}
}

func circleIn(cx, cy, r float64) func(px, py float64) float64 {
	return func(px, py float64) float64 { return r - math.Hypot(px-cx, py-cy) }
}

func rectIn(x, y, w, h, rx float64) func(px, py float64) float64 {
	return func(px, py float64) float64 {
		qx := math.Abs(px-(x+w/2)) - (w/2 - rx)
		qy := math.Abs(py-(y+h/2)) - (h/2 - rx)
		d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rx
		return -d
	}
}

// strokeIn は、折れ線 pts を線幅 w（端は丸）で引いたときの内側の距離。
func strokeIn(w float64, pts ...[2]float64) func(px, py float64) float64 {
	return func(px, py float64) float64 {
		best := math.Inf(1)
		for i := 0; i+1 < len(pts); i++ {
			best = math.Min(best, segDist(px, py, pts[i], pts[i+1]))
		}
		return w/2 - best
	}
}

func segDist(px, py float64, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, ((px-a[0])*dx+(py-a[1])*dy)/l))
	}
	return math.Hypot(px-(a[0]+t*dx), py-(a[1]+t*dy))
}

// quad は、2 次ベジェ曲線を折れ線の点列にする（電波の弧の近似）。
func quad(p0, p1, p2 [2]float64) [][2]float64 {
	const n = 16
	pts := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / n
		u := 1 - t
		pts = append(pts, [2]float64{
			u*u*p0[0] + 2*u*t*p1[0] + t*t*p2[0],
			u*u*p0[1] + 2*u*t*p1[1] + t*t*p2[1],
		})
	}
	return pts
}

// drawBase は、地（favicon。size が simpleIconMax 以下なら簡略版）を size×size で描く。座標は favicon の 64×64 の値を写したもの。
func drawBase(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cv := canvas{img}
	k := float64(size) / 64
	// 64×64 の座標で組んだ図形の距離を、画素の単位に直して渡す。
	f := func(in func(px, py float64) float64) func(px, py float64) float64 {
		return func(px, py float64) float64 { return in(px/k, py/k) * k }
	}
	cv.fill(colorFaviconNavy, f(rectIn(0, 0, 64, 64, 14)))
	if size <= simpleIconMax {
		// 簡略版（設計で決めた値）。
		cv.fill(colorFaviconCream, f(strokeIn(7, [2]float64{8, 26}, [2]float64{32, 10}, [2]float64{56, 26})))
		cv.fill(colorFaviconCream, f(rectIn(14, 34, 36, 22, 4)))
		cv.fill(colorFaviconNavy, f(circleIn(40, 45, 6)))
		return img
	}
	// 以下は web/public/favicon.svg の図形を、上から順に写したもの。
	cv.fill(colorFaviconCream, f(strokeIn(5, [2]float64{8, 24.5}, [2]float64{32, 9.5}, [2]float64{56, 24.5})))
	cv.fill(colorFaviconCream, f(strokeIn(5, [2]float64{12, 24}, [2]float64{12, 53})))
	cv.fill(colorFaviconCream, f(strokeIn(5, [2]float64{52, 24}, [2]float64{52, 53})))
	cv.fill(colorFaviconCream, f(strokeIn(1.6, [2]float64{22.5, 36.9}, [2]float64{32, 31.2})))
	cv.fill(colorFaviconCream, f(rectIn(18.6, 36.9, 27, 16.1, 3)))
	cv.fill(colorFaviconCream, f(circleIn(32, 31.2, 1.8)))
	cv.fill(colorFaviconNavy, f(circleIn(39.4, 45, 4.4)))
	cv.fill(colorFaviconCream, f(circleIn(39.4, 45, 1.9)))
	for _, y := range []float64{41.3, 45, 48.7} {
		cv.fill(colorFaviconNavy, f(strokeIn(2.2, [2]float64{22, y}, [2]float64{31, y})))
	}
	cv.fill(colorFaviconWave, f(strokeIn(2.4, quad([2]float64{24.6, 26}, [2]float64{32, 17}, [2]float64{39.4, 26})...)))
	cv.fill(colorFaviconWave, f(strokeIn(2.4, quad([2]float64{27.5, 28}, [2]float64{32, 21.6}, [2]float64{36.5, 28})...)))
	return img
}

// badgeGeometry は、バッジの中心と半径。右下の角に寄せる（半径は一辺の 0.27 倍、白い縁は一辺の 0.04 倍）。
func badgeGeometry(size int) (cx, cy, r float64) {
	s := float64(size)
	r = s * 0.27
	return s - r, s - r, r
}

// drawIcon は、状態 st のアイコンを size×size で描く。
func drawIcon(st iconState, size int) *image.NRGBA {
	img := drawBase(size)
	if st == stateNormal {
		return img
	}
	cv := canvas{img}
	s := float64(size)
	cx, cy, r := badgeGeometry(size)
	// 16px は印が潰れるので、「!」の縦棒・点と横棒を太らせる。
	thick := 1.0
	if size <= 16 {
		thick = 1.4
	}
	cv.fill(colorWhite, circleIn(cx, cy, r+s*0.04))
	col := colorStopped
	switch st {
	case stateRecording:
		col = colorRecording
	case stateTrouble:
		col = colorTrouble
	}
	cv.fill(col, circleIn(cx, cy, r))
	switch st {
	case stateRecording:
		cv.fill(colorWhite, circleIn(cx, cy, 0.45*r))
	case stateTrouble:
		hw := 0.15 * r * thick
		cv.fill(colorWhite, func(px, py float64) float64 {
			return math.Min(hw-math.Abs(px-cx), math.Min(py-(cy-0.65*r), (cy+0.15*r)-py))
		})
		cv.fill(colorWhite, circleIn(cx, cy+0.45*r, 0.15*r*thick))
	case stateStopped:
		cv.fill(colorWhite, func(px, py float64) float64 {
			return math.Min(0.6*r-math.Abs(px-cx), 0.17*r*thick-math.Abs(py-cy))
		})
	case stateUnconfigured:
		cv.fill(colorWhite, circleIn(cx, cy, 0.55*r))
	}
	return img
}

// coverage は、境界からの距離 v（内側が正）を、画素の覆われ方（0〜1）にする（1 画素の幅でぼかす）。
func coverage(v float64) float64 {
	return math.Max(0, math.Min(1, v+0.5))
}

// iconICO は、状態 st のアイコンを ICO の形式（iconSizes の大きさを 32bpp の DIB で入れる）にする。systray.SetIcon に渡す。
func iconICO(st iconState) []byte {
	type entry struct {
		size int
		data []byte
	}
	entries := make([]entry, 0, len(iconSizes))
	for _, n := range iconSizes {
		entries = append(entries, entry{n, dib(drawIcon(st, n))})
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	w := func(v any) { _ = binary.Write(&buf, le, v) }
	// ICONDIR
	w(uint16(0))
	w(uint16(1))
	w(uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		// ICONDIRENTRY（幅・高さの 0 は 256 を表すが、ここでは 256 を使わない）
		w(uint8(e.size))
		w(uint8(e.size))
		w(uint8(0))
		w(uint8(0))
		w(uint16(1))
		w(uint16(32))
		w(uint32(len(e.data)))
		w(uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		buf.Write(e.data)
	}
	return buf.Bytes()
}

// dib は、画像を ICO の中の 32bpp の DIB（BITMAPINFOHEADER、下の行から並べた BGRA、AND マスク）にする。
func dib(img *image.NRGBA) []byte {
	n := img.Bounds().Dx()
	var buf bytes.Buffer
	le := binary.LittleEndian
	w := func(v any) { _ = binary.Write(&buf, le, v) }
	maskStride := ((n + 31) / 32) * 4
	// BITMAPINFOHEADER（高さは色と AND マスクの 2 枚分）
	w(uint32(40))
	w(int32(n))
	w(int32(2 * n))
	w(uint16(1))
	w(uint16(32))
	w(uint32(0))
	w(uint32(n*n*4 + maskStride*n))
	w(int32(0))
	w(int32(0))
	w(uint32(0))
	w(uint32(0))
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	// AND マスク（透明を alpha で表すので、全部 0）。
	buf.Write(make([]byte, maskStride*n))
	return buf.Bytes()
}

// appIconPNG は、トーストに出すアプリのアイコン（favicon をバッジなしで描いたもの。待機中の絵と同じ）を PNG にする。
func appIconPNG() ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, drawBase(64)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
