// Package httpapi provides HTTP endpoints for OAuth management.
// These endpoints handle browser redirects (OAuth authorize/callback)
// and account management — things that can't go through MCP.
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/auth"
)

// 业务错误码（与 dinq-server 保持一致）
const (
	codeSuccess        = 0
	codeInvalidRequest = 4100
	codeMissingParam   = 4101
	codeInvalidParam   = 4102
	codeNotConnected   = 4200
	codeInternalError  = 5001
)

// Handler provides HTTP routes for auth management.
type Handler struct {
	authMgr  *auth.Manager
	registry *adapter.Registry
	mux      *http.ServeMux
}

// New creates a new HTTP handler.
func New(authMgr *auth.Manager, registry *adapter.Registry) *Handler {
	h := &Handler{
		authMgr:  authMgr,
		registry: registry,
		mux:      http.NewServeMux(),
	}
	h.mux.HandleFunc("GET /health", h.handleHealth)
	h.mux.HandleFunc("GET /auth/platforms", h.handleListPlatforms)
	h.mux.HandleFunc("POST /auth/connect", h.handleConnect)
	h.mux.HandleFunc("GET /auth/callback/{platform}", h.handleCallback)
	h.mux.HandleFunc("GET /auth/composio-callback", h.handleComposioCallback)
	h.mux.HandleFunc("GET /auth/accounts", h.handleListAccounts)
	h.mux.HandleFunc("POST /auth/connect-credentials", h.handleConnectCredentials)
	h.mux.HandleFunc("POST /api/execute", h.handleExecute)
	return h
}

// Handler returns the http.Handler for mounting in a server.
func (h *Handler) Handler() http.Handler {
	return h.mux
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondOK(w, map[string]string{
		"status":  "ok",
		"service": "dinq-connector",
		"version": "0.1.0",
	})
}

// GET /auth/platforms — list available platforms and their auth status for a user.
func (h *Handler) handleListPlatforms(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")

	type platformInfo struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		AuthScheme  string `json:"auth_scheme"`
		Connected   bool   `json:"connected"`
		Status      string `json:"status,omitempty"`
	}

	var platforms []platformInfo
	for _, a := range h.registry.List() {
		info := platformInfo{
			Name:        a.Name(),
			DisplayName: a.DisplayName(),
			AuthScheme:  string(a.AuthScheme()),
		}
		if userID != "" {
			account, err := h.authMgr.GetAccountStatus(r.Context(), userID, a.Name())
			if err == nil {
				info.Connected = account.IsActive()
				info.Status = account.Status
			}
		}
		platforms = append(platforms, info)
	}

	respondOK(w, map[string]any{"platforms": platforms})
}

// POST /auth/connect — initiate OAuth flow.
// Body: { "user_id": "xxx", "platform": "github", "callback_url": "https://..." }
func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID      string `json:"user_id"`
		Platform    string `json:"platform"`
		CallbackURL string `json:"callback_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, codeInvalidRequest, "invalid JSON")
		return
	}
	if body.UserID == "" || body.Platform == "" {
		respondError(w, codeMissingParam, "user_id and platform are required")
		return
	}

	redirectURL, err := h.authMgr.InitiateOAuth(r.Context(), body.UserID, body.Platform, body.CallbackURL)
	if err != nil {
		respondError(w, codeInvalidParam, err.Error())
		return
	}

	respondOK(w, map[string]string{
		"redirect_url": redirectURL,
		"status":       "initiated",
	})
}

// POST /auth/connect-credentials — connect a credentials-based platform (e.g. SMTP email).
// Body: { "user_id": "xxx", "platform": "smtp_email", "credentials": { "email": "...", "password": "...", "smtp_host": "...", "smtp_port": 587 } }
func (h *Handler) handleConnectCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID      string         `json:"user_id"`
		Platform    string         `json:"platform"`
		Credentials map[string]any `json:"credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, codeInvalidRequest, "invalid JSON")
		return
	}
	if body.UserID == "" || body.Platform == "" {
		respondError(w, codeMissingParam, "user_id and platform are required")
		return
	}
	if body.Credentials == nil {
		respondError(w, codeMissingParam, "credentials are required")
		return
	}

	a := h.registry.Get(body.Platform)
	if a == nil {
		respondError(w, codeInvalidParam, "unknown platform: "+body.Platform)
		return
	}

	credJSON, _ := json.Marshal(body.Credentials)

	// Extract email for account_email field
	email, _ := body.Credentials["email"].(string)

	account, err := h.authMgr.SaveCredentials(r.Context(), body.UserID, body.Platform, string(credJSON), email)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}

	respondOK(w, map[string]any{
		"status":        account.Status,
		"platform":      account.Platform,
		"account_email": account.AccountEmail,
	})
}

