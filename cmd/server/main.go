// dinq-connector: Platform connector with per-user OAuth and MCP tool execution.
//
// Two endpoints:
//   - HTTP API (:8091) — OAuth management, health, account CRUD
//   - MCP Server (:8091/mcp) — Tool execution with per-user tokens
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/github"
	"github.com/DINQ-labs/dinq-connector/internal/adapter/twitter"
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

	// Composio-backed adapters
	if apiKey := os.Getenv("COMPOSIO_API_KEY"); apiKey != "" {
		cc := composio.NewClient(apiKey)
		ctx := context.Background()

		// Fetch all integration UUIDs once (needed for InitiateConnection v3 API)
		integrations, err := cc.GetIntegrations(ctx)
		if err != nil {
			log.Printf("[Composio] Warning: failed to fetch integrations: %v", err)
			integrations = map[string]composio.Integration{}
		} else {
			log.Printf("[Composio] Loaded %d integration UUIDs", len(integrations))
		}
		integrationID := func(appName string) string {
			if i, ok := integrations[appName]; ok {
				return i.ID
			}
			return ""
		}

		// (Twitter is now direct OAuth 2.0; see registration below)

		// All other platforms: dynamic — tools fetched from Composio API at startup
		dynamicPlatforms := []struct {
			envKey      string
			platform    string
			displayName string
			appName     string
		}{
			{"COMPOSIO_LINKEDIN_AUTH_CONFIG_ID", "linkedin", "LinkedIn", "linkedin"},
			{"COMPOSIO_GMAIL_AUTH_CONFIG_ID", "gmail", "Gmail", "gmail"},
			{"COMPOSIO_GOOGLE_CALENDAR_AUTH_CONFIG_ID", "googlecalendar", "Google Calendar", "googlecalendar"},
			{"COMPOSIO_GOOGLE_SHEETS_AUTH_CONFIG_ID", "googlesheets", "Google Sheets", "googlesheets"},
			{"COMPOSIO_NOTION_AUTH_CONFIG_ID", "notion", "Notion", "notion"},
			{"COMPOSIO_SLACK_AUTH_CONFIG_ID", "slack", "Slack", "slack"},
			{"COMPOSIO_DISCORD_AUTH_CONFIG_ID", "discord", "Discord", "discord"},
			{"COMPOSIO_OUTLOOK_AUTH_CONFIG_ID", "outlook", "Outlook", "outlook"},
			{"COMPOSIO_REDDIT_AUTH_CONFIG_ID", "reddit", "Reddit", "reddit"},
		}

		for _, p := range dynamicPlatforms {
			id := os.Getenv(p.envKey)
			if id == "" {
				continue
			}
			a, err := adapter.NewDynamicComposioAdapter(ctx, cc, p.platform, p.displayName, id, integrationID(p.appName), p.appName)
			if err != nil {
				log.Printf("[Registry] Warning: %s skipped: %v", p.displayName, err)
				continue
			}
			registry.Register(a)
			log.Printf("[Registry] %s registered (%d tools via Composio)", p.displayName, len(a.Tools()))
		}
	}

	// Twitter: disabled — API 503 service unavailable (rate limits / policy)
	// if os.Getenv("TWITTER_CLIENT_ID") != "" {
	// 	registry.Register(twitter.New())
	// 	log.Println("[Registry] Twitter registered (direct OAuth 2.0)")
	// }

	log.Printf("[Registry] %d adapters registered", len(registry.List()))

	// --- Auth Manager ---
	authMgr := auth.NewManager(store, registry, baseURL)

	// Direct OAuth configs (for non-Composio adapters like GitHub)
	if id := os.Getenv("GITHUB_CLIENT_ID"); id != "" {
		authMgr.SetConfig("github", id, os.Getenv("GITHUB_CLIENT_SECRET"), "repo,read:user,read:org")
		log.Println("[Auth] GitHub OAuth configured")
	}

	if id := os.Getenv("TWITTER_CLIENT_ID"); id != "" {
		authMgr.SetConfig("twitter", id, os.Getenv("TWITTER_CLIENT_SECRET"), "tweet.read,tweet.write,users.read,offline.access")
		log.Println("[Auth] Twitter OAuth configured")
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
