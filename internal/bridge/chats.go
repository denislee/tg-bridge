package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
)

// Chat is the bridge-normalized representation of a Telegram dialog.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"` // "user", "group", "channel"
	Title    string `json:"title"`
	Unread   int    `json:"unread"`
	LastMsg  string `json:"last_msg,omitempty"`
	LastTs   int64  `json:"last_ts,omitempty"`
	Username string `json:"username,omitempty"`
}

// Message is the bridge-normalized representation of a message.
type Message struct {
	ID      int    `json:"id"`
	ChatID  int64  `json:"chat_id"`
	From    string `json:"from,omitempty"`
	FromID  int64  `json:"from_id,omitempty"`
	Text    string `json:"text"`
	Ts      int64  `json:"ts"`
	Out     bool   `json:"out"`
	ReplyTo int    `json:"reply_to,omitempty"`
	Media   any    `json:"media,omitempty"`
}

// ListChats returns up to limit recent dialogs.
func (b *Bridge) ListChats(ctx context.Context, limit int) ([]Chat, error) {
	if err := b.waitReady(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	res, err := b.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get dialogs: %w", err)
	}

	var (
		dialogs  []tg.DialogClass
		messages []tg.MessageClass
		users    []tg.UserClass
		chats    []tg.ChatClass
	)
	switch v := res.(type) {
	case *tg.MessagesDialogs:
		dialogs, messages, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	case *tg.MessagesDialogsSlice:
		dialogs, messages, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	default:
		return nil, fmt.Errorf("unexpected dialogs response %T", res)
	}

	msgByKey := indexMessages(messages)
	userByID := indexUsers(users)
	chatByID := indexChats(chats)

	out := make([]Chat, 0, len(dialogs))
	for _, d := range dialogs {
		dlg, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		c := Chat{Unread: dlg.UnreadCount}
		switch p := dlg.Peer.(type) {
		case *tg.PeerUser:
			c.ID = p.UserID
			c.Type = "user"
			if u := userByID[p.UserID]; u != nil {
				c.Title = strings.TrimSpace(u.FirstName + " " + u.LastName)
				c.Username = u.Username
				b.cachePeer(c.ID, &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash})
			}
		case *tg.PeerChat:
			c.ID = -p.ChatID
			c.Type = "group"
			if ch, ok := chatByID[p.ChatID].(*tg.Chat); ok {
				c.Title = ch.Title
				b.cachePeer(c.ID, &tg.InputPeerChat{ChatID: ch.ID})
			}
		case *tg.PeerChannel:
			c.ID = -1000000000000 - p.ChannelID
			c.Type = "channel"
			if ch, ok := chatByID[p.ChannelID].(*tg.Channel); ok {
				c.Title = ch.Title
				c.Username = ch.Username
				b.cachePeer(c.ID, &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
			}
		default:
			continue
		}

		if m, ok := msgByKey[msgKey(dlg.Peer, dlg.TopMessage)]; ok {
			if mm, ok := m.(*tg.Message); ok {
				c.LastMsg = truncate(mm.Message, 80)
				c.LastTs = int64(mm.Date)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// GetMessages returns up to limit recent messages from a chat.
func (b *Bridge) GetMessages(ctx context.Context, chatID int64, limit int, beforeID int) ([]Message, error) {
	if err := b.waitReady(ctx); err != nil {
		return nil, err
	}
	peer, err := b.resolvePeer(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	req := &tg.MessagesGetHistoryRequest{
		Peer:     peer,
		Limit:    limit,
		OffsetID: beforeID,
	}
	res, err := b.api.MessagesGetHistory(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	var (
		messages []tg.MessageClass
		users    []tg.UserClass
	)
	switch v := res.(type) {
	case *tg.MessagesMessages:
		messages, users = v.Messages, v.Users
	case *tg.MessagesMessagesSlice:
		messages, users = v.Messages, v.Users
	case *tg.MessagesChannelMessages:
		messages, users = v.Messages, v.Users
	default:
		return nil, fmt.Errorf("unexpected history response %T", res)
	}
	userByID := indexUsers(users)

	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		mm, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		msg := Message{
			ID:     mm.ID,
			ChatID: chatID,
			Text:   mm.Message,
			Ts:     int64(mm.Date),
			Out:    mm.Out,
		}
		if fromID, ok := mm.GetFromID(); ok {
			msg.FromID = peerToChatID(fromID)
			if pu, ok := fromID.(*tg.PeerUser); ok {
				if u := userByID[pu.UserID]; u != nil {
					msg.From = strings.TrimSpace(u.FirstName + " " + u.LastName)
				}
			}
		}
		if h, ok := mm.GetReplyTo(); ok {
			if rh, ok := h.(*tg.MessageReplyHeader); ok {
				msg.ReplyTo = rh.ReplyToMsgID
			}
		}
		if mm.Media != nil {
			msg.Media = b.summarizeMedia(mm.Media)
		}
		out = append(out, msg)
	}
	return out, nil
}

// MarkRead marks messages up to upToID as read in chatID.
func (b *Bridge) MarkRead(ctx context.Context, chatID int64, upToID int) error {
	if err := b.waitReady(ctx); err != nil {
		return err
	}
	peer, err := b.resolvePeer(ctx, chatID)
	if err != nil {
		return err
	}
	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := b.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			MaxID:   upToID,
		})
		return err
	}
	_, err = b.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
		Peer:  peer,
		MaxID: upToID,
	})
	return err
}

