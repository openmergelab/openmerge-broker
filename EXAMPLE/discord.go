package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const apiBase = "https://discord.com/api/v10"

// Client holds no state — all config comes from environment variables.
// Exists so it can be injected and mocked in tests.
type Client struct{}

// ─── OAuth ────────────────────────────────────────────────────────────────────

// DiscordUser holds the fields we need from Discord's /users/@me.
type DiscordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// ExchangeCode exchanges a Discord OAuth code for user info.
func ExchangeCode(ctx context.Context, code, redirectURI string) (*DiscordUser, error) {
	data := url.Values{
		"client_id":     {os.Getenv("DISCORD_CLIENT_ID")},
		"client_secret": {os.Getenv("DISCORD_CLIENT_SECRET")},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://discord.com/api/oauth2/token",
		strings.NewReader(data.Encode()),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}

	return GetUser(ctx, tokenResp.AccessToken)
}

// GetUser fetches the Discord user for a given access token.
func GetUser(ctx context.Context, accessToken string) (*DiscordUser, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		apiBase+"/users/@me", nil,
	)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get discord user: %w", err)
	}
	defer resp.Body.Close()

	var user DiscordUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode discord user: %w", err)
	}
	return &user, nil
}

// ─── Server management ────────────────────────────────────────────────────────

// EnsureMember checks if a user is in the Merge server and adds them if not.
// Called during onboarding to ensure both users are in the server
// before a match channel can be created for them.
func EnsureMember(ctx context.Context, discordUserID string) error {
	botToken := os.Getenv("DISCORD_BOT_TOKEN")
	guildID  := os.Getenv("DISCORD_GUILD_ID")

	// Check if already a member
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/guilds/%s/members/%s", apiBase, guildID, discordUserID),
		nil,
	)
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("check member: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// Already a member
		return nil
	}

	// Not a member — add them
	// Requires GUILD_MEMBERS intent and the user's OAuth access token
	// This is called during onboarding when we have the token
	return fmt.Errorf("user %s is not in the Merge server — "+
		"they must accept the server invite during onboarding", discordUserID)
}

// ─── Channel management ───────────────────────────────────────────────────────

// CreateMatchChannel creates a private channel in the Merge Discord server
// visible only to the two matched users and the bot.
//
// Option 2 architecture:
//   - Both users join the Merge server during onboarding (one-time step)
//   - At match time, broker creates a private channel for this pair only
//   - Bot posts the intro message
//   - Neither user had to initiate — conversation context already exists
//
// Channel name format: 🌿-handle-a-handle-b
// Truncated to 100 chars (Discord limit) and lowercased.
func CreateMatchChannel(ctx context.Context, handleA, handleB string) (string, error) {
	botToken := os.Getenv("DISCORD_BOT_TOKEN")
	guildID  := os.Getenv("DISCORD_GUILD_ID")

	if botToken == "" {
		return "", fmt.Errorf("DISCORD_BOT_TOKEN not set")
	}
	if guildID == "" {
		return "", fmt.Errorf("DISCORD_GUILD_ID not set")
	}

	channelName := sanitiseChannelName(
		fmt.Sprintf("meet-%s-%s", handleA, handleB),
	)

	body, _ := json.Marshal(map[string]interface{}{
		"name":                 channelName,
		"type":                 0,    // GUILD_TEXT
		"topic":                "Your Merge introduction. This channel is just for you two.",
		"permission_overwrites": []map[string]interface{}{
			// Deny @everyone — channel is private
			{
				"id":   guildID, // @everyone role has same ID as the guild
				"type": 0,       // role
				"deny": "1024",  // VIEW_CHANNEL
			},
		},
	})

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/guilds/%s/channels", apiBase, guildID),
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create channel: %w", err)
	}
	defer resp.Body.Close()

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

// AddUserToChannel grants a specific Discord user VIEW_CHANNEL permission
// on a private channel. Called once per user per match channel.
func AddUserToChannel(ctx context.Context, channelID, discordUserID string) error {
	botToken := os.Getenv("DISCORD_BOT_TOKEN")

	body, _ := json.Marshal(map[string]interface{}{
		"allow": "3072", // VIEW_CHANNEL + SEND_MESSAGES
		"type":  1,      // member (not role)
	})

	req, _ := http.NewRequestWithContext(ctx, "PUT",
		fmt.Sprintf("%s/channels/%s/permissions/%s", apiBase, channelID, discordUserID),
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("add user to channel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord permission error: status %d", resp.StatusCode)
	}
	return nil
}

// SendMessage posts a message to a Discord channel.
// Used only to send the introduction message.
// After this, the channel belongs to the two humans.
func SendMessage(ctx context.Context, channelID, content string) error {
	botToken := os.Getenv("DISCORD_BOT_TOKEN")

	body, _ := json.Marshal(map[string]string{"content": content})

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/channels/%s/messages", apiBase, channelID),
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord message error: status %d", resp.StatusCode)
	}
	return nil
}

// GenerateInvite creates a single-use server invite for onboarding.
// Sent to new users so they can join the Merge server during setup.
// max_uses=1 ensures the invite cannot be shared.
func GenerateInvite(ctx context.Context) (string, error) {
	botToken       := os.Getenv("DISCORD_BOT_TOKEN")
	onboardChannel := os.Getenv("DISCORD_ONBOARD_CHANNEL_ID")

	if onboardChannel == "" {
		return "", fmt.Errorf("DISCORD_ONBOARD_CHANNEL_ID not set")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"max_age":   604800, // 7 days
		"max_uses":  1,      // single use — cannot be shared
		"unique":    true,
		"temporary": false,
	})

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/channels/%s/invites", apiBase, onboardChannel),
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("generate invite: %w", err)
	}
	defer resp.Body.Close()

	var invite struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&invite); err != nil {
		return "", fmt.Errorf("decode invite: %w", err)
	}

	return "https://discord.gg/" + invite.Code, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// sanitiseChannelName makes a string safe for use as a Discord channel name.
// Discord requires: lowercase, no spaces, max 100 chars, alphanumeric + hyphens.
var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9-]`)

func sanitiseChannelName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nonAlphanumeric.ReplaceAllString(s, "")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
