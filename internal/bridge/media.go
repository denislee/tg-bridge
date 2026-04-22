package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

// MediaSummary is a lightweight descriptor embedded in Event/Message so the
// device knows media exists without downloading the full payload. If
// MediaID is empty the bridge cannot serve the media (e.g. documents in v1).
type MediaSummary struct {
	Type     string `json:"type"`     // "photo", "video", "voice", "document", "sticker", "other"
	MediaID  string `json:"media_id,omitempty"`
	MimeType string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Width    int    `json:"w,omitempty"`
	Height   int    `json:"h,omitempty"`
}

type mediaKind uint8

const (
	mediaKindPhoto mediaKind = 1
)

// mediaRef carries enough information to re-fetch a media file from Telegram.
// It is serialized and HMAC-signed into the opaque MediaID exposed to clients.
type mediaRef struct {
	Kind       mediaKind
	ID         int64
	AccessHash int64
	FileRef    []byte
	ThumbSize  string // photo thumb type ("s", "m", ...)
}

// summarizeMedia turns a MessageMediaClass into a MediaSummary with a
// signed MediaID where the bridge can serve the payload.
func (b *Bridge) summarizeMedia(m tg.MessageMediaClass) *MediaSummary {
	switch v := m.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := v.Photo.(*tg.Photo)
		if !ok {
			return &MediaSummary{Type: "photo"}
		}
		thumbType, w, h, size, ok := pickSmallestPhotoSize(p.Sizes)
		if !ok {
			return &MediaSummary{Type: "photo"}
		}
		id := b.encodeMediaID(mediaRef{
			Kind:       mediaKindPhoto,
			ID:         p.ID,
			AccessHash: p.AccessHash,
			FileRef:    p.FileReference,
			ThumbSize:  thumbType,
		})
		return &MediaSummary{
			Type:     "photo",
			MediaID:  id,
			MimeType: "image/jpeg",
			Size:     int64(size),
			Width:    w,
			Height:   h,
		}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return &MediaSummary{Type: "document"}
		}
		return &MediaSummary{
			Type:     classifyDoc(d),
			MimeType: d.MimeType,
			Size:     d.Size,
		}
	}
	return &MediaSummary{Type: "other"}
}

func classifyDoc(d *tg.Document) string {
	for _, a := range d.Attributes {
		switch a.(type) {
		case *tg.DocumentAttributeAudio:
			return "voice"
		case *tg.DocumentAttributeVideo:
			return "video"
		case *tg.DocumentAttributeSticker:
			return "sticker"
		}
	}
	return "document"
}

// pickSmallestPhotoSize returns the smallest downloadable PhotoSize type
// among the provided sizes. Stripped/cached inline thumbs are skipped since
// they are not fetched via downloader.
func pickSmallestPhotoSize(sizes []tg.PhotoSizeClass) (sizeType string, w, h, bytes int, ok bool) {
	bestPx := -1
	for _, s := range sizes {
		switch ss := s.(type) {
		case *tg.PhotoSize:
			px := ss.W * ss.H
			if bestPx < 0 || px < bestPx {
				bestPx = px
				sizeType, w, h, bytes, ok = ss.Type, ss.W, ss.H, ss.Size, true
			}
		case *tg.PhotoSizeProgressive:
			px := ss.W * ss.H
			if bestPx < 0 || px < bestPx {
				bestPx = px
				sizeType, w, h, ok = ss.Type, ss.W, ss.H, true
				if len(ss.Sizes) > 0 {
					bytes = ss.Sizes[0]
				}
			}
		}
	}
	return
}

// --- HMAC-signed opaque IDs ---

const mediaTagSize = 16

// encodeMediaID returns a base64url(payload || HMAC16(payload)).
func (b *Bridge) encodeMediaID(ref mediaRef) string {
	payload := marshalMediaRef(ref)
	mac := hmac.New(sha256.New, b.mediaKey)
	mac.Write(payload)
	tag := mac.Sum(nil)[:mediaTagSize]
	out := make([]byte, 0, len(payload)+mediaTagSize)
	out = append(out, payload...)
	out = append(out, tag...)
	return base64.RawURLEncoding.EncodeToString(out)
}

