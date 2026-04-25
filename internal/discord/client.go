package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const apiBase = "https://discord.com/api/v10"

// Client wraps Discord bot API calls.
type Client struct {
	BotToken           string
	GuildID            string
	OnboardChannelID   string
	httpClient         *http.Client
}

// New creates a Discord API client. Returns nil if botToken is empty.
func New(botToken, guildID, onboardChannelID string) *Client {
	if botToken == "" {
		return nil
	}
	return &Client{
		BotToken:         botToken,
		GuildID:          guildID,
		OnboardChannelID: onboardChannelID,
		httpClient:       &http.Client{Timeout: 15 * time.Second},
	}
}

// CreateMatchChannel creates a private text channel visible only to the two
// matched users and the bot.
func (c *Client) CreateMatchChannel(ctx context.Context, handleA, handleB string) (string, error) {
	channelName := SanitiseChannelName(
		fmt.Sprintf("meet-%s-%s", handleA, handleB),
	)

	body, _ := json.Marshal(map[string]interface{}{
		"name":  channelName,
		"type":  0, // GUILD_TEXT
		"topic": "Your Merge introduction. This channel is just for you two.",
		"permission_overwrites": []map[string]interface{}{
			{
				"id":   c.GuildID, // @everyone role has same ID as the guild
				"type": 0,         // role
				"deny": "1024",    // VIEW_CHANNEL
			},
		},
	})

	resp, err := c.doRequestWithRetry(ctx, "POST",
		fmt.Sprintf("%s/guilds/%s/channels", apiBase, c.GuildID), body)
	if err != nil {
		return "", fmt.Errorf("create channel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create channel: status %d: %s", resp.StatusCode, respBody)
	}

	var channel struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&channel); err != nil {
		return "", fmt.Errorf("decode channel response: %w", err)
	}
	if channel.ID == "" {
		return "", fmt.Errorf("discord returned empty channel id")
	}
	return channel.ID, nil
}

// AddUserToChannel grants a specific Discord user VIEW_CHANNEL + SEND_MESSAGES
// permission on a private channel.
func (c *Client) AddUserToChannel(ctx context.Context, channelID, discordUserID string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"allow": "3072", // VIEW_CHANNEL + SEND_MESSAGES
		"type":  1,      // member (not role)
	})

	resp, err := c.doRequestWithRetry(ctx, "PUT",
		fmt.Sprintf("%s/channels/%s/permissions/%s", apiBase, channelID, discordUserID), body)
	if err != nil {
		return fmt.Errorf("add user to channel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add user to channel: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// SendMessage posts a message to a Discord channel.
func (c *Client) SendMessage(ctx context.Context, channelID, content string) error {
	body, _ := json.Marshal(map[string]string{"content": content})

	resp, err := c.doRequestWithRetry(ctx, "POST",
		fmt.Sprintf("%s/channels/%s/messages", apiBase, channelID), body)
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

// GenerateInvite creates a single-use server invite for onboarding.
func (c *Client) GenerateInvite(ctx context.Context) (string, error) {
	if c.OnboardChannelID == "" {
		return "", fmt.Errorf("onboard channel ID not configured")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"max_age":   604800, // 7 days
		"max_uses":  1,
		"unique":    true,
		"temporary": false,
	})

	resp, err := c.doRequestWithRetry(ctx, "POST",
		fmt.Sprintf("%s/channels/%s/invites", apiBase, c.OnboardChannelID), body)
	if err != nil {
		return "", fmt.Errorf("generate invite: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("generate invite: status %d: %s", resp.StatusCode, respBody)
	}

	var invite struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&invite); err != nil {
		return "", fmt.Errorf("decode invite: %w", err)
	}
	return "https://discord.gg/" + invite.Code, nil
}

// EnsureMember checks if a user is in the guild.
func (c *Client) EnsureMember(ctx context.Context, discordUserID string) (bool, error) {
	resp, err := c.doRequestWithRetry(ctx, "GET",
		fmt.Sprintf("%s/guilds/%s/members/%s", apiBase, c.GuildID, discordUserID), nil)
	if err != nil {
		return false, fmt.Errorf("check member: %w", err)
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// JoinGuild adds a user to the guild using their OAuth2 access token.
// Requires the guilds.join OAuth scope and the bot must be in the guild.
// Returns true if the user was added or was already a member.
func (c *Client) JoinGuild(ctx context.Context, discordUserID, accessToken string) (bool, error) {
	body, _ := json.Marshal(map[string]string{
		"access_token": accessToken,
	})

	resp, err := c.doRequestWithRetry(ctx, "PUT",
		fmt.Sprintf("%s/guilds/%s/members/%s", apiBase, c.GuildID, discordUserID), body)
	if err != nil {
		return false, fmt.Errorf("join guild: %w", err)
	}
	defer resp.Body.Close()

	// 201 = added, 204 = already a member
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
		return true, nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("join guild: status %d: %s", resp.StatusCode, respBody)
}

// doRequestWithRetry sends a request and retries once on 429 (rate limit).
func (c *Client) doRequestWithRetry(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.BotToken)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		// Rate limited — wait for Retry-After
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()

		wait := 1 * time.Second
		if secs, err := strconv.ParseFloat(retryAfter, 64); err == nil && secs > 0 {
			wait = time.Duration(secs*1000) * time.Millisecond
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
			// retry
		}
	}

	return nil, fmt.Errorf("rate limited after retries")
}
