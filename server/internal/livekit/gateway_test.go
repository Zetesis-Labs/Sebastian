package livekit

import (
	"testing"

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
