// Package artwork は放送回の画像（radiko の番組表の img）を取得する。
//
// 受け付ける形式は JPEG・PNG・GIF で、PNG と GIF は JPEG に直して返す（Fetch は常に JPEG の
// バイト列を返す）。
//
// 画像は付随的なものなので、取れなくても録音の保存は失敗にしない。呼び出し側はエラーを
// ログに出して、画像なしで続ける。
package artwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"time"
)

const (
	// MaxBytes は受け付ける画像の大きさの上限（5 MB）。
	MaxBytes = 5 << 20
	// Timeout は 1 回の取得のタイムアウト。
	Timeout = 30 * time.Second
	// MaxDimension は JPEG に直す画像の幅・高さの上限（ピクセル）。
	MaxDimension = 4096
	// jpegQuality は JPEG に直すときの画質。
	jpegQuality = 90
)

// ErrNoURL は URL が空だったことを表す。
var ErrNoURL = errors.New("artwork: no url")

// Fetch は url の画像を取得し、JPEG のバイト列にして返す。client が nil なら http.DefaultClient。
// Content-Type が image/jpeg ならバイト列をそのまま返す。image/png・image/gif は JPEG に直す
// （GIF はアニメーションでも先頭のコマ。不透明でない画素は白の背景に重ねる）。
// それ以外の Content-Type、取得したバイト列が MaxBytes を超えるもの、幅か高さが MaxDimension を
// 超えるもの、読み込めないものはエラーにする。
func Fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if url == "" {
		return nil, ErrNoURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artwork: status %d", resp.StatusCode)
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mt != "image/jpeg" && mt != "image/png" && mt != "image/gif") {
		return nil, fmt.Errorf("artwork: content-type %q is not image/jpeg, image/png or image/gif", resp.Header.Get("Content-Type"))
	}
	if resp.ContentLength > MaxBytes {
		return nil, fmt.Errorf("artwork: too large (%d bytes)", resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("artwork: too large (over %d bytes)", MaxBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("artwork: empty body")
	}
	if mt == "image/jpeg" {
		return data, nil
	}
	return toJPEG(mt, data)
}

// toJPEG は PNG・GIF のバイト列を JPEG に直す。
func toJPEG(mt string, data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	if cfg.Width > MaxDimension || cfg.Height > MaxDimension {
		return nil, fmt.Errorf("artwork: dimensions too large (%dx%d)", cfg.Width, cfg.Height)
	}
	var img image.Image
	switch mt {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(data))
	default:
		img, err = gif.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("artwork: %w", err)
	}
	return out.Bytes(), nil
}
