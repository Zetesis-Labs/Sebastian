package api

import "context"

type clientIPKey struct{}

// WithClientIP records the address a request came from: the TCP peer, and only
// when the request did not come through a proxy (see httpserver.Middleware).
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIP is the address the request came from; "" when unknown.
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}
