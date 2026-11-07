package airplay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const receiverBinary = "shairport-sync"

// StartReceiver runs shairport-sync and hands out its audio 20 ms at a time.
// The channel closes when the child ends.
func StartReceiver(ctx context.Context, cfg Config, logger *slog.Logger) (<-chan []int16, error) {
	path := filepath.Join(os.TempDir(), "shairport-sync.conf")
	if err := os.WriteFile(path, []byte(receiverConf(cfg)), 0o600); err != nil {
		return nil, fmt.Errorf("write the receiver's configuration: %w", err)
	}
	cmd := exec.CommandContext(ctx, receiverBinary, "-c", path)
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", receiverBinary, err)
	}
	logger.Info("AirPlay receiver up", "name", cfg.Name, "port", cfg.Port, "latencyOffset", cfg.LatencyOffset)

	frames := make(chan []int16, 16)
	go func() {
		defer close(frames)
		err := readFrames(stdout, frames, logger)
		if waitErr := cmd.Wait(); waitErr != nil && ctx.Err() == nil {
			logger.Error("the AirPlay receiver stopped", "error", errors.Join(err, waitErr))
		}
	}()
	return frames, nil
}

// readFrames never blocks on the consumer: a full pipe would stall
// shairport-sync's player and its sync with the source, so a frame nobody can
// take is dropped instead.
func readFrames(r io.Reader, out chan<- []int16, logger *slog.Logger) error {
	buf := make([]byte, frameSamples*2)
	var lastWarning time.Time
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		frame := make([]int16, frameSamples)
		for i := range frame {
			frame[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
		}
		select {
		case out <- frame:
		default:
			if time.Since(lastWarning) > time.Minute {
				logger.Warn("dropping AirPlay audio: the bridge is not keeping up")
				lastWarning = time.Now()
			}
		}
	}
}
