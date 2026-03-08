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
	mcpPort := envIntOrDefault("MCP_PORT", 0) // 0 = same port

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

		// Twitter: static adapter (free tier — limited tools)
		if id := os.Getenv("COMPOSIO_TWITTER_AUTH_CONFIG_ID"); id != "" {
			registry.Register(twitter.New(cc, id, integrationID("twitter")))
			log.Println("[Registry] Twitter registered (static, free tier)")
		}

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

	log.Printf("[Registry] %d adapters registered", len(registry.List()))

	// --- Auth Manager ---
	authMgr := auth.NewManager(store, registry, baseURL)

	// Direct OAuth configs (for non-Composio adapters like GitHub)
	if id := os.Getenv("GITHUB_CLIENT_ID"); id != "" {
		authMgr.SetConfig("github", id, os.Getenv("GITHUB_CLIENT_SECRET"), "repo,read:user,read:org")
		log.Println("[Auth] GitHub OAuth configured")
	}

	// --- HTTP API ---
	httpHandler := httpapi.New(authMgr, registry)

	// --- MCP Server ---
	if mcpPort == 0 || mcpPort == port {
		mcpSrv := mcpserver.New(registry, authMgr, mcpserver.Config{
			Port:     port,
			Endpoint: "/mcp",
		})

		mux := http.NewServeMux()
		mux.Handle("/", httpHandler.Handler())

		log.Printf("[Server] Starting on :%d (HTTP API + MCP at /mcp)", port)

		go func() {
			if err := mcpSrv.Start(); err != nil {
				log.Fatalf("MCP server error: %v", err)
			}
		}()

		httpAddr := ":" + strconv.Itoa(port+1)
		log.Printf("[HTTP] API server on %s", httpAddr)
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	} else {
		mcpSrv := mcpserver.New(registry, authMgr, mcpserver.Config{
			Port:     mcpPort,
			Endpoint: "/mcp",
		})

		go func() {
			log.Printf("[MCP] Starting on :%d/mcp", mcpPort)
			if err := mcpSrv.Start(); err != nil {
				log.Fatalf("MCP server error: %v", err)
			}
		}()

		httpAddr := ":" + strconv.Itoa(port)
		log.Printf("[HTTP] Starting on %s", httpAddr)
		if err := http.ListenAndServe(httpAddr, httpHandler.Handler()); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
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
