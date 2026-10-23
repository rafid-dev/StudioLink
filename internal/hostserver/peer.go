package hostserver

import "context"

type peerKey struct{}

// WithPeer records who sent a request (for example a tailnet user and device)
// so the session log names them instead of just a remote address.
func WithPeer(ctx context.Context, peer string) context.Context {
	return context.WithValue(ctx, peerKey{}, peer)
}

func peerFrom(ctx context.Context) string {
	peer, _ := ctx.Value(peerKey{}).(string)
	return peer
}
