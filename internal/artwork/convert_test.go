package artwork

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("JPEG として読めない: %v", err)
	}
	return img
}

func nearly(c color.Color, r, g, b uint8, tol int) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(a uint32, w uint8) bool {
		x := int(a>>8) - int(w)
		if x < 0 {
			x = -x
		}
		return x <= tol
	}
	return d(cr, r) && d(cg, g) && d(cb, b)
}

func TestFetchConvertsPNG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{200, 0, 0, 255})
		}
	}
	got, err := Fetch(context.Background(), nil, serve(t, "image/png", encodePNG(t, img), 0))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeJPEG(t, got)
	if !nearly(out.At(8, 8), 200, 0, 0, 12) {
		t.Errorf("不透明な画素: %v", out.At(8, 8))
	}
}

func TestFetchPNGTransparentOverWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16)) // 全画素が透明
	got, err := Fetch(context.Background(), nil, serve(t, "image/png", encodePNG(t, img), 0))
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeJPEG(t, got).At(8, 8); !nearly(c, 255, 255, 255, 6) {
		t.Errorf("透明な画素が白でない: %v", c)
	}
}

func TestFetchConvertsGIF(t *testing.T) {
	pal := color.Palette{color.White, color.RGBA{0, 0, 255, 255}}
	frame := image.NewPaletted(image.Rect(0, 0, 16, 16), pal)
	for i := range frame.Pix {
		frame.Pix[i] = 1
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}}); err != nil {
		t.Fatal(err)
	}
	got, err := Fetch(context.Background(), nil, serve(t, "image/gif", b.Bytes(), 0))
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeJPEG(t, got).At(8, 8); !nearly(c, 0, 0, 255, 12) {
		t.Errorf("GIF の画素: %v", c)
	}
}

func TestFetchJPEGPassThrough(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	got, err := Fetch(context.Background(), nil, serve(t, "image/jpeg", b.Bytes(), 0))
	if err != nil || !bytes.Equal(got, b.Bytes()) {
		t.Fatalf("バイト列がそのまま返らない: %v", err)
	}
}

func TestFetchConvertRejects(t *testing.T) {
	wide := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, MaxDimension+1, 1)))
	tall := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 1, MaxDimension+1)))
	okSize := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, MaxDimension, 1)))
	cut := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 32, 32)))
	cases := map[string]string{
		"WebP":          serve(t, "image/webp", []byte("RIFF....WEBP"), 0),
		"PNG の中身が壊れている": serve(t, "image/png", []byte("not a png"), 0),
		"PNG が途中で切れる":   serve(t, "image/png", cut[:len(cut)-20], 0),
		"幅が大きすぎる":       serve(t, "image/png", wide, 0),
		"高さが大きすぎる":      serve(t, "image/png", tall, 0),
	}
	for name, url := range cases {
		if _, err := Fetch(context.Background(), nil, url); err == nil {
			t.Errorf("%s: エラーにならない", name)
		}
	}
	if _, err := Fetch(context.Background(), nil, serve(t, "image/png", okSize, 0)); err != nil {
		t.Errorf("幅が上限ちょうど: %v", err)
	}
}
