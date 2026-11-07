package airplay

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"gopkg.in/hraban/opus.v2"
)

const (
	// Music settings, as tools/play.py: DTX would cut quiet passages.
	opusBitrate = 128000
	// joinPatience allows for an order that travels in the unit's 30 s poll.
	joinPatience = 40 * time.Second
)

type liveKitRoom struct {
	room       *lksdk.Room
	track      *lksdk.LocalTrack
	encoder    *opus.Encoder
	subscribed atomic.Bool
	packet     []byte
	logger     *slog.Logger
	playbackID string
	warnAt     time.Time
}

// JoinLiveKit is the Join that publishes into LiveKit. NewPCMLocalTrack would
// be shorter, but it fixes Opus to voice with no say on the bitrate.
func JoinLiveKit(logger *slog.Logger) Join {
	return func(_ context.Context, started Started, lost func()) (Room, error) {
		encoder, err := opus.NewEncoder(sampleRate, channels, opus.AppAudio)
		if err != nil {
			return nil, fmt.Errorf("opus encoder: %w", err)
		}
		if err := encoder.SetBitrate(opusBitrate); err != nil {
			return nil, fmt.Errorf("opus bitrate: %w", err)
		}
		if err := encoder.SetDTX(false); err != nil {
			return nil, fmt.Errorf("opus DTX: %w", err)
		}

		r := &liveKitRoom{
			encoder:    encoder,
			packet:     make([]byte, 4000),
			logger:     logger,
			playbackID: started.ID,
			warnAt:     time.Now().Add(joinPatience),
		}
		callback := lksdk.NewRoomCallback()
		callback.OnLocalTrackSubscribed = func(*lksdk.LocalTrackPublication, *lksdk.LocalParticipant) {
			if !r.subscribed.Swap(true) {
				logger.Info("the speaker is listening", "playback", started.ID)
			}
		}
		callback.OnReconnecting = func() { logger.Warn("reconnecting to the room", "playback", started.ID) }
		callback.OnReconnected = func() { logger.Info("reconnected to the room", "playback", started.ID) }
		callback.OnDisconnectedWithReason = func(reason lksdk.DisconnectionReason) {
			logger.Warn("disconnected from the room", "playback", started.ID, "reason", reason)
			lost()
		}

		room, err := lksdk.ConnectToRoomWithToken(started.ServerURL, started.SourceToken, callback, lksdk.WithAutoSubscribe(false))
		if err != nil {
			return nil, fmt.Errorf("connect to the room: %w", err)
		}
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: sampleRate, Channels: channels})
		if err != nil {
			room.Disconnect()
			return nil, fmt.Errorf("create the track: %w", err)
		}
		options := &lksdk.TrackPublicationOptions{Name: "airplay", Source: livekit.TrackSource_MICROPHONE, Stereo: true, DisableDTX: true}
		if _, err := room.LocalParticipant.PublishTrack(track, options); err != nil {
			room.Disconnect()
			return nil, fmt.Errorf("publish the track: %w", err)
		}
		r.room, r.track = room, track
		return r, nil
	}
}

// Write drops the audio until the speaker listens: it is a live stream, and
// queueing it would add to the delay for good.
func (r *liveKitRoom) Write(pcm []int16) error {
	if !r.subscribed.Load() {
		if now := time.Now(); now.After(r.warnAt) {
			r.logger.Warn("playing to nobody: the speaker has not joined the room", "playback", r.playbackID)
			r.warnAt = now.Add(time.Minute)
		}
		return nil
	}
	n, err := r.encoder.Encode(pcm, r.packet)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return r.track.WriteSample(media.Sample{Data: bytes.Clone(r.packet[:n]), Duration: frameDuration}, nil)
}

func (r *liveKitRoom) Close() {
	r.room.Disconnect()
}
