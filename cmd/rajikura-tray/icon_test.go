package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// near は、2 色の各チャンネルの差が tol 以内かを返す。
func near(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol && a.A > 200
}

// at は、バッジの中心から (dx, dy)×半径 だけ離れた画素の色。
func at(img *image.NRGBA, size int, dx, dy float64) color.NRGBA {
	cx, cy, r := badgeGeometry(size)
	return img.NRGBAAt(int(cx+dx*r), int(cy+dy*r))
}

func TestBadgeColorsAndMarks(t *testing.T) {
	for _, size := range iconSizes {
		base := drawBase(size)
		cx, cy, _ := badgeGeometry(size)
		// 待機中はバッジが無い（バッジの中心が地のまま）。32 以上では地の紺。
		normal := drawIcon(stateNormal, size)
		if got, want := normal.NRGBAAt(int(cx), int(cy)), base.NRGBAAt(int(cx), int(cy)); got != want {
			t.Errorf("%dpx 待機中: バッジがある %v != %v", size, got, want)
		}
		if size > simpleIconMax {
			if got := normal.NRGBAAt(int(cx), int(cy)); !near(got, colorFaviconNavy, 8) {
				t.Errorf("%dpx 待機中: バッジの位置が地の紺でない %v", size, got)
			}
		}
		// 印の乗らない点（中心から左上へ 0.55 半径）に、バッジの色が出る。
		for st, want := range map[iconState]color.NRGBA{
			stateRecording: colorRecording, stateTrouble: colorTrouble, stateStopped: colorStopped, stateUnconfigured: colorStopped,
		} {
			if got := at(drawIcon(st, size), size, -0.55, -0.55); !near(got, want, 24) {
				t.Errorf("%dpx %v: バッジの色が違う %v want %v", size, st, got, want)
			}
		}
		up, tol := -0.38, 24
		if size <= 16 {
			tol = 45 // 16px は画素の粗さで、輪の内側の縁の画素が少し灰に寄る。
		}
		if size <= 16 {
			up = -0.45 // 16px は横棒を太らせたので、棒から離れた点を見る。
		}
		// 止まっている（灰の丸）と未設定（灰の輪、中は白）は、中心から上 0.38 半径で見分ける。
		if got := at(drawIcon(stateStopped, size), size, 0, up); !near(got, colorStopped, 24) {
			t.Errorf("%dpx 止まっている: 中心の上が灰でない %v", size, got)
		}
		if got := at(drawIcon(stateUnconfigured, size), size, 0, up); !near(got, colorWhite, tol) {
			t.Errorf("%dpx 未設定: 輪の中が白でない %v", size, got)
		}
		// 印: 録音中の点と止まっているの横棒は中心が白、異常ありの「!」は中心の縦棒が白。
		for _, st := range []iconState{stateRecording, stateTrouble, stateStopped} {
			if got := at(drawIcon(st, size), size, 0, 0); !near(got, colorWhite, 24) {
				t.Errorf("%dpx %v: 中心に白の印が無い %v", size, st, got)
			}
		}
	}
}

func TestSimpleBaseForSmallSizes(t *testing.T) {
	// 簡略版は電波（橙）を描かず、favicon は描く。
	hasWave := func(img *image.NRGBA) bool {
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				if near(img.NRGBAAt(x, y), colorFaviconWave, 6) {
					return true
				}
			}
		}
		return false
	}
	for _, n := range []int{16, 24} {
		if hasWave(drawBase(n)) {
			t.Errorf("%dpx は簡略版にする（電波を描かない）", n)
		}
	}
	for _, n := range []int{32, 48, 64} {
		if !hasWave(drawBase(n)) {
			t.Errorf("%dpx は favicon をそのまま描く（電波がある）", n)
		}
	}
}

func TestAppIconIsBaseWithoutBadge(t *testing.T) {
	b, err := appIconPNG()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	want := drawBase(64)
	cx, cy, _ := badgeGeometry(64)
	if got := color.NRGBAModel.Convert(img.At(int(cx), int(cy))).(color.NRGBA); got != want.NRGBAAt(int(cx), int(cy)) || !near(got, colorFaviconNavy, 8) {
		t.Errorf("バッジの位置が地の色でない: %v", got)
	}
}
