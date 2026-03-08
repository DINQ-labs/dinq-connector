package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/composio"
	"github.com/DINQ-labs/dinq-connector/internal/db"
	"github.com/DINQ-labs/dinq-connector/internal/models"
)

// Manager handles OAuth flows and connected account lifecycle.
type Manager struct {
	store    *db.Store
	registry *adapter.Registry
	configs  map[string]*models.AuthConfig
	baseURL  string
}

func NewManager(store *db.Store, registry *adapter.Registry, baseURL string) *Manager {
	return &Manager{
		store:    store,
		registry: registry,
		configs:  make(map[string]*models.AuthConfig),
		baseURL:  baseURL,
	}
}

func (m *Manager) SetConfig(platform, clientID, clientSecret, scopes string) {
	m.configs[platform] = &models.AuthConfig{
		Platform:     platform,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
	}
}

func (m *Manager) InitiateOAuth(ctx context.Context, userID, platform, callbackURL string) (string, error) {
	a := m.registry.Get(platform)
	if a == nil {
		return "", fmt.Errorf("unknown platform: %s", platform)
	}

	// Composio-backed adapters delegate OAuth entirely to Composio.
	if a.AuthScheme() == adapter.AuthComposio {
		return m.initiateComposioAuth(ctx, a, userID, callbackURL)
	}

	if a.AuthScheme() != adapter.AuthOAuth2 {
		return "", fmt.Errorf("platform %s does not use OAuth2", platform)
	}
	cfg := m.configs[platform]
	if cfg == nil {
		return "", fmt.Errorf("no OAuth config for platform: %s", platform)
	}
	oauthCfg := a.OAuthConfig()
	if oauthCfg == nil {
		return "", fmt.Errorf("platform %s has no OAuth config", platform)
	}

	state, err := randomState()
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}

	pending := &models.PendingAuth{
		State:       state,
		UserID:      userID,
		Platform:    platform,
		CallbackURL: callbackURL,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}
	if err := m.store.SavePendingAuth(ctx, pending); err != nil {
		return "", fmt.Errorf("save pending auth: %w", err)
	}

	params := url.Values{
		"client_id":    {cfg.ClientID},
		"redirect_uri": {m.baseURL + "/auth/callback/" + platform},
		"scope":        {cfg.Scopes},
		"state":        {state},
	}
	return oauthCfg.AuthorizeURL + "?" + params.Encode(), nil
}

// initiateComposioAuth delegates the OAuth flow to Composio.
func (m *Manager) initiateComposioAuth(ctx context.Context, a adapter.PlatformAdapter, userID, callbackURL string) (string, error) {
	cap, ok := a.(adapter.ComposioAuthProvider)
	if !ok {
		return "", fmt.Errorf("platform %s is ComposioAuth but does not implement ComposioAuthProvider", a.Name())
	}

	state, err := randomState()
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}

	// Save pending auth so we can restore callbackURL after Composio redirects back.
	pending := &models.PendingAuth{
		State:       state,
		UserID:      userID,
		Platform:    a.Name(),
		CallbackURL: callbackURL,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}
	if err := m.store.SavePendingAuth(ctx, pending); err != nil {
		return "", fmt.Errorf("save pending auth: %w", err)
	}

	// Composio will redirect user here after OAuth completes.
	composioCallback := m.baseURL + "/auth/composio-callback?state=" + state

	resp, err := cap.ComposioClient().InitiateConnection(ctx, composio.InitiateConnectionRequest{
		IntegrationID: cap.IntegrationID(),
		RedirectURI:   composioCallback,
	})
	if err != nil {
		return "", fmt.Errorf("composio initiate: %w", err)
	}

	// Store the Composio connectedAccountId — this is our "token" for executing actions.
	now := time.Now()
	account := &models.ConnectedAccount{
		UserID:      userID,
		Platform:    a.Name(),
		Status:      models.StatusInitiated,
		AccessToken: resp.ConnectedAccountID,
		TokenType:   "composio",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := m.store.UpsertConnectedAccount(ctx, account); err != nil {
		return "", fmt.Errorf("save account: %w", err)
	}

	return resp.RedirectURL, nil
}

