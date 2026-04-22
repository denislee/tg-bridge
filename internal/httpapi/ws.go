package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"tg-bridge/internal/bridge"
	"tg-bridge/internal/config"
)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // LAN use; TLS already terminated
	})
	if err != nil {
		s.log.Warn("ws accept", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sub, unsub := s.br.Events()
	defer unsub()

	cl := clientFromCtx(r.Context())

	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	pingTick := time.NewTicker(30 * time.Second)
	defer pingTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if !eventAllowed(cl, ev) {
				continue
			}
			if err := writeEvent(ctx, conn, ev); err != nil {
				return
			}
		case <-pingTick.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

func writeEvent(ctx context.Context, conn *websocket.Conn, ev bridge.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, data)
}

func eventAllowed(cl *config.ClientConfig, ev bridge.Event) bool {
	if cl == nil || len(cl.AllowedChats) == 0 {
		return true
	}
	for _, id := range cl.AllowedChats {
		if id == ev.ChatID {
			return true
		}
	}
	return false
}
