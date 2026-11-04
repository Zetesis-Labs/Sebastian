package livekit

import (
	"context"
	"time"

	"github.com/livekit/protocol/auth"
	livekitapi "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type Gateway struct {
	apiKey    string
	apiSecret string
	dispatch  *lksdk.AgentDispatchClient
	rooms     *lksdk.RoomServiceClient
}

func NewGateway(url, apiKey, apiSecret string) *Gateway {
	return &Gateway{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		dispatch:  lksdk.NewAgentDispatchServiceClient(url, apiKey, apiSecret),
		rooms:     lksdk.NewRoomServiceClient(url, apiKey, apiSecret),
	}
}

func (g *Gateway) Dispatch(ctx context.Context, room, agentName string, metadata []byte) error {
	_, err := g.dispatch.CreateDispatch(ctx, &livekitapi.CreateAgentDispatchRequest{
		Room:      room,
		AgentName: agentName,
		Metadata:  string(metadata),
	})
	return err
}

func (g *Gateway) MintToken(room, identity string, ttl time.Duration) (string, error) {
	return g.mint(&auth.VideoGrant{RoomJoin: true, Room: room}, identity, ttl)
}

// MintListenToken lets the unit into a playback room to hear the source: it
// can neither publish media nor data there.
func (g *Gateway) MintListenToken(room, identity string, ttl time.Duration) (string, error) {
	grant := &auth.VideoGrant{RoomJoin: true, Room: room}
	grant.SetCanPublish(false)
	grant.SetCanPublishData(false)
	grant.SetCanSubscribe(true)
	return g.mint(grant, identity, ttl)
}

// MintSourceToken lets a playback source publish into the room without
// hearing what is there.
func (g *Gateway) MintSourceToken(room, identity string, ttl time.Duration) (string, error) {
	grant := &auth.VideoGrant{RoomJoin: true, Room: room}
	grant.SetCanPublish(true)
	grant.SetCanPublishData(false)
	grant.SetCanSubscribe(false)
	return g.mint(grant, identity, ttl)
}

func (g *Gateway) mint(grant *auth.VideoGrant, identity string, ttl time.Duration) (string, error) {
	return auth.NewAccessToken(g.apiKey, g.apiSecret).
		SetVideoGrant(grant).
		SetIdentity(identity).
		SetName(identity).
		SetValidFor(ttl).
		ToJWT()
}

// SendToAgentWith sends the payload on topic to the agent of the room where
// the participant identity is talking with one; false when there is none.
// Rooms are few (one per open session), so it asks LiveKit instead of keeping
// track of which session is open.
func (g *Gateway) SendToAgentWith(ctx context.Context, identity, topic string, payload []byte) (bool, error) {
	rooms, err := g.rooms.ListRooms(ctx, &livekitapi.ListRoomsRequest{})
	if err != nil {
		return false, err
	}
	var scanErr error
	for _, room := range rooms.GetRooms() {
		if room.GetNumParticipants() < 2 {
			continue
		}
		participants, err := g.rooms.ListParticipants(ctx, &livekitapi.ListParticipantsRequest{Room: room.GetName()})
		if err != nil {
			scanErr = err // one room's error must not hide the unit's room
			continue
		}
		agent, ok := agentTalkingWith(participants.GetParticipants(), identity)
		if !ok {
			continue
		}
		_, err = g.rooms.SendData(ctx, &livekitapi.SendDataRequest{
			Room:                  room.GetName(),
			Data:                  payload,
			Kind:                  livekitapi.DataPacket_RELIABLE,
			Topic:                 &topic,
			DestinationIdentities: []string{agent},
		})
		return err == nil, err
	}
	return false, scanErr
}

func agentTalkingWith(participants []*livekitapi.ParticipantInfo, identity string) (string, bool) {
	var agent string
	present := false
	for _, p := range participants {
		switch {
		case p.GetIdentity() == identity:
			present = true
		case p.GetKind() == livekitapi.ParticipantInfo_AGENT:
			agent = p.GetIdentity()
		}
	}
	return agent, present && agent != ""
}
