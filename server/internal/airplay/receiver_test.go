package airplay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func pcmBytes(frames int) []byte {
	var buf bytes.Buffer
	for i := range frames * frameSamples {
		_ = binary.Write(&buf, binary.LittleEndian, int16(i-frameSamples))
	}
	return buf.Bytes()
}

func TestReadFramesDecodesLittleEndian(t *testing.T) {
	out := make(chan []int16, 2)
	err := readFrames(bytes.NewReader(pcmBytes(2)), out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	first := <-out
	if len(first) != frameSamples || first[0] != -frameSamples || first[1] != -frameSamples+1 {
		t.Fatalf("first frame: len %d, starts %v", len(first), first[:2])
	}
	second := <-out
	if second[0] != 0 {
		t.Fatalf("second frame starts %d", second[0])
	}
}

func TestReadFramesDropsWhatNobodyTakes(t *testing.T) {
	out := make(chan []int16, 1)
	err := readFrames(bytes.NewReader(pcmBytes(5)), out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("it blocked or failed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("queued %d frames", len(out))
	}
}
