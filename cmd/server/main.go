// dinq-connector: Platform connector with per-user OAuth and MCP tool execution.
//
// Two endpoints:
//   - HTTP API (:8091) — OAuth management, health, account CRUD
//   - MCP Server (:8091/mcp) — Tool execution with per-user tokens
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/dinq"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/discord_bot"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/github"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/gmail"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/outlook"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/smtp_email"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/twitter"
	"github.com/DINQ-labs/dinq-connector/internal/apify"
	"github.com/DINQ-labs/dinq-connector/internal/auth"
	"github.com/DINQ-labs/dinq-connector/internal/composio"
	"github.com/DINQ-labs/dinq-connector/internal/db"
	"github.com/DINQ-labs/dinq-connector/internal/httpapi"
	"github.com/DINQ-labs/dinq-connector/internal/mcpserver"
)

func main() {
	// --- Config from env ---
	databaseURL := envOrDefault("DATABASE_URL", "postgres://localhost:5432/dinq_connector?sslmode=disable")
	baseURL := envOrDefault("BASE_URL", "http://localhost:8091")
	port := envIntOrDefault("PORT", 8091)

	// --- Database ---
	store, err := db.New(databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer store.Close()
	log.Println("[DB] Connected and migrated")

	// --- Adapter Registry ---
	registry := adapter.NewRegistry()
	registry.Register(github.New())

	// Gmail direct OAuth adapter — takes priority over Composio Gmail when configured.
	useDirectGmail := os.Getenv("GMAIL_CLIENT_ID") != ""
	if useDirectGmail {
		registry.Register(gmail.New())
		log.Println("[Registry] Gmail registered (direct OAuth 2.0)")
	}

	// Dinq platform adapter — always available, no OAuth needed.
	// Uses X-User-ID header to call dinq-server internal APIs on behalf of the user.
	if dinqServerURL := envOrDefault("DINQ_SERVER_URL", ""); dinqServerURL != "" {
		registry.Register(dinq.New(dinqServerURL))
		log.Printf("[Registry] Dinq registered (internal auth, server=%s)", dinqServerURL)
	}

	// Outlook direct OAuth adapter — takes priority over Composio Outlook when configured.
	useDirectOutlook := os.Getenv("OUTLOOK_CLIENT_ID") != ""
	if useDirectOutlook {
		registry.Register(outlook.New())
		log.Println("[Registry] Outlook registered (direct OAuth 2.0)")
	}

	// SMTP Email adapter — credentials-based, no OAuth.
	registry.Register(smtp_email.New())
	log.Println("[Registry] SMTP Email registered (credentials auth)")

	// Composio-backed adapters (v3 API uses auth_config_id directly, no integration UUIDs needed)
	if apiKey := os.Getenv("COMPOSIO_API_KEY"); apiKey != "" {
		cc := composio.NewClient(apiKey)
		ctx := context.Background()

		dynamicPlatforms := []struct {
			envKey      string
			platform    string
			displayName string
			appName     string
			descs       map[string]string // description overrides (localName → description)
			exclude     []string          // tool names to exclude
		}{
			{"COMPOSIO_LINKEDIN_AUTH_CONFIG_ID", "linkedin", "LinkedIn", "linkedin", linkedinDescs, nil},
			{"COMPOSIO_GMAIL_AUTH_CONFIG_ID", "gmail", "Gmail", "gmail", gmailDescs, []string{"delete_message"}},
			{"COMPOSIO_GOOGLE_CALENDAR_AUTH_CONFIG_ID", "googlecalendar", "Google Calendar", "googlecalendar", googleCalendarDescs, nil},
			{"COMPOSIO_GOOGLE_SHEETS_AUTH_CONFIG_ID", "googlesheets", "Google Sheets", "googlesheets", googleSheetsDescs, nil},
			{"COMPOSIO_NOTION_AUTH_CONFIG_ID", "notion", "Notion", "notion", notionDescs, nil},
			{"COMPOSIO_SLACK_AUTH_CONFIG_ID", "slack", "Slack", "slack", slackDescs, nil},
			{"COMPOSIO_DISCORD_AUTH_CONFIG_ID", "discord", "Discord", "discord", discordComposioDescs, nil},
			{"COMPOSIO_OUTLOOK_AUTH_CONFIG_ID", "outlook", "Outlook", "outlook", outlookDescs, nil},
			{"COMPOSIO_REDDIT_AUTH_CONFIG_ID", "reddit", "Reddit", "reddit", redditDescs, nil},
		}

		discordBotToken := os.Getenv("DISCORD_BOT_TOKEN")
		for _, p := range dynamicPlatforms {
			if p.platform == "discord" && discordBotToken != "" {
				continue
			}
			if p.platform == "gmail" && useDirectGmail {
				continue
			}
			if p.platform == "outlook" && useDirectOutlook {
				continue
			}
			authConfigID := os.Getenv(p.envKey)
			if authConfigID == "" {
				continue
			}
			a, err := adapter.NewDynamicComposioAdapter(ctx, cc, p.platform, p.displayName, authConfigID, p.appName, p.descs, p.exclude...)
			if err != nil {
				log.Printf("[Registry] Warning: %s skipped: %v", p.displayName, err)
				continue
			}
			registry.Register(a)
			log.Printf("[Registry] %s registered (%d tools via Composio)", p.displayName, len(a.Tools()))
		}

		// Discord Bot Token adapter — replaces Composio discord (can send messages)
		if discordBotToken != "" {
			registry.Register(discord_bot.New(discordBotToken))
			log.Println("[Registry] Discord registered (Bot Token — send_message enabled)")
		}
	}

	if os.Getenv("TWITTER_CLIENT_ID") != "" {
		registry.Register(twitter.New())
		log.Println("[Registry] Twitter registered (direct OAuth 2.0)")
	}

	// Attach Apify post-search tools to LinkedIn and Twitter adapters.
	// These tools appear under connector_discover_tools(platform="linkedin/twitter").
	if apifyToken := os.Getenv("APIFY_API_KEY"); apifyToken != "" {
		apifyClient := apify.NewClient(apifyToken)
		attachApifySearchTools(registry, apifyClient)
	}

	log.Printf("[Registry] %d adapters registered", len(registry.List()))

	// --- Auth Manager ---
	authMgr := auth.NewManager(store, registry, baseURL)

	// Direct OAuth configs (for non-Composio adapters like GitHub)
	if id := os.Getenv("GITHUB_CLIENT_ID"); id != "" {
		authMgr.SetConfig("github", id, os.Getenv("GITHUB_CLIENT_SECRET"), "repo,read:user,read:org")
		log.Println("[Auth] GitHub OAuth configured")
	}

	if id := os.Getenv("GMAIL_CLIENT_ID"); id != "" {
		authMgr.SetConfig("gmail", id, os.Getenv("GMAIL_CLIENT_SECRET"),
			"https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/gmail.send openid email")
		log.Println("[Auth] Gmail OAuth configured")
	}

	if id := os.Getenv("TWITTER_CLIENT_ID"); id != "" {
		authMgr.SetConfig("twitter", id, os.Getenv("TWITTER_CLIENT_SECRET"), "tweet.read,tweet.write,users.read,offline.access")
		log.Println("[Auth] Twitter OAuth configured")
	}

	if id := os.Getenv("OUTLOOK_CLIENT_ID"); id != "" {
		authMgr.SetConfig("outlook", id, os.Getenv("OUTLOOK_CLIENT_SECRET"), "Mail.Send Mail.Read User.Read offline_access")
		log.Println("[Auth] Outlook OAuth configured")
	}

	// --- HTTP API + MCP on same port ---
	// MCP at /mcp, HTTP API (OAuth callbacks, health) at everything else.
	httpHandler := httpapi.New(authMgr, registry)
	mcpHandler := mcpserver.NewHandler(registry, authMgr, "/mcp")

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)
	mux.Handle("/", httpHandler.Handler())

	addr := ":" + strconv.Itoa(port)
	log.Printf("[Server] Starting on %s (HTTP API + MCP at /mcp)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// attachApifySearchTools adds Apify-backed search tools to the LinkedIn and Twitter adapters.
func attachApifySearchTools(registry *adapter.Registry, client *apify.Client) {
	if li := registry.Get("linkedin"); li != nil {
		if ca, ok := li.(*adapter.ComposioAdapter); ok {
			ca.AddExtraTool(adapter.ExtraTool{
				LocalName:   "search_posts",
				Description: "[READ] Search LinkedIn posts by keywords. Returns matching posts with author, content, likes, and URL. Call when researching what people are saying about a topic, finding thought leaders, or monitoring industry discussions.",
				Schema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"keywords": map[string]any{"type": "string", "description": "Search keywords or phrase"},
						"limit":    map[string]any{"type": "integer", "description": "Max results to return (default 10, max 50)"},
					},
					Required: []string{"keywords"},
				},
				Execute: func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
					maxPosts := 10
					if l, ok := args["limit"].(float64); ok {
						maxPosts = int(l)
					}
					input := map[string]any{
						"searchQueries": []string{fmt.Sprintf("%v", args["keywords"])},
						"maxPosts":      maxPosts,
					}
					items, err := client.RunActor(ctx, "harvestapi~linkedin-post-search", input)
					if err != nil {
						return mcp.NewToolResultError("LinkedIn search error: " + err.Error()), nil
					}
					data, _ := json.Marshal(items)
					return mcp.NewToolResultText(string(data)), nil
				},
			})
			log.Println("[Registry] LinkedIn: attached Apify post search tool")
		}
	}

	if tw := registry.Get("twitter"); tw != nil {
		addTwitterSearch := func(t adapter.ExtraTool) {
			if ca, ok := tw.(*adapter.ComposioAdapter); ok {
				ca.AddExtraTool(t)
			} else if ta, ok := tw.(*twitter.Adapter); ok {
				ta.AddExtraTool(t)
			}
		}
		addTwitterSearch(adapter.ExtraTool{
			LocalName:   "search_posts",
			Description: "[READ] Search Twitter/X posts by keywords or hashtags. Returns matching tweets with author, content, likes, retweets, and URL. Call when researching trending topics, finding relevant voices in a field, or monitoring what's being said about a company or person.",
			Schema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query, keywords, or hashtags"},
					"limit": map[string]any{"type": "integer", "description": "Max results to return (default 10, max 100)"},
				},
				Required: []string{"query"},
			},
			Execute: func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
				limit := 10
				if l, ok := args["limit"].(float64); ok {
					limit = int(l)
				}
				input := map[string]any{
					"query":        fmt.Sprintf("%v", args["query"]),
					"resultsCount": limit,
				}
				items, err := client.RunActor(ctx, "scraper_one~x-posts-search", input)
				if err != nil {
					return mcp.NewToolResultError("Twitter search error: " + err.Error()), nil
				}
				data, _ := json.Marshal(items)
				return mcp.NewToolResultText(string(data)), nil
			},
		})
		log.Println("[Registry] Twitter: attached Apify post search tool")
	}
}

