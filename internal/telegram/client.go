package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const apiBase = "https://api.telegram.org"

// Client wraps Telegram Bot API calls.
type Client struct {
	BotToken   string
	httpClient *http.Client
}

// New creates a Telegram Bot API client. Returns nil if botToken is empty.
func New(botToken string) *Client {
	if botToken == "" {
		return nil
	}
	return &Client{
		BotToken:   botToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SendMessage sends a text message to a Telegram chat (user DM).
func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})

	url := fmt.Sprintf("%s/bot%s/sendMessage", apiBase, c.BotToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send message: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// GetMe returns the bot's username for validation.
func (c *Client) GetMe(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/bot%s/getMe", apiBase, c.BotToken)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("getMe failed")
	}
	return result.Result.Username, nil
}
