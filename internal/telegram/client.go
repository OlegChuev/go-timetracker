// Package telegram is a small Bot API client built on the standard library.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.telegram.org"

// APIError is a non-ok response from the Bot API.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Client talks to the Telegram Bot API.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
	log     *slog.Logger
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL points the client at another Bot API host, such as a local Bot
// API server or a test double.
func WithBaseURL(base string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(base, "/") }
}

// New builds a client. The HTTP timeout must outlast the long poll timeout,
// since getUpdates deliberately holds the connection open while idle.
func New(token string, log *slog.Logger, opts ...Option) *Client {
	c := &Client{
		token:   token,
		baseURL: defaultAPIBase,
		http:    &http.Client{Timeout: 90 * time.Second},
		log:     log,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// call posts a JSON payload to a Bot API method and decodes result into out.
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode %s payload: %w", method, err)
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, url.PathEscape(c.token), method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}

	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("decode %s response (http %d): %w", method, resp.StatusCode, err)
	}
	if !parsed.OK {
		apiErr := &APIError{Method: method, Code: parsed.ErrorCode, Description: parsed.Description}
		if parsed.Parameters != nil {
			apiErr.RetryAfter = parsed.Parameters.RetryAfter
		}
		return apiErr
	}
	if out != nil && len(parsed.Result) > 0 {
		if err := json.Unmarshal(parsed.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// GetMe verifies the token and returns the bot account.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var me User
	if err := c.call(ctx, "getMe", nil, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

// GetUpdates long polls for new events starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var updates []Update
	if err := c.call(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage delivers text to a chat and returns the sent message.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error) {
	var sent Message
	if err := c.call(ctx, "sendMessage", req, &sent); err != nil {
		return nil, err
	}
	return &sent, nil
}

// EditMessageText replaces the text and markup of an existing message. Telegram
// rejects an edit that changes nothing, which is not a real failure here.
func (c *Client) EditMessageText(ctx context.Context, req EditMessageTextRequest) error {
	err := c.call(ctx, "editMessageText", req, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

// AnswerCallbackQuery clears the loading spinner on an inline button.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	}, nil)
}

// SetMyCommands publishes the command list shown in the Telegram menu.
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

// DeleteWebhook drops any configured webhook so long polling can run.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}