// --- Platform description overrides ---
// Composio's generic tool descriptions are written for Composio's dashboard, not for an AI assistant.
// These maps override them with context-aware descriptions: when to call, what to confirm, what the result is used for.
// Key = localName (Composio slug with platform prefix stripped and lowercased, e.g. GMAIL_SEND_EMAIL → send_email).
// Tools not listed here fall back to Composio's description.

var linkedinDescs = map[string]string{
	"create_post":      "[WRITE — confirm before calling] Publish a post to LinkedIn, visible to the user's network. Confirm the exact text before posting — it's public and immediately visible. Best for sharing insights, career updates, or job signals.",
	"create_text_post": "[WRITE — confirm before calling] Publish a text post to LinkedIn. Confirm content before posting — it's public.",
	"create_link_post": "[WRITE — confirm before calling] Share a URL post on LinkedIn with commentary. Confirm text and URL before posting.",
	"get_profile":      "[READ] Get the authenticated user's LinkedIn profile: headline, experience, education, skills. Call to understand their professional background before suggesting actions.",
	"get_my_profile":   "[READ] Get the authenticated user's LinkedIn profile summary. Useful for personalizing outreach or understanding their positioning.",
	"send_message":     "[WRITE — confirm before calling] Send a LinkedIn direct message to a connection. Confirm recipient and message content — LinkedIn DMs are personal and visible to the recipient.",
	"search_people":    "[READ] Search LinkedIn for people by name, title, or company. Useful for finding connections or researching professionals.",
	"get_connections":  "[READ] List the user's LinkedIn connections. Use to find who they know before suggesting networking strategies.",
	"like_post":        "[WRITE — confirm before calling] Like a LinkedIn post on behalf of the user. Confirm the post before acting.",
	"comment_on_post":  "[WRITE — confirm before calling] Comment on a LinkedIn post. Confirm the comment text — it's public.",
	"get_feed":         "[READ] Get the user's LinkedIn feed. Useful for monitoring industry trends or finding posts to engage with.",
	"get_post":         "[READ] Get details of a specific LinkedIn post by ID.",
	"get_company":      "[READ] Get a LinkedIn company page: description, employee count, industry, recent posts.",
	"get_job_postings": "[READ] Get job postings from a LinkedIn company page. Useful for job research.",
	"search_jobs":      "[READ] Search LinkedIn jobs by keywords, location, or company. Call when the user is job hunting.",
}

