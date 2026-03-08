// Package twitter provides a Composio-backed Twitter adapter.
// Free tier: post tweets (1,500/month), delete tweets, get own profile.
package twitter

import (
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/composio"
)

// Composio action IDs for Twitter.
// Verify these in the Composio dashboard: https://app.composio.dev
const (
	ActionCreateTweet = "TWITTER_CREATION_OF_A_TWEET"
	ActionDeleteTweet = "TWITTER_TWEET_DELETE"
	ActionGetMe       = "TWITTER_USER_LOOKUP_ME"
)

// New creates a Twitter adapter backed by Composio.
func New(client *composio.Client, authConfigID string) *adapter.ComposioAdapter {
	return adapter.NewComposioAdapter(client, adapter.ComposioAdapterConfig{
		Platform:     "twitter",
		DisplayName_: "Twitter / X",
		AuthConfigID: authConfigID,
		AppName:      "twitter",
		Tools_:       tools(),
	})
}

func tools() []adapter.ComposioToolMapping {
	return []adapter.ComposioToolMapping{
		{
			LocalName:      "create_tweet",
			ComposioAction: ActionCreateTweet,
			Description:    "Post a new tweet on Twitter/X. Free tier limit: 1,500 tweets per month.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"text": map[string]any{
						"type":        "string",
						"description": "The tweet text content (max 280 characters).",
					},
					"reply_to": map[string]any{
						"type":        "string",
						"description": "Tweet ID to reply to (optional).",
					},
					"quote_tweet_id": map[string]any{
						"type":        "string",
						"description": "Tweet ID to quote (optional).",
					},
				},
				Required: []string{"text"},
			},
		},
		{
			LocalName:      "delete_tweet",
			ComposioAction: ActionDeleteTweet,
			Description:    "Delete a tweet by its ID.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"tweet_id": map[string]any{
						"type":        "string",
						"description": "The ID of the tweet to delete.",
					},
				},
				Required: []string{"tweet_id"},
			},
		},
		{
			LocalName:      "get_me",
			ComposioAction: ActionGetMe,
			Description:    "Get the authenticated Twitter user's profile information.",
			InputSchema: mcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{},
			},
		},
	}
}
