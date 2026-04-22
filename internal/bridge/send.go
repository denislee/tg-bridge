package bridge

import (
	"context"
	"fmt"
)

// SendText sends a plain-text message to chatID. Fails fast: no retry queue.
func (b *Bridge) SendText(ctx context.Context, chatID int64, text string) (int, error) {
	if err := b.waitReady(ctx); err != nil {
		return 0, err
	}
	peer, err := b.resolvePeer(ctx, chatID)
	if err != nil {
		return 0, err
	}
	upd, err := b.sender.To(peer).Text(ctx, text)
	if err != nil {
		return 0, fmt.Errorf("send: %w", err)
	}
	// Best-effort extraction of the new message id. Not all update types
	// expose it directly; return 0 if unavailable.
	return extractMessageID(upd), nil
}