var gmailDescs = map[string]string{
	"send_email":              "[WRITE — confirm before calling] Send an email from the user's Gmail account. Confirm recipient(s), subject, and body before sending — emails cannot be unsent.",
	"reply_to_email":          "[WRITE — confirm before calling] Reply to an existing Gmail thread. Confirm the reply content before sending.",
	"reply_to_thread":         "[WRITE — confirm before calling] Reply to a Gmail thread. Confirm content before sending.",
	"forward_email":           "[WRITE — confirm before calling] Forward an email to another recipient. Confirm recipient and any added message before sending.",
	"create_draft":            "[WRITE-SAFE] Save an email as a draft in Gmail without sending. Use when the user wants to review before sending.",
	"get_message":             "[READ] Get the full content of a Gmail message by ID: sender, recipients, subject, body. Use when you have a message ID from list_messages or search_messages.",
	"list_messages":           "[READ] List Gmail messages matching a query (sender, subject, date, label). Returns message IDs — call get_message to read full content.",
	"search_messages":         "[READ] Search Gmail by keywords, sender, date range, or label. Useful for finding specific conversations or checking if something was sent.",
	"get_labels":              "[READ] List all Gmail labels (folders/categories). Useful before filtering searches by label.",
	"get_thread":              "[READ] Get all messages in a Gmail thread. Call to read the full conversation history.",
	"list_threads":            "[READ] List Gmail threads matching a query. Each thread groups related messages.",
	"mark_as_read":            "[WRITE-SAFE] Mark a Gmail message as read. Safe to call without confirmation.",
	"add_label_to_email":      "[WRITE-SAFE] Add a label to a Gmail message for organization. Safe to call without confirmation.",
	"remove_label_from_email": "[WRITE-SAFE] Remove a label from a Gmail message. Safe to call without confirmation.",
	"create_email_draft":      "[WRITE-SAFE] Save a draft email without sending. Safe — nothing is sent until the user explicitly sends it.",
}

