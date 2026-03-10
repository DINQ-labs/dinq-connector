// Package twitter implements the PlatformAdapter for Twitter/X using direct OAuth 2.0.
package twitter

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

const apiBase = "https://api.twitter.com"

// Adapter implements adapter.PlatformAdapter for Twitter/X via direct OAuth 2.0.
type Adapter struct {
	extraTools []adapter.ExtraTool
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) AddExtraTool(t adapter.ExtraTool) {
	a.extraTools = append(a.extraTools, t)
}

func (a *Adapter) Name() string                   { return "twitter" }
func (a *Adapter) DisplayName() string            { return "Twitter / X" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthOAuth2 }

func (a *Adapter) OAuthConfig() *adapter.OAuthConfig {
	return &adapter.OAuthConfig{
		AuthorizeURL: "https://twitter.com/i/oauth2/authorize",
		TokenURL:     "https://api.twitter.com/2/oauth2/token",
		Scopes:       []string{"tweet.read", "tweet.write", "users.read", "offline.access"},
		PKCE:         true,
		BasicAuth:    true, // Twitter token endpoint requires Basic Auth header
	}
}

func (a *Adapter) Tools() []mcp.Tool {
	tools := []mcp.Tool{
		mcp.NewTool("twitter_create_tweet",
			mcp.WithDescription("Post a new tweet. Free tier limit: 500 tweets/month per user."),
			mcp.WithString("text", mcp.Required(), mcp.Description("Tweet text (max 280 characters)")),
		),
		mcp.NewTool("twitter_delete_tweet",
			mcp.WithDescription("Delete a tweet by its ID."),
			mcp.WithString("tweet_id", mcp.Required(), mcp.Description("The tweet ID to delete")),
		),
		mcp.NewTool("twitter_get_me",
			mcp.WithDescription("Get the authenticated user's Twitter profile (id, name, username, description)."),
		),
	}
	for _, t := range a.extraTools {
		tools = append(tools, mcp.Tool{
			Name:        "twitter_" + t.LocalName,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return tools
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, token, _ string) (*mcp.CallToolResult, error) {
	for _, t := range a.extraTools {
		if t.LocalName == toolName {
			return t.Execute(ctx, args)
		}
	}
	switch toolName {
	case "create_tweet":
		return a.createTweet(ctx, args, token)
	case "delete_tweet":
		return a.deleteTweet(ctx, args, token)
	case "get_me":
		return a.getMe(ctx, token)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

func (a *Adapter) createTweet(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	text, _ := args["text"].(string)
	if text == "" {
		return mcp.NewToolResultError("text is required"), nil
	}
	body, _ := json.Marshal(map[string]string{"text": text})
	data, err := twitterPost(ctx, "/2/tweets", body, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) deleteTweet(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	tweetID, _ := args["tweet_id"].(string)
	if tweetID == "" {
		return mcp.NewToolResultError("tweet_id is required"), nil
	}
	data, err := twitterDelete(ctx, "/2/tweets/"+tweetID, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getMe(ctx context.Context, token string) (*mcp.CallToolResult, error) {
	data, err := twitterGet(ctx, "/2/users/me?user.fields=id,name,username,description,profile_image_url,public_metrics", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func twitterGet(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doRequest(req)
}

func twitterPost(ctx context.Context, path string, body []byte, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doRequest(req)
}

func twitterDelete(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "DELETE", apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
		return nil, fmt.Errorf("Twitter API %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