// warmPeerCache fetches recent dialogs to populate the peer cache so
// subsequent send/fetch calls can resolve chat IDs without extra lookups.
func (b *Bridge) warmPeerCache(ctx context.Context, limit int) error {
	_, err := b.ListChats(ctx, limit)
	return err
}

// --- helpers ---

func indexMessages(ms []tg.MessageClass) map[string]tg.MessageClass {
	out := map[string]tg.MessageClass{}
	for _, m := range ms {
		if mm, ok := m.(*tg.Message); ok {
			out[msgKey(mm.PeerID, mm.ID)] = m
		}
	}
	return out
}

func msgKey(p tg.PeerClass, id int) string {
	return fmt.Sprintf("%d:%d", peerToChatID(p), id)
}

func indexUsers(us []tg.UserClass) map[int64]*tg.User {
	out := map[int64]*tg.User{}
	for _, u := range us {
		if uu, ok := u.(*tg.User); ok {
			out[uu.ID] = uu
		}
	}
	return out
}

func indexChats(cs []tg.ChatClass) map[int64]tg.ChatClass {
	out := map[int64]tg.ChatClass{}
	for _, c := range cs {
		switch cc := c.(type) {
		case *tg.Chat:
			out[cc.ID] = cc
		case *tg.Channel:
			out[cc.ID] = cc
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// peerToChatID maps a Telegram PeerClass to our int64 chat id convention:
//   user      →  user_id
//   chat      → -chat_id
//   channel   → -1000000000000 - channel_id  (mirrors Bot API)
func peerToChatID(p tg.PeerClass) int64 {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID
	case *tg.PeerChat:
		return -v.ChatID
	case *tg.PeerChannel:
		return -1000000000000 - v.ChannelID
	}
	return 0
}

func (b *Bridge) cachePeer(chatID int64, peer tg.InputPeerClass) {
	b.peerMu.Lock()
	b.peerCache[chatID] = peer
	b.peerMu.Unlock()
}

func (b *Bridge) resolvePeer(ctx context.Context, chatID int64) (tg.InputPeerClass, error) {
	b.peerMu.RLock()
	p, ok := b.peerCache[chatID]
	b.peerMu.RUnlock()
	if ok {
		return p, nil
	}
	// Refresh dialogs and try again.
	if _, err := b.ListChats(ctx, 200); err != nil {
		return nil, err
	}
	b.peerMu.RLock()
	p, ok = b.peerCache[chatID]
	b.peerMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("chat %d not found", chatID)
	}
	return p, nil
}

// resolveName returns a human-readable name for a PeerClass using entities.
func resolveName(e tg.Entities, p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		if u, ok := e.Users[v.UserID]; ok {
			return strings.TrimSpace(u.FirstName + " " + u.LastName)
		}
	case *tg.PeerChat:
		if c, ok := e.Chats[v.ChatID]; ok {
			return c.Title
		}
	case *tg.PeerChannel:
		if c, ok := e.Channels[v.ChannelID]; ok {
			return c.Title
		}
	}
	return ""
}