var googleCalendarDescs = map[string]string{
	"create_event":      "[WRITE — confirm before calling] Create a new Google Calendar event. Confirm title, date/time, location, and attendees before creating — invites are sent to attendees immediately.",
	"list_events":       "[READ] List upcoming events from Google Calendar. Call when checking schedule, availability, or planning around existing commitments.",
	"get_event":         "[READ] Get the full details of a specific calendar event by ID.",
	"update_event":      "[WRITE — confirm before calling] Update an existing calendar event (title, time, location, attendees). Confirm changes — attendees will be notified.",
	"delete_event":      "[WRITE — confirm before calling] Delete a calendar event permanently. Confirm with the user first — attendees will receive a cancellation notice.",
	"quick_add":         "[WRITE — confirm before calling] Create a calendar event from a natural-language string (e.g. 'Lunch with Alice tomorrow at noon'). Confirm the parsed event before creating.",
	"find_free_slots":   "[READ] Find available time slots in the user's calendar. Useful for scheduling meetings or suggesting when to block focus time.",
	"get_calendar_list": "[READ] List all Google Calendars the user has (personal, work, shared). Call to find which calendar to create events in.",
}

var googleSheetsDescs = map[string]string{
	"get_sheet":          "[READ] Read data from a Google Sheet range (e.g. 'Sheet1!A1:D20'). Requires spreadsheet ID and range.",
	"read_spreadsheet":   "[READ] Read data from a Google Spreadsheet. Returns cell values for a specified range.",
	"update_spreadsheet": "[WRITE — confirm before calling] Write data to a range in a Google Sheet. Confirm target range and data — this overwrites existing content.",
	"create_spreadsheet": "[WRITE — confirm before calling] Create a new Google Spreadsheet. Confirm the title with the user.",
	"append_rows":        "[WRITE — confirm before calling] Append rows to a Google Sheet. Confirm the data and target sheet before appending.",
	"batch_update":       "[WRITE — confirm before calling] Update multiple ranges in a Google Sheet in one call. Confirm all changes before applying.",
	"clear_range":        "[WRITE — confirm before calling] Clear all content in a Google Sheet range. Confirm the range — this permanently deletes cell data.",
	"list_spreadsheets":  "[READ] List the user's Google Spreadsheets from Drive. Useful for finding a spreadsheet to read or update.",
}