// decodeMediaID verifies the HMAC and returns the underlying mediaRef.
func (b *Bridge) decodeMediaID(s string) (mediaRef, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return mediaRef{}, fmt.Errorf("decode media id: %w", err)
	}
	if len(raw) < mediaTagSize+1 {
		return mediaRef{}, errors.New("media id too short")
	}
	payload := raw[:len(raw)-mediaTagSize]
	tag := raw[len(raw)-mediaTagSize:]
	mac := hmac.New(sha256.New, b.mediaKey)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil)[:mediaTagSize], tag) {
		return mediaRef{}, errors.New("media id signature mismatch")
	}
	return unmarshalMediaRef(payload)
}

// Layout: [kind:1][id:8][access_hash:8][fileref_len:2][fileref][size_len:1][size]
func marshalMediaRef(r mediaRef) []byte {
	out := make([]byte, 0, 1+8+8+2+len(r.FileRef)+1+len(r.ThumbSize))
	out = append(out, byte(r.Kind))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(r.ID))
	out = append(out, buf[:]...)
	binary.BigEndian.PutUint64(buf[:], uint64(r.AccessHash))
	out = append(out, buf[:]...)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(r.FileRef)))
	out = append(out, l[:]...)
	out = append(out, r.FileRef...)
	out = append(out, byte(len(r.ThumbSize)))
	out = append(out, r.ThumbSize...)
	return out
}

func unmarshalMediaRef(b []byte) (mediaRef, error) {
	if len(b) < 1+8+8+2 {
		return mediaRef{}, errors.New("payload truncated")
	}
	r := mediaRef{Kind: mediaKind(b[0])}
	b = b[1:]
	r.ID = int64(binary.BigEndian.Uint64(b[:8]))
	b = b[8:]
	r.AccessHash = int64(binary.BigEndian.Uint64(b[:8]))
	b = b[8:]
	frLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if len(b) < frLen+1 {
		return mediaRef{}, errors.New("payload truncated (fileref)")
	}
	r.FileRef = make([]byte, frLen)
	copy(r.FileRef, b[:frLen])
	b = b[frLen:]
	szLen := int(b[0])
	b = b[1:]
	if len(b) < szLen {
		return mediaRef{}, errors.New("payload truncated (size)")
	}
	r.ThumbSize = string(b[:szLen])
	return r, nil
}

// FetchMedia returns the local path and mime type for the given media ID,
// downloading and caching if necessary.
func (b *Bridge) FetchMedia(ctx context.Context, mediaID string) (string, string, error) {
	if p, mime, ok := b.mediaCache.Get(mediaID); ok {
		return p, mime, nil
	}
	ref, err := b.decodeMediaID(mediaID)
	if err != nil {
		return "", "", err
	}
	if err := b.waitReady(ctx); err != nil {
		return "", "", err
	}
	switch ref.Kind {
	case mediaKindPhoto:
		loc := &tg.InputPhotoFileLocation{
			ID:            ref.ID,
			AccessHash:    ref.AccessHash,
			FileReference: ref.FileRef,
			ThumbSize:     ref.ThumbSize,
		}
		mime := "image/jpeg"
		path, err := b.mediaCache.Put(mediaID, mime, func(w io.Writer) error {
			_, err := downloader.NewDownloader().Download(b.api, loc).Stream(ctx, w)
			return err
		})
		return path, mime, err
	default:
		return "", "", fmt.Errorf("unsupported media kind %d", ref.Kind)
	}
}

// extractMessageID pulls a message id out of a send result.
func extractMessageID(u tg.UpdatesClass) int {
	switch v := u.(type) {
	case *tg.UpdateShortSentMessage:
		return v.ID
	case *tg.Updates:
		for _, up := range v.Updates {
			switch uu := up.(type) {
			case *tg.UpdateNewMessage:
				if m, ok := uu.Message.(*tg.Message); ok {
					return m.ID
				}
			case *tg.UpdateNewChannelMessage:
				if m, ok := uu.Message.(*tg.Message); ok {
					return m.ID
				}
			case *tg.UpdateMessageID:
				return uu.ID
			}
		}
	}
	return 0
}