// HandleComposioCallback is called when Composio redirects back after OAuth.
// It verifies the connection is active and updates our DB.
func (m *Manager) HandleComposioCallback(ctx context.Context, state string) (*models.ConnectedAccount, string, error) {
	pending, err := m.store.GetPendingAuth(ctx, state)
	if err != nil {
		return nil, "", fmt.Errorf("invalid or expired state")
	}
	if time.Now().After(pending.ExpiresAt) {
		return nil, "", fmt.Errorf("auth flow expired")
	}

	a := m.registry.Get(pending.Platform)
	if a == nil {
		return nil, "", fmt.Errorf("unknown platform: %s", pending.Platform)
	}
	cap, ok := a.(adapter.ComposioAuthProvider)
	if !ok {
		return nil, "", fmt.Errorf("platform %s is not a Composio adapter", pending.Platform)
	}

	// Retrieve the account we saved during initiation.
	account, err := m.store.GetConnectedAccount(ctx, pending.UserID, pending.Platform)
	if err != nil {
		return nil, "", fmt.Errorf("no pending account for %s/%s", pending.UserID, pending.Platform)
	}

	// Verify with Composio that the connection is now active.
	conn, err := cap.ComposioClient().GetConnection(ctx, account.AccessToken)
	if err != nil {
		account.Status = models.StatusFailed
		account.StatusReason = "composio verify failed: " + err.Error()
		account.UpdatedAt = time.Now()
		_ = m.store.UpsertConnectedAccount(ctx, account)
		return nil, "", fmt.Errorf("composio verify: %w", err)
	}

	if conn.Status == "ACTIVE" {
		account.Status = models.StatusActive
		account.StatusReason = ""
		// Update to the final Composio account ID (ca_xxx format), which may differ
		// from the initial UUID returned by InitiateConnection.
		if conn.ID != "" && conn.ID != account.AccessToken {
			account.AccessToken = conn.ID
		}
	} else {
		account.Status = models.StatusFailed
		account.StatusReason = fmt.Sprintf("composio connection status: %s", conn.Status)
	}
	account.UpdatedAt = time.Now()
	_ = m.store.UpsertConnectedAccount(ctx, account)
	_ = m.store.DeletePendingAuth(ctx, state)

	return account, pending.CallbackURL, nil
}

func (m *Manager) HandleCallback(ctx context.Context, platform, code, state string) (*models.ConnectedAccount, string, error) {
	pending, err := m.store.GetPendingAuth(ctx, state)
	if err != nil {
		return nil, "", fmt.Errorf("invalid or expired state")
	}
	if time.Now().After(pending.ExpiresAt) {
		return nil, "", fmt.Errorf("auth flow expired")
	}
	if pending.Platform != platform {
		return nil, "", fmt.Errorf("platform mismatch")
	}

	a := m.registry.Get(platform)
	if a == nil {
		return nil, "", fmt.Errorf("unknown platform: %s", platform)
	}
	cfg := m.configs[platform]
	oauthCfg := a.OAuthConfig()

	tokenResp, err := exchangeCode(ctx, oauthCfg.TokenURL, cfg.ClientID, cfg.ClientSecret, code, m.baseURL+"/auth/callback/"+platform)
	if err != nil {
		return nil, "", fmt.Errorf("token exchange: %w", err)
	}

	now := time.Now()
	var expiresAt *time.Time
	if tokenResp.ExpiresIn > 0 {
		t := now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	account := &models.ConnectedAccount{
		UserID:       pending.UserID,
		Platform:     platform,
		Status:       models.StatusActive,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		Scopes:       cfg.Scopes,
		ExpiresAt:    expiresAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := m.store.UpsertConnectedAccount(ctx, account); err != nil {
		return nil, "", fmt.Errorf("save account: %w", err)
	}
	_ = m.store.DeletePendingAuth(ctx, state)

	return account, pending.CallbackURL, nil
}

func (m *Manager) GetActiveToken(ctx context.Context, userID, platform string) (string, error) {
	account, err := m.store.GetConnectedAccount(ctx, userID, platform)
	if err != nil {
		return "", fmt.Errorf("no connected account for user %s on %s", userID, platform)
	}
	if account.Status != models.StatusActive {
		return "", fmt.Errorf("account %s/%s is %s", userID, platform, account.Status)
	}

	// Composio adapters: token is the Composio connectedAccountId, no local refresh needed.
	a := m.registry.Get(platform)
	if a != nil && a.AuthScheme() == adapter.AuthComposio {
		return account.AccessToken, nil
	}

	if !account.NeedsRefresh() {
		return account.AccessToken, nil
	}

	cfg := m.configs[platform]
	oauthCfg := a.OAuthConfig()
	if oauthCfg == nil || cfg == nil {
		return "", fmt.Errorf("cannot refresh: no OAuth config for %s", platform)
	}

	tokenResp, err := refreshToken(ctx, oauthCfg.TokenURL, cfg.ClientID, cfg.ClientSecret, account.RefreshToken)
	if err != nil {
		account.Status = models.StatusExpired
		account.StatusReason = "refresh failed: " + err.Error()
		account.UpdatedAt = time.Now()
		_ = m.store.UpsertConnectedAccount(ctx, account)
		return "", fmt.Errorf("token refresh failed: %w", err)
	}

	account.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		account.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		account.ExpiresAt = &t
	}
	account.UpdatedAt = time.Now()
	_ = m.store.UpsertConnectedAccount(ctx, account)

	return account.AccessToken, nil
}

func (m *Manager) GetAccountStatus(ctx context.Context, userID, platform string) (*models.ConnectedAccount, error) {
	return m.store.GetConnectedAccount(ctx, userID, platform)
}

func (m *Manager) ListAccounts(ctx context.Context, userID string) ([]*models.ConnectedAccount, error) {
	return m.store.ListConnectedAccounts(ctx, userID)
}

// --- OAuth helpers ---

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	return postToken(ctx, tokenURL, data)
}

func refreshToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshTok string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshTok},
	}
	return postToken(ctx, tokenURL, data)
}

func postToken(ctx context.Context, tokenURL string, data url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}
	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	return &result, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
