package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/contrib/bbolt"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	bolt "go.etcd.io/bbolt"

	"tg-bridge/internal/config"
)

// Bridge wraps a single-user gotd client and exposes a narrow API for the
// HTTP layer.
type Bridge struct {
	cfg  *config.Config
	log  *slog.Logger
	auth *httpAuthenticator

	// set after client.Run starts — nil until then
	clientReady atomic.Bool
	client      *telegram.Client
	api         *tg.Client
	sender      *message.Sender
	self        atomic.Pointer[tg.User]

	events *broker

	// peer cache: our chat_id -> InputPeer
	peerMu    sync.RWMutex
	peerCache map[int64]tg.InputPeerClass

	mediaKey   []byte
	mediaCache *mediaCache

	boltDB *bolt.DB
}

func New(cfg *config.Config, log *slog.Logger) (*Bridge, error) {
	if err := os.MkdirAll(cfg.Telegram.SessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	boltDB, err := bolt.Open(filepath.Join(cfg.Telegram.SessionDir, "updates.db"), 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open updates db: %w", err)
	}

	mediaKey, err := loadOrCreateMediaKey(cfg.Telegram.SessionDir)
	if err != nil {
		_ = boltDB.Close()
		return nil, fmt.Errorf("media key: %w", err)
	}
	mc, err := newMediaCache(cfg.Media.CacheDir, cfg.Media.MaxBytes)
	if err != nil {
		_ = boltDB.Close()
		return nil, fmt.Errorf("media cache: %w", err)
	}

	b := &Bridge{
		cfg:        cfg,
		log:        log,
		auth:       newHTTPAuthenticator(),
		events:     newBroker(64),
		peerCache:  map[int64]tg.InputPeerClass{},
		mediaKey:   mediaKey,
		mediaCache: mc,
		boltDB:     boltDB,
	}
	return b, nil
}

func (b *Bridge) Close() error {
	if b.boltDB != nil {
		return b.boltDB.Close()
	}
	return nil
}

// AuthStatus reports the current state of the auth flow.
func (b *Bridge) AuthStatus() (AuthState, error) {
	return b.auth.Status()
}

// SubmitPhone / SubmitCode / SubmitPassword are invoked by HTTP handlers.
func (b *Bridge) SubmitPhone(p string) error {
	if !b.auth.SubmitPhone(p) {
		return errors.New("no phone prompt pending")
	}
	return nil
}
func (b *Bridge) SubmitCode(c string) error {
	if !b.auth.SubmitCode(c) {
		return errors.New("no code prompt pending")
	}
	return nil
}
func (b *Bridge) SubmitPassword(p string) error {
	if !b.auth.SubmitPassword(p) {
		return errors.New("no password prompt pending")
	}
	return nil
}

// Events returns a subscription for WS clients.
func (b *Bridge) Events() (<-chan Event, func()) { return b.events.Subscribe() }

// Self returns the authenticated user (nil before auth completes).
func (b *Bridge) Self() *tg.User { return b.self.Load() }

// Run blocks until ctx is cancelled or a fatal error occurs.
func (b *Bridge) Run(ctx context.Context) error {
	sessionFile := filepath.Join(b.cfg.Telegram.SessionDir, "session.json")

	dispatcher := tg.NewUpdateDispatcher()
	updatesMgr := updates.New(updates.Config{
		Handler: dispatcher,
		Storage: bbolt.NewStateStorage(b.boltDB),
		Logger:  nil,
	})

	b.client = telegram.NewClient(b.cfg.Telegram.APIID, b.cfg.Telegram.APIHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionFile},
		UpdateHandler:  updatesMgr,
	})

	dispatcher.OnNewMessage(b.handleNewMessage)
	dispatcher.OnNewChannelMessage(b.handleNewChannelMessage)

	return b.client.Run(ctx, func(ctx context.Context) error {
		b.api = b.client.API()
		b.sender = message.NewSender(b.api)
		b.clientReady.Store(true)

		flow := auth.NewFlow(b.auth, auth.SendCodeOptions{})
		if err := b.client.Auth().IfNecessary(ctx, flow); err != nil {
			b.auth.setState(AuthError, err)
			return fmt.Errorf("auth: %w", err)
		}
		b.auth.setState(AuthAuthorized, nil)

		self, err := b.client.Self(ctx)
		if err != nil {
			return fmt.Errorf("fetch self: %w", err)
		}
		b.self.Store(self)
		b.log.Info("authorized", "user_id", self.ID, "username", self.Username)

		// Warm the peer cache from recent dialogs. Non-fatal on error.
		if err := b.warmPeerCache(ctx, 100); err != nil {
			b.log.Warn("warm peer cache failed", "err", err)
		}

		return updatesMgr.Run(ctx, b.api, self.ID, updates.AuthOptions{
			IsBot:      self.Bot,
			OnStart:    func(context.Context) { b.log.Info("updates manager started") },
		})
	})
}

// --- update handlers ---

func (b *Bridge) handleNewMessage(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
	return b.dispatchMessage(e, u.Message)
}

func (b *Bridge) handleNewChannelMessage(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
	return b.dispatchMessage(e, u.Message)
}

func (b *Bridge) dispatchMessage(e tg.Entities, m tg.MessageClass) error {
	msg, ok := m.(*tg.Message)
	if !ok || msg.Out {
		return nil
	}
	chatID := peerToChatID(msg.PeerID)
	if !b.chatAllowed(chatID) {
		return nil
	}
	ev := Event{
		Type:   "message",
		ChatID: chatID,
		MsgID:  msg.ID,
		Text:   msg.Message,
		Ts:     int64(msg.Date),
	}
	if fromID, ok := msg.GetFromID(); ok {
		ev.FromID = peerToChatID(fromID)
		ev.From = resolveName(e, fromID)
	} else {
		ev.From = resolveName(e, msg.PeerID)
	}
	if h, ok := msg.GetReplyTo(); ok {
		if rh, ok := h.(*tg.MessageReplyHeader); ok {
			ev.ReplyTo = rh.ReplyToMsgID
		}
	}
	if msg.Media != nil {
		ev.Media = b.summarizeMedia(msg.Media)
	}
	b.events.Publish(ev)
	return nil
}

// chatAllowed returns true if the current client config permits this chat.
// For v1 we apply the first configured client's allow-list globally; the
// HTTP layer still gates requests per-token.
func (b *Bridge) chatAllowed(chatID int64) bool {
	if len(b.cfg.Clients) == 0 {
		return true
	}
	allow := b.cfg.Clients[0].AllowedChats
	if len(allow) == 0 {
		return true
	}
	for _, id := range allow {
		if id == chatID {
			return true
		}
	}
	return false
}

// waitReady blocks until the client is running or ctx expires.
func (b *Bridge) waitReady(ctx context.Context) error {
	if b.clientReady.Load() && b.Self() != nil {
		return nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if b.clientReady.Load() && b.Self() != nil {
				return nil
			}
		}
	}
}
