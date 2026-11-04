package livekit

import (
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	livekitapi "github.com/livekit/protocol/livekit"
)

func TestTheAgentTalkingWithTheUnitIsFoundOnlyWhenBothAreInTheRoom(t *testing.T) {
	unit := &livekitapi.ParticipantInfo{Identity: "e072a1f96ef0", Kind: livekitapi.ParticipantInfo_STANDARD}
	agent := &livekitapi.ParticipantInfo{Identity: "agent-AJ_1", Kind: livekitapi.ParticipantInfo_AGENT}
	other := &livekitapi.ParticipantInfo{Identity: "otra-placa", Kind: livekitapi.ParticipantInfo_STANDARD}

	if got, ok := agentTalkingWith([]*livekitapi.ParticipantInfo{unit, agent}, "e072a1f96ef0"); !ok || got != "agent-AJ_1" {
		t.Fatalf("unit + agent = %q, %v", got, ok)
	}
	if _, ok := agentTalkingWith([]*livekitapi.ParticipantInfo{unit}, "e072a1f96ef0"); ok {
		t.Fatal("a unit alone has no conversation")
	}
	if _, ok := agentTalkingWith([]*livekitapi.ParticipantInfo{other, agent}, "e072a1f96ef0"); ok {
		t.Fatal("another unit's conversation is not ours")
	}
}

func grantsOf(t *testing.T, token string) *auth.VideoGrant {
	t.Helper()
	v, err := auth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := v.Verify("secret-secret-secret-secret-secret")
	if err != nil {
		t.Fatal(err)
	}
	return claims.Video
}

func TestAPlaybackRoomIsListenedToByTheUnitAndFedOnlyByTheSource(t *testing.T) {
	g := NewGateway("http://livekit:7880", "key", "secret-secret-secret-secret-secret")
	room := "sebastian-play-1"

	unit, err := g.MintListenToken(room, "e072a1f96ef0", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if v := grantsOf(t, unit); v.Room != room || !v.RoomJoin || v.GetCanPublish() || !v.GetCanSubscribe() || v.GetCanPublishData() {
		t.Fatalf("unit grant = %+v", v)
	}
	source, err := g.MintSourceToken(room, "source-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if v := grantsOf(t, source); v.Room != room || !v.RoomJoin || !v.GetCanPublish() || v.GetCanSubscribe() || v.GetCanPublishData() {
		t.Fatalf("source grant = %+v", v)
	}
}
