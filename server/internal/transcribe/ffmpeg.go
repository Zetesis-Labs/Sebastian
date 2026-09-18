package transcribe

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strconv"
)

// FFmpeg is the Cutter over the ffmpeg binary: pieces keep the Opus stream
// untouched (`-c copy`); speaker clips are small 16 kHz WAVs.
type FFmpeg struct{ Path string }

func (f FFmpeg) Cut(ctx context.Context, src string, p Piece) ([]byte, error) {
	return f.run(ctx, "-ss", seconds(p.Start.Seconds()), "-t", seconds(p.Length.Seconds()), "-i", src, "-c", "copy", "-f", "ogg", "pipe:1")
}

func (f FFmpeg) Clip(ctx context.Context, src string, start, length float64) (string, error) {
	wav, err := f.run(ctx, "-ss", seconds(start), "-t", seconds(length), "-i", src, "-ac", "1", "-ar", "16000", "-f", "wav", "pipe:1")
	if err != nil {
		return "", err
	}
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav), nil
}

func (f FFmpeg) run(ctx context.Context, args ...string) ([]byte, error) {
	path := f.Path
	if path == "" {
		path = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, bytes.TrimSpace(errs.Bytes()))
	}
	return out.Bytes(), nil
}

func seconds(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