// GET /auth/callback/{platform} — OAuth callback handler.
// GitHub/Google/Slack redirect here after user authorization.
func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		errMsg := r.URL.Query().Get("error")
		if errMsg == "" {
			errMsg = "missing code or state"
		}
		http.Error(w, "Authorization failed: "+errMsg, http.StatusBadRequest)
		return
	}

	account, callbackURL, err := h.authMgr.HandleCallback(r.Context(), platform, code, state)
	if err != nil {
		log.Printf("[Auth] Callback error for %s: %v", platform, err)
		http.Error(w, "Authorization failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Auth] %s connected for user %s (status: %s)", platform, account.UserID, account.Status)

	// Redirect to callback URL if provided, otherwise show success page
	if callbackURL != "" {
		http.Redirect(w, r, callbackURL+"?status=connected&platform="+platform, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><body style="font-family:sans-serif;text-align:center;padding:60px">
<h2>Connected!</h2>
<p>Your ` + h.registry.Get(platform).DisplayName() + ` account has been connected.</p>
<p>You can close this window and return to your conversation.</p>
</body></html>`))
}

// GET /auth/composio-callback?state=xxx — Composio OAuth callback handler.
// After user completes OAuth on the platform (via Composio), Composio redirects here.
func (h *Handler) handleComposioCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Error(w, "missing state parameter", http.StatusBadRequest)
		return
	}

	account, callbackURL, err := h.authMgr.HandleComposioCallback(r.Context(), state)
	if err != nil {
		log.Printf("[Auth] Composio callback error: %v", err)
		http.Error(w, "Authorization failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Auth] %s connected via Composio for user %s (status: %s)", account.Platform, account.UserID, account.Status)

	if callbackURL != "" {
		http.Redirect(w, r, callbackURL+"?status=connected&platform="+account.Platform, http.StatusFound)
		return
	}

	displayName := account.Platform
	if a := h.registry.Get(account.Platform); a != nil {
		displayName = a.DisplayName()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><body style="font-family:sans-serif;text-align:center;padding:60px">
<h2>Connected!</h2>
<p>Your ` + displayName + ` account has been connected.</p>
<p>You can close this window and return to your conversation.</p>
</body></html>`))
}

// GET /auth/accounts?user_id=xxx — list connected accounts for a user.
func (h *Handler) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		respondError(w, codeMissingParam, "user_id is required")
		return
	}

	accounts, err := h.authMgr.ListAccounts(r.Context(), userID)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}

	respondOK(w, map[string]any{"accounts": accounts})
}

// POST /api/execute — execute a platform tool on behalf of a user.
// Body: { "user_id": "xxx", "platform": "gmail", "action": "send_email", "params": { ... } }
// Internal API for service-to-service calls (e.g. dinq-server sending emails via user's Gmail).
func (h *Handler) handleExecute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   string         `json:"user_id"`
		Platform string         `json:"platform"`
		Action   string         `json:"action"`
		Params   map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, codeInvalidRequest, "invalid JSON")
		return
	}
	if body.UserID == "" || body.Platform == "" || body.Action == "" {
		respondError(w, codeMissingParam, "user_id, platform, and action are required")
		return
	}

	a := h.registry.Get(body.Platform)
	if a == nil {
		respondError(w, codeInvalidParam, "unknown platform: "+body.Platform)
		return
	}

	// Get user's access token
	token, err := h.authMgr.GetActiveToken(r.Context(), body.UserID, body.Platform)
	if err != nil {
		respondError(w, codeNotConnected, "user not connected: "+err.Error())
		return
	}

	result, err := a.Execute(r.Context(), body.Action, body.Params, token, body.UserID)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}

	// Extract text content from MCP result
	var content string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			content = tc.Text
			break
		}
	}
	if content == "" {
		data, _ := json.Marshal(result.Content)
		content = string(data)
	}

	if result.IsError {
		respondError(w, codeInternalError, content)
		return
	}

	// Try to parse content as JSON for clean output
	var jsonResult json.RawMessage
	if err := json.Unmarshal([]byte(content), &jsonResult); err == nil {
		respondOK(w, jsonResult)
	} else {
		respondOK(w, content)
	}
}

// --- Response helpers (unified {code, data, message} format) ---

func respondOK(w http.ResponseWriter, data any) {
	writeJSON(w, map[string]any{
		"code":    codeSuccess,
		"data":    data,
		"message": "success",
	})
}

func respondError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, map[string]any{
		"code":    code,
		"data":    nil,
		"message": message,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}
