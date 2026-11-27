// Package telegram is a small Telegram Bot API client: the few methods
// Stockpot needs (backups upload and download files, alerts send messages),
// with typed errors so callers can honour rate limits.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public Bot API.
const DefaultBaseURL = "https://api.telegram.org"

// MaxUploadBytes is the Bot API's limit for an uploaded file.
const MaxUploadBytes = 50 << 20

// Error is an error answered by the Bot API.
type Error struct {
	Method      string
	Code        int
	Description string
	// RetryAfter is set on 429: how long Telegram asks us to wait.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	s := fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return s
}

// Temporary reports whether retrying later may succeed: rate limits and
// server errors.
func (e *Error) Temporary() bool { return e.Code == http.StatusTooManyRequests || e.Code >= 500 }

// RetryAfter returns the wait Telegram asked for when err is a rate limit.
func RetryAfter(err error) (time.Duration, bool) {
	var e *Error
	if errors.As(err, &e) && e.Code == http.StatusTooManyRequests {
		return e.RetryAfter, true
	}
	return 0, false
}

// IsTemporary reports whether err is worth retrying: a rate limit, a server
// error or a network failure (but not a cancelled context).
func IsTemporary(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Temporary()
	}
	return true
}

// Client calls the Bot API for one bot token. Safe for concurrent use.
type Client struct {
	// Token is read per call, so a token edited in the admin panel applies
	// without a restart.
	token   func() string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at another API server (tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client. Its Timeout must be zero or generous:
// file transfers are large; per-call deadlines come from the context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New returns a client whose token is read through token on every call.
func New(token func() string, opts ...Option) *Client {
	c := &Client{token: token, baseURL: DefaultBaseURL, http: &http.Client{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ErrNoToken means no bot token is configured.
var ErrNoToken = errors.New("telegram: bot token is not set")

// ChatID is a numeric chat id ("-1001234567890") or a channel @username.
type ChatID string

// User is a Telegram bot or user.
type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

// Document is a file attached to a message.
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
}

// Chat identifies where a message lives.
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Message is the subset of the Bot API message Stockpot reads.
type Message struct {
	MessageID int64     `json:"message_id"`
	Chat      Chat      `json:"chat"`
	From      *User     `json:"from"`
	Date      int64     `json:"date"`
	Text      string    `json:"text"`
	Document  *Document `json:"document"`
}

// Update is one item of getUpdates.
type Update struct {
	UpdateID    int64    `json:"update_id"`
	Message     *Message `json:"message"`
	ChannelPost *Message `json:"channel_post"`
}

// File is the answer of getFile.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) endpoint(method string) (string, error) {
	t := c.token()
	if t == "" {
		return "", ErrNoToken
	}
	return c.baseURL + "/bot" + t + "/" + method, nil
}

// do sends req and decodes the envelope's result into out (if not nil).
func (c *Client) do(req *http.Request, method string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return redact(err, c.token())
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return redact(err, c.token())
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return &Error{Method: method, Code: resp.StatusCode, Description: "unreadable response"}
	}
	if !env.OK {
		e := &Error{Method: method, Code: env.ErrorCode, Description: env.Description}
		if e.Code == 0 {
			e.Code = resp.StatusCode
		}
		if env.Parameters != nil {
			e.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		return e
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

// redact keeps the bot token (part of every URL) out of network errors.
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, strings.ReplaceAll(ue.URL, token, "<token>"), ue.Err)
	}
	return err
}

// call posts JSON params to method.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	ep, err := c.endpoint(method)
	if err != nil {
		return err
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, method, out)
}

// GetMe checks the token and returns the bot.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// SendMessage sends a plain text message.
func (c *Client) SendMessage(ctx context.Context, chat ChatID, text string) (Message, error) {
	var m Message
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id": chat, "text": text, "disable_web_page_preview": true,
	}, &m)
	return m, err
}

// SendDocument uploads content as a file, streaming it (the body is never
// held in memory by the client). The caption is limited to 1024 characters
// by Telegram.
func (c *Client) SendDocument(ctx context.Context, chat ChatID, filename, caption string, content io.Reader) (Message, error) {
	ep, err := c.endpoint("sendDocument")
	if err != nil {
		return Message{}, err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := mw.WriteField("chat_id", string(chat)); err != nil {
				return err
			}
			if caption != "" {
				if err := mw.WriteField("caption", caption); err != nil {
					return err
				}
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename=%q`, filename))
			h.Set("Content-Type", "application/octet-stream")
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, content); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return Message{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var m Message
	err = c.do(req, "sendDocument", &m)
	_ = pr.CloseWithError(err) // unblocks the writer if the server answered early
	return m, err
}

// GetFile resolves a file id to a downloadable path.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

// Download streams the file at path (from GetFile). The caller closes the
// body. Bots can download files up to 20 MB only, so anything that
// must be fetched back (backup parts) is kept below that.
func (c *Client) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	t := c.token()
	if t == "" {
		return nil, ErrNoToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/file/bot"+t+"/"+strings.TrimLeft(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redact(err, t)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		e := &Error{Method: "download", Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
		if s := resp.Header.Get("Retry-After"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				e.RetryAfter = time.Duration(n) * time.Second
			}
		}
		return nil, e
	}
	return resp.Body, nil
}

// DeleteMessage removes a message. Bots can only delete messages younger
// than 48 hours in most chats.
func (c *Client) DeleteMessage(ctx context.Context, chat ChatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": messageID}, nil)
}

// ForwardMessage forwards a message (used to resolve a message link into
// its file ids). The forwarded copy is a new message the caller may delete.
func (c *Client) ForwardMessage(ctx context.Context, to, from ChatID, messageID int64) (Message, error) {
	var m Message
	err := c.call(ctx, "forwardMessage", map[string]any{
		"chat_id": to, "from_chat_id": from, "message_id": messageID,
	}, &m)
	return m, err
}

// GetUpdates long-polls for updates after offset. timeout is the server-side
// wait in seconds (0 = return immediately).
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var us []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout,
		"allowed_updates": []string{"message", "channel_post"},
	}, &us)
	return us, err
}