var notionDescs = map[string]string{
	"create_page":      "[WRITE — confirm before calling] Create a new Notion page or database entry. Confirm the title, parent location, and content before creating.",
	"get_page":         "[READ] Get the content and properties of a specific Notion page by ID.",
	"update_page":      "[WRITE — confirm before calling] Update a Notion page's properties or content. Confirm changes before applying.",
	"search":           "[READ] Search the Notion workspace by keyword. Call before creating to check if similar content exists, or to find a page to update.",
	"create_database":  "[WRITE — confirm before calling] Create a new Notion database. Confirm structure and parent page with the user.",
	"query_database":   "[READ] Query a Notion database with filters. Use to fetch structured data like tasks, contacts, or job applications.",
	"add_page_content": "[WRITE — confirm before calling] Add blocks/content to an existing Notion page. Confirm content before adding.",
	"get_database":     "[READ] Get the schema and properties of a Notion database by ID.",
	"list_databases":   "[READ] List accessible Notion databases. Call to find database IDs before querying.",
	"list_pages":       "[READ] List pages in the Notion workspace. Useful for browsing available content.",
}

var slackDescs = map[string]string{
	"send_message":         "[WRITE — confirm before calling] Send a message to a Slack channel or DM. This posts publicly (or privately in DMs) — confirm channel and message content before sending.",
	"list_channels":        "[READ] List Slack channels accessible to the user. Call first to find channel names and IDs before sending messages or reading history.",
	"get_channel_messages": "[READ] Read recent messages from a Slack channel. Call to check conversation context before posting or responding.",
	"reply_to_message":     "[WRITE — confirm before calling] Reply in a Slack thread. Confirm the reply content before posting — it's visible to channel members.",
	"create_channel":       "[WRITE — confirm before calling] Create a new Slack channel. Confirm name, topic, and purpose with the user.",
	"invite_to_channel":    "[WRITE — confirm before calling] Invite a user to a Slack channel. Confirm recipient and channel before inviting.",
	"get_user_info":        "[READ] Get a Slack user's profile: name, email, status, role. Useful for finding user IDs before sending DMs.",
	"list_users":           "[READ] List Slack workspace members. Call to find user IDs before messaging or inviting.",
	"set_status":           "[WRITE — confirm before calling] Set the user's Slack status (emoji + message). Confirm the status text.",
	"search_messages":      "[READ] Search Slack messages across channels by keyword. Useful for finding specific conversations or decisions.",
	"upload_file":          "[WRITE — confirm before calling] Upload a file to a Slack channel. Confirm file and target channel before uploading.",
	"get_channel_info":     "[READ] Get details of a Slack channel: topic, members, purpose. Useful before posting or inviting.",
	"get_reactions":        "[READ] Get emoji reactions on a Slack message.",
	"add_reaction":         "[WRITE-SAFE] Add an emoji reaction to a Slack message. Generally safe — confirm emoji and message if unsure.",
	"schedule_message":     "[WRITE — confirm before calling] Schedule a Slack message for a future time. Confirm channel, content, and send time before scheduling.",
}

