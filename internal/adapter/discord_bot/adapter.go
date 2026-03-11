// Package discord_bot implements a Discord adapter using a Bot Token.
// Unlike OAuth user tokens, bot tokens can send messages to channels.
package discord_bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const apiBase = "https://discord.com/api/v10"

// Adapter implements PlatformAdapter for Discord using a Bot Token.
// The bot token is global (not per-user), so no OAuth flow is needed.
type Adapter struct {
	token string
}

func New(botToken string) *Adapter {
	return &Adapter{token: botToken}
}

func (a *Adapter) Name() string                      { return "discord" }
func (a *Adapter) DisplayName() string               { return "Discord" }
func (a *Adapter) AuthScheme() adapter.AuthScheme    { return adapter.AuthBotToken }
func (a *Adapter) OAuthConfig() *adapter.OAuthConfig { return nil }

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("discord_send_message",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Send a message to a Discord channel. "+
					"Posts publicly in the server — confirm channel and content first. "+
					"If channel_id is unknown, call discord_get_channels first to find it.",
			),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Discord channel ID (get from discord_get_channels)")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Message text to send")),
		),
		mcp.NewTool("discord_get_messages",
			mcp.WithDescription(
				"[READ] Read recent messages from a Discord channel. "+
					"Call to check conversation history, monitor activity, or gather context before responding. "+
					"If channel_id is unknown, call discord_get_channels first.",
			),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Discord channel ID (get from discord_get_channels)")),
			mcp.WithNumber("limit", mcp.Description("Number of messages to fetch (default 20, max 100)")),
		),
		mcp.NewTool("discord_get_guilds",
			mcp.WithDescription(
				"[READ] List all Discord servers the bot is a member of. "+
					"Call first to discover available servers and get guild_ids — needed before calling discord_get_channels.",
			),
		),
		mcp.NewTool("discord_get_channels",
			mcp.WithDescription(
				"[READ] List all channels in a Discord server. "+
					"Call to find channel names and IDs — required before sending messages or reading history. "+
					"Requires guild_id from discord_get_guilds.",
			),
			mcp.WithString("guild_id", mcp.Required(), mcp.Description("Discord server (guild) ID (get from discord_get_guilds)")),
		),
		mcp.NewTool("discord_get_me",
			mcp.WithDescription(
				"[READ] Get the bot's Discord identity: username, id, discriminator. "+
					"Useful for debugging or when the user asks about the bot's Discord account.",
			),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, _, _ string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "send_message":
		return a.sendMessage(ctx, args)
	case "get_messages":
		return a.getMessages(ctx, args)
	case "get_guilds":
		return a.get(ctx, "/users/@me/guilds")
	case "get_channels":
		guildID, _ := args["guild_id"].(string)
		if guildID == "" {
			return mcp.NewToolResultError("guild_id is required"), nil
		}
		return a.get(ctx, "/guilds/"+guildID+"/channels")
	case "get_me":
		return a.get(ctx, "/users/@me")
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

func (a *Adapter) sendMessage(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	channelID, _ := args["channel_id"].(string)
	content, _ := args["content"].(string)
	if channelID == "" || content == "" {
		return mcp.NewToolResultError("channel_id and content are required"), nil
	}
	body, _ := json.Marshal(map[string]string{"content": content})
	data, err := a.post(ctx, "/channels/"+channelID+"/messages", body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getMessages(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	channelID, _ := args["channel_id"].(string)
	if channelID == "" {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}
	return a.get(ctx, fmt.Sprintf("/channels/%s/messages?limit=%d", channelID, limit))
}

func (a *Adapter) get(ctx context.Context, path string) (*mcp.CallToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+path, nil)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	req.Header.Set("Authorization", "Bot "+a.token)
	data, err := doRequest(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+a.token)
	req.Header.Set("Content-Type", "application/json")
	return doRequest(req)
}

func doRequest(req *http.Request) ([]byte, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Discord API %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
