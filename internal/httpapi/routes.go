// Package httpapi provides HTTP endpoints for OAuth management.
// These endpoints handle browser redirects (OAuth authorize/callback)
// and account management — things that can't go through MCP.
package httpapi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"

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
	codeAccountExpired = 4201
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
	h.mux.HandleFunc("GET /auth/credentials/{platform}", h.handleCredentialsPage)
	h.mux.HandleFunc("POST /auth/credentials/{platform}", h.handleCredentialsPage)
	h.mux.HandleFunc("GET /auth/callback/{platform}", h.handleCallback)
	h.mux.HandleFunc("GET /auth/composio-callback", h.handleComposioCallback)
	h.mux.HandleFunc("GET /auth/accounts", h.handleListAccounts)
	h.mux.HandleFunc("DELETE /auth/accounts/{id}", h.handleDeleteAccount)
	h.mux.HandleFunc("POST /auth/connect-credentials", h.handleConnectCredentials)
	h.mux.HandleFunc("POST /api/execute", h.handleExecute)
	return h
}

type credentialsPageData struct {
	State   string
	Email   string
	Error   string
	Success bool
}

var credentialsPageTemplate = template.Must(template.New("credentials").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Connect email</title>
  <style>
    *{box-sizing:border-box}body{margin:0;background:#f5f6f8;color:#16181d;font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
    main{width:min(520px,calc(100% - 32px));margin:64px auto;background:#fff;border:1px solid #e1e4e8;padding:32px}
    h1{font-size:24px;margin:0 0 8px}p{color:#626873;line-height:1.5;margin:0 0 24px}label{display:block;font-size:14px;font-weight:600;margin:16px 0 7px}
    input,select{width:100%;height:44px;border:1px solid #c9ced6;padding:0 12px;font:inherit;background:#fff}input:focus,select:focus{outline:2px solid #3b82f6;outline-offset:1px}
    .error{padding:12px;background:#fff1f0;color:#b42318;margin-bottom:16px}.hint{font-size:13px;color:#737983;margin-top:8px}
    button{width:100%;height:46px;margin-top:24px;border:0;background:#111827;color:#fff;font:600 15px inherit;cursor:pointer}button:hover{background:#263244}
    @media(max-width:520px){main{margin:20px auto;padding:24px}}
  </style>
</head>
<body><main>
{{if .Success}}
  <h1>Email connected</h1><p>Your SMTP mailbox is ready. You can close this page.</p>
{{else}}
  <h1>Connect your email</h1>
  <p>Enter your mailbox and app password. The outgoing mail settings are detected automatically.</p>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  <form method="post">
    <input type="hidden" name="state" value="{{.State}}">
    <label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="username" required value="{{.Email}}">
    <label for="password">App password or authorization code</label><input id="password" name="password" type="password" autocomplete="current-password" required>
    <div class="hint">This is usually different from your normal sign-in password.</div>
    <button type="submit">Connect email</button>
  </form>
{{end}}
</main></body></html>`))

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

// getUserID reads user_id from query param, falling back to X-User-ID header (injected by gateway).
func getUserID(r *http.Request) string {
	if uid := r.URL.Query().Get("user_id"); uid != "" {
		return uid
	}
	return r.Header.Get("X-User-ID")
}

// GET /auth/platforms — list available platforms and their auth status for a user.
func (h *Handler) handleListPlatforms(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

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
			Name:        publicPlatformName(a.Name()),
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
	if body.UserID == "" {
		body.UserID = r.Header.Get("X-User-ID")
	}
	if body.UserID == "" || body.Platform == "" {
		respondError(w, codeMissingParam, "user_id and platform are required")
		return
	}
	originalPlatform := body.Platform
	body.Platform = adapter.ResolveName(body.Platform)

	redirectURL, err := h.authMgr.InitiateOAuth(r.Context(), body.UserID, body.Platform, body.CallbackURL, originalPlatform)
	if err != nil {
		respondError(w, codeInvalidParam, err.Error())
		return
	}

	respondOK(w, map[string]string{
		"redirect_url": redirectURL,
		"status":       "initiated",
	})
}

func (h *Handler) handleCredentialsPage(w http.ResponseWriter, r *http.Request) {
	platform := adapter.ResolveName(r.PathValue("platform"))
	state := r.FormValue("state")
	if state == "" {
		http.Error(w, "missing connection state", http.StatusBadRequest)
		return
	}
	if _, err := h.authMgr.GetPendingCredentials(r.Context(), platform, state); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data := credentialsPageData{State: state}
	if r.Method == http.MethodPost {
		data.Email = strings.TrimSpace(r.FormValue("email"))
		credentials := map[string]any{
			"email":    data.Email,
			"password": r.FormValue("password"),
		}
		account, callbackURL, err := h.authMgr.CompleteCredentials(r.Context(), platform, state, credentials)
		if err != nil {
			data.Error = err.Error()
			renderCredentialsPage(w, http.StatusBadRequest, data)
			return
		}
		if callbackURL != "" {
			redirectURL, err := connectedRedirectURL(callbackURL, account.Platform)
			if err == nil {
				http.Redirect(w, r, redirectURL, http.StatusFound)
				return
			}
		}
		data.Success = true
	}
	renderCredentialsPage(w, http.StatusOK, data)
}

func renderCredentialsPage(w http.ResponseWriter, status int, data credentialsPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The credentials page is served through the gateway under /connector.
	// Omitting form-action avoids browsers treating the proxied public origin as
	// different from the connector origin. The static form still submits to its
	// current URL, while base-uri and the script ban prevent target rewriting.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := credentialsPageTemplate.Execute(w, data); err != nil {
		log.Printf("[Auth] render credentials page: %v", err)
	}
}

func connectedRedirectURL(callbackURL, platform string) (string, error) {
	u, err := url.Parse(callbackURL)
	if err != nil || u.Scheme == "" {
		return "", fmt.Errorf("invalid callback URL")
	}
	query := u.Query()
	query.Set("status", "connected")
	query.Set("platform", publicPlatformName(platform))
	u.RawQuery = query.Encode()
	return u.String(), nil
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
	if body.UserID == "" {
		body.UserID = r.Header.Get("X-User-ID")
	}
	body.Platform = adapter.ResolveName(body.Platform)
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
	validator, ok := a.(adapter.CredentialsAuthProvider)
	if !ok {
		respondError(w, codeInvalidParam, "platform does not support credential connection: "+body.Platform)
		return
	}
	normalized, email, err := validator.ValidateCredentials(r.Context(), body.Credentials)
	if err != nil {
		respondError(w, codeInvalidParam, err.Error())
		return
	}

	credJSON, err := json.Marshal(normalized)
	if err != nil {
		respondError(w, codeInternalError, "failed to encode credentials")
		return
	}

	account, err := h.authMgr.SaveCredentials(r.Context(), body.UserID, body.Platform, string(credJSON), email)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}

	respondOK(w, map[string]any{
		"status":        account.Status,
		"platform":      publicPlatformName(account.Platform),
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
	userID := getUserID(r)
	if userID == "" {
		respondError(w, codeMissingParam, "user_id is required")
		return
	}

	accounts, err := h.authMgr.ListAccounts(r.Context(), userID)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}
	for _, account := range accounts {
		account.Platform = publicPlatformName(account.Platform)
	}

	respondOK(w, map[string]any{"accounts": accounts})
}

// publicPlatformName preserves the platform contract used by the existing
// integration UI while storage and execution use the more accurate name.
func publicPlatformName(platform string) string {
	if platform == "smtp_email" {
		return "imap"
	}
	return platform
}

// DELETE /auth/accounts/{id} — delete a connected account.
func (h *Handler) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	if userID == "" {
		respondError(w, codeMissingParam, "user_id is required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		respondError(w, codeMissingParam, "id is required")
		return
	}

	ok, err := h.authMgr.DeleteAccount(r.Context(), userID, id)
	if err != nil {
		respondError(w, codeInternalError, err.Error())
		return
	}
	if !ok {
		respondError(w, codeNotConnected, "account not found")
		return
	}
	respondOK(w, map[string]any{"ok": true})
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
	body.Platform = adapter.ResolveName(body.Platform)
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
		errMsg := err.Error()
		if strings.Contains(errMsg, "expired") {
			respondError(w, codeAccountExpired, "account expired, please reconnect: "+errMsg)
		} else {
			respondError(w, codeNotConnected, "user not connected: "+errMsg)
		}
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
