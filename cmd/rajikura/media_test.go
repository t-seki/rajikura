package main

import (
	"testing"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/media"
)

// muxer で、m4a への格納を ffmpeg 版と builtin から選ぶ（ADR 0013）。
func TestNewMedia(t *testing.T) {
	if m, ok := newMedia(config.Config{Muxer: config.MuxerBuiltin, FFmpeg: "/x/ffmpeg"}).(media.Builtin); !ok {
		t.Errorf("builtin: got %T", m)
	}
	tl, ok := newMedia(config.Config{Muxer: config.MuxerFFmpeg, FFmpeg: "/x/ffmpeg", FFprobe: "/x/ffprobe"}).(*media.Tools)
	if !ok {
		t.Fatalf("ffmpeg: got %T", tl)
	}
	if tl.FFmpeg != "/x/ffmpeg" || tl.FFprobe != "/x/ffprobe" {
		t.Errorf("ffmpeg: paths = %q, %q", tl.FFmpeg, tl.FFprobe)
	}
}
