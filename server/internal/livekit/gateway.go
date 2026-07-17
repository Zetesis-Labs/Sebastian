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
}

func NewGateway(url, apiKey, apiSecret string) *Gateway {
	return &Gateway{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		dispatch:  lksdk.NewAgentDispatchServiceClient(url, apiKey, apiSecret),
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
	return auth.NewAccessToken(g.apiKey, g.apiSecret).
		SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: room}).
		SetIdentity(identity).
		SetName(identity).
		SetValidFor(ttl).
		ToJWT()
}