var discordComposioDescs = map[string]string{
	"send_message": "[WRITE — confirm before calling] Send a message to a Discord channel via the user's account. Confirm channel and content before sending — it's publicly visible.",
	"get_messages": "[READ] Read recent messages from a Discord channel.",
	"get_guilds":   "[READ] List Discord servers the user is in. Call first to find guild_ids.",
	"get_channels": "[READ] List channels in a Discord server. Call to find channel_ids before sending messages.",
}

var outlookDescs = map[string]string{
	"send_email":      "[WRITE — confirm before calling] Send an email from the user's Outlook account. Confirm recipient(s), subject, and body — emails cannot be unsent.",
	"reply_to_email":  "[WRITE — confirm before calling] Reply to an Outlook email thread. Confirm reply content before sending.",
	"forward_email":   "[WRITE — confirm before calling] Forward an Outlook email. Confirm recipient and any added message before sending.",
	"create_draft":    "[WRITE-SAFE] Save an Outlook email draft without sending. Safe — nothing is sent until explicitly sent.",
	"list_messages":   "[READ] List emails from the Outlook inbox or a specific folder. Useful for checking recent messages.",
	"get_message":     "[READ] Get the full content of a specific Outlook email by ID.",
	"search_messages": "[READ] Search Outlook emails by keyword, sender, or date. Useful for finding specific conversations.",
	"delete_message":  "[WRITE — confirm before calling] Permanently delete an Outlook email. Confirm with the user first.",
	"move_message":    "[WRITE-SAFE] Move an email to a different Outlook folder for organization.",
	"mark_as_read":    "[WRITE-SAFE] Mark an Outlook email as read. Safe to call without confirmation.",
	"list_folders":    "[READ] List Outlook mail folders. Useful for understanding inbox organization.",
	"create_event":    "[WRITE — confirm before calling] Create a calendar event in Outlook Calendar. Confirm title, time, and attendees before creating.",
	"list_events":     "[READ] List upcoming events from Outlook Calendar. Useful for checking availability.",
	"list_contacts":   "[READ] List contacts from the user's Outlook address book. Useful for finding email addresses.",
}

var redditDescs = map[string]string{
	"submit_post":          "[WRITE — confirm before calling] Submit a post to a Reddit subreddit. Confirm subreddit, title, and content before posting — it will be publicly visible.",
	"create_comment":       "[WRITE — confirm before calling] Post a comment on a Reddit thread. Confirm content before posting — it's public and tied to the user's account.",
	"get_subreddit_posts":  "[READ] Get posts from a subreddit sorted by hot/new/top. Call to research community discussions or find relevant threads.",
	"get_post_comments":    "[READ] Get comments from a Reddit post. Useful for reading community discussion.",
	"get_my_profile":       "[READ] Get the authenticated user's Reddit profile: karma, account age, recent posts.",
	"vote":                 "[WRITE-SAFE] Upvote or downvote a Reddit post or comment. Confirm direction if unsure.",
	"save_post":            "[WRITE-SAFE] Save a Reddit post to the user's saved list. Safe to call.",
	"search":               "[READ] Search Reddit posts by keyword across all or specific subreddits. Useful for finding relevant discussions or sentiment on a topic.",
	"get_user":             "[READ] Get a Reddit user's public profile. Useful for researching a user's history or credibility.",
	"subscribe":            "[WRITE — confirm before calling] Subscribe to a subreddit. Confirm the subreddit name before subscribing.",
	"send_private_message": "[WRITE — confirm before calling] Send a Reddit private message (DM). Confirm recipient and content — it's private but tied to the user's account.",
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
