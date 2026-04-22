package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	self := s.br.Self()
	if self == nil {
		writeError(w, http.StatusServiceUnavailable, "not authorized yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       self.ID,
		"username": self.Username,
		"first":    self.FirstName,
		"last":     self.LastName,
		"phone":    self.Phone,
	})
}

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	chats, err := s.br.ListChats(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Apply per-client allow-list filter if present.
	if cl := clientFromCtx(r.Context()); cl != nil && len(cl.AllowedChats) > 0 {
		allow := map[int64]struct{}{}
		for _, id := range cl.AllowedChats {
			allow[id] = struct{}{}
		}
		filtered := chats[:0]
		for _, c := range chats {
			if _, ok := allow[c.ID]; ok {
				filtered = append(filtered, c)
			}
		}
		chats = filtered
	}
	writeJSON(w, http.StatusOK, chats)
}

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	chatID, err := pathChatID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !clientCanAccess(r, chatID) {
		writeError(w, http.StatusForbidden, "chat not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	before, _ := strconv.Atoi(r.URL.Query().Get("before"))

	msgs, err := s.br.GetMessages(r.Context(), chatID, limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	chatID, err := pathChatID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !clientCanAccess(r, chatID) {
		writeError(w, http.StatusForbidden, "chat not allowed")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text required")
		return
	}
	id, err := s.br.SendText(r.Context(), chatID, req.Text)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"msg_id": id})
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	chatID, err := pathChatID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !clientCanAccess(r, chatID) {
		writeError(w, http.StatusForbidden, "chat not allowed")
		return
	}
	var req struct {
		UpTo int `json:"up_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "up_to required")
		return
	}
	if err := s.br.MarkRead(r.Context(), chatID, req.UpTo); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "media id required")
		return
	}
	path, mime, err := s.br.FetchMedia(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, path)
}

func pathChatID(r *http.Request) (int64, error) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func clientCanAccess(r *http.Request, chatID int64) bool {
	cl := clientFromCtx(r.Context())
	if cl == nil || len(cl.AllowedChats) == 0 {
		return true
	}
	for _, id := range cl.AllowedChats {
		if id == chatID {
			return true
		}
	}
	return false
}
