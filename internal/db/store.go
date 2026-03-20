// Package db provides PostgreSQL storage for connected accounts and pending auth flows.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"

	"github.com/DINQ-labs/dinq-connector/internal/models"
)

// Store wraps a PostgreSQL connection pool.
type Store struct {
	db *sql.DB
}

// New creates a new store and runs migrations.
func New(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS connected_accounts (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id       TEXT NOT NULL,
			platform      TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'initiated',
			status_reason TEXT NOT NULL DEFAULT '',
			access_token  TEXT NOT NULL DEFAULT '',
			refresh_token TEXT NOT NULL DEFAULT '',
			token_type    TEXT NOT NULL DEFAULT 'bearer',
			scopes        TEXT NOT NULL DEFAULT '',
			expires_at    TIMESTAMPTZ,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(user_id, platform)
		)`,
		`CREATE TABLE IF NOT EXISTS pending_auths (
			state       TEXT PRIMARY KEY,
			user_id     TEXT NOT NULL,
			platform    TEXT NOT NULL,
			callback_url TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at  TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_connected_accounts_user ON connected_accounts(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_connected_accounts_platform ON connected_accounts(user_id, platform)`,
		`ALTER TABLE pending_auths ADD COLUMN IF NOT EXISTS code_verifier TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE connected_accounts ADD COLUMN IF NOT EXISTS account_email TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec %q: %w", q[:60], err)
		}
	}
	return nil
}

// --- Connected Accounts ---

// UpsertConnectedAccount inserts or updates a connected account (unique on user_id+platform).
func (s *Store) UpsertConnectedAccount(ctx context.Context, a *models.ConnectedAccount) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connected_accounts (user_id, platform, status, status_reason, account_email, access_token, refresh_token, token_type, scopes, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (user_id, platform) DO UPDATE SET
			status = EXCLUDED.status,
			status_reason = EXCLUDED.status_reason,
			account_email = EXCLUDED.account_email,
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_type = EXCLUDED.token_type,
			scopes = EXCLUDED.scopes,
			expires_at = EXCLUDED.expires_at,
			updated_at = EXCLUDED.updated_at
	`, a.UserID, a.Platform, a.Status, a.StatusReason, a.AccountEmail, a.AccessToken, a.RefreshToken, a.TokenType, a.Scopes, a.ExpiresAt, a.CreatedAt, a.UpdatedAt)
	return err
}

// GetConnectedAccount returns the connected account for a user+platform.
func (s *Store) GetConnectedAccount(ctx context.Context, userID, platform string) (*models.ConnectedAccount, error) {
	a := &models.ConnectedAccount{}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, platform, status, status_reason, account_email, access_token, refresh_token, token_type, scopes, expires_at, created_at, updated_at
		FROM connected_accounts WHERE user_id = $1 AND platform = $2
	`, userID, platform).Scan(
		&a.ID, &a.UserID, &a.Platform, &a.Status, &a.StatusReason,
		&a.AccountEmail, &a.AccessToken, &a.RefreshToken, &a.TokenType, &a.Scopes,
		&a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not found")
	}
	return a, err
}

// ListConnectedAccounts returns all connected accounts for a user.
func (s *Store) ListConnectedAccounts(ctx context.Context, userID string) ([]*models.ConnectedAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, platform, status, status_reason, account_email, token_type, scopes, expires_at, created_at, updated_at
		FROM connected_accounts WHERE user_id = $1 ORDER BY platform
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*models.ConnectedAccount
	for rows.Next() {
		a := &models.ConnectedAccount{}
		if err := rows.Scan(&a.ID, &a.UserID, &a.Platform, &a.Status, &a.StatusReason, &a.AccountEmail, &a.TokenType, &a.Scopes, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// --- Pending Auths ---

// SavePendingAuth saves a pending OAuth flow.
func (s *Store) SavePendingAuth(ctx context.Context, p *models.PendingAuth) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pending_auths (state, user_id, platform, callback_url, code_verifier, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (state) DO UPDATE SET
			user_id = EXCLUDED.user_id, platform = EXCLUDED.platform,
			callback_url = EXCLUDED.callback_url, code_verifier = EXCLUDED.code_verifier,
			expires_at = EXCLUDED.expires_at
	`, p.State, p.UserID, p.Platform, p.CallbackURL, p.CodeVerifier, p.CreatedAt, p.ExpiresAt)
	return err
}

// GetPendingAuth retrieves a pending auth by state.
func (s *Store) GetPendingAuth(ctx context.Context, state string) (*models.PendingAuth, error) {
	p := &models.PendingAuth{}
	err := s.db.QueryRowContext(ctx, `
		SELECT state, user_id, platform, callback_url, code_verifier, created_at, expires_at
		FROM pending_auths WHERE state = $1
	`, state).Scan(&p.State, &p.UserID, &p.Platform, &p.CallbackURL, &p.CodeVerifier, &p.CreatedAt, &p.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not found")
	}
	return p, err
}

// DeletePendingAuth removes a pending auth (after completion or expiry).
func (s *Store) DeletePendingAuth(ctx context.Context, state string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_auths WHERE state = $1`, state)
	return err
}

// CleanExpiredPendingAuths removes expired pending auth flows.
func (s *Store) CleanExpiredPendingAuths(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_auths WHERE expires_at < NOW()`)
	return err
}
