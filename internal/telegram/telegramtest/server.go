// Package telegramtest is an in-memory fake of the Telegram Bot API for
// tests: it stores sent messages and documents and serves them back.
package telegramtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Token is the bot token the fake accepts.
const Token = "123456:TEST"

// Sent is a message or document the bot sent.
type Sent struct {
	MessageID int64
	ChatID    string
	Text      string
	Caption   string
	Filename  string
	FileID    string
	Data      []byte
	Deleted   bool
}

// Server is the fake. Hooks may be set before use.
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	nextID int64
	sent   []*Sent
	files  map[string]*Sent
	calls  map[string]int

	// Fail, if set, is consulted before each API call; returning a status
	// (and optional retry_after seconds) makes the call fail.
	Fail func(method string, n int) (status int, retryAfter int)
	// MaxFile rejects uploads larger than this many bytes (0 = 50 MB).
	MaxFile int
}

// New starts a fake API; it is closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{files: map[string]*Sent{}, calls: map[string]int{}, nextID: 100}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Messages returns a copy of everything sent so far.
func (s *Server) Messages() []Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Sent, len(s.sent))
	for i, m := range s.sent {
		out[i] = *m
	}
	return out
}

// Calls counts the calls made to method.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, desc string, retry int) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retry > 0 {
		body["parameters"] = map[string]any{"retry_after": retry}
	}
	reply(w, code, body)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if rest, ok := strings.CutPrefix(path, "file/bot"+Token+"/"); ok {
		s.download(w, rest)
		return
	}
	rest, ok := strings.CutPrefix(path, "bot"+Token+"/")
	if !ok {
		fail(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	s.mu.Lock()
	s.calls[rest]++
	n := s.calls[rest]
	s.mu.Unlock()
	if s.Fail != nil {
		if st, ra := s.Fail(rest, n); st != 0 {
			_, _ = io.Copy(io.Discard, r.Body)
			fail(w, st, "injected failure", ra)
			return
		}
	}
	switch rest {
	case "getMe":
		reply(w, 200, map[string]any{"ok": true, "result": map[string]any{"id": 1, "is_bot": true, "username": "stockpot_test_bot"}})
	case "sendMessage":
		var p struct {
			ChatID any    `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		m := s.add(&Sent{ChatID: fmt.Sprint(p.ChatID), Text: p.Text})
		reply(w, 200, map[string]any{"ok": true, "result": s.msg(m)})
	case "sendDocument":
		s.sendDocument(w, r)
	case "getFile":
		var p struct {
			FileID string `json:"file_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		f := s.files[p.FileID]
		s.mu.Unlock()
		if f == nil {
			fail(w, 400, "Bad Request: invalid file_id", 0)
			return
		}
		reply(w, 200, map[string]any{"ok": true, "result": map[string]any{
			"file_id": f.FileID, "file_size": len(f.Data), "file_path": "documents/" + f.FileID,
		}})
	case "deleteMessage":
		var p struct {
			MessageID int64 `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		var found *Sent
		for _, m := range s.sent {
			if m.MessageID == p.MessageID && !m.Deleted {
				found = m
			}
		}
		if found != nil {
			found.Deleted = true
		}
		s.mu.Unlock()
		if found == nil {
			fail(w, 400, "Bad Request: message to delete not found", 0)
			return
		}
		reply(w, 200, map[string]any{"ok": true, "result": true})
	case "forwardMessage":
		var p struct {
			ChatID    any   `json:"chat_id"`
			MessageID int64 `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		var src *Sent
		for _, m := range s.sent {
			if m.MessageID == p.MessageID && !m.Deleted {
				src = m
			}
		}
		s.mu.Unlock()
		if src == nil {
			fail(w, 400, "Bad Request: message to forward not found", 0)
			return
		}
		cp := *src
		cp.ChatID = fmt.Sprint(p.ChatID)
		m := s.add(&cp)
		reply(w, 200, map[string]any{"ok": true, "result": s.msg(m)})
	case "getUpdates":
		reply(w, 200, map[string]any{"ok": true, "result": []any{}})
	default:
		fail(w, 404, "Not Found", 0)
	}
}

func (s *Server) add(m *Sent) *Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	m.MessageID = s.nextID
	s.sent = append(s.sent, m)
	if m.FileID != "" {
		s.files[m.FileID] = m
	}
	return m
}

func (s *Server) msg(m *Sent) map[string]any {
	out := map[string]any{"message_id": m.MessageID, "chat": map[string]any{"id": -1001, "type": "channel"}, "text": m.Text}
	if m.FileID != "" {
		out["document"] = map[string]any{"file_id": m.FileID, "file_name": m.Filename, "file_size": len(m.Data)}
	}
	return out
}

func (s *Server) sendDocument(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "Bad Request: multipart", 0)
		return
	}
	m := &Sent{}
	limit := s.MaxFile
	if limit == 0 {
		limit = 50 << 20
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, "Bad Request: multipart", 0)
			return
		}
		b, err := io.ReadAll(p)
		if err != nil {
			fail(w, 400, "Bad Request: read", 0)
			return
		}
		switch p.FormName() {
		case "chat_id":
			m.ChatID = string(b)
		case "caption":
			m.Caption = string(b)
		case "document":
			m.Filename, m.Data = p.FileName(), b
		}
	}
	if len(m.Data) > limit {
		fail(w, 413, "Request Entity Too Large", 0)
		return
	}
	s.mu.Lock()
	id := len(s.files) + 1
	s.mu.Unlock()
	m.FileID = "file-" + strconv.Itoa(id)
	s.add(m)
	reply(w, 200, map[string]any{"ok": true, "result": s.msg(m)})
}

func (s *Server) download(w http.ResponseWriter, rest string) {
	id := strings.TrimPrefix(rest, "documents/")
	s.mu.Lock()
	f := s.files[id]
	s.mu.Unlock()
	if f == nil {
		http.NotFound(w, nil)
		return
	}
	// Like the real API, files over 20 MB cannot be fetched by bots.
	if len(f.Data) > 20<<20 {
		http.Error(w, "too big", http.StatusBadRequest)
		return
	}
	_, _ = w.Write(f.Data)
}
