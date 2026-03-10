// Package apify provides a simple client for running Apify actors synchronously.
package apify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls the Apify Actor REST API.
type Client struct {
	token      string
	httpClient *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		token:      token,
		httpClient: &http.Client{Timeout: 90 * time.Second},
	}
}

// RunActor runs an actor synchronously and returns the dataset items.
// Uses /run-sync-get-dataset-items so the caller blocks until results are ready.
func (c *Client) RunActor(ctx context.Context, actorID string, input map[string]any) ([]map[string]any, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf(
		"https://api.apify.com/v2/acts/%s/run-sync-get-dataset-items?token=%s&timeout=60&memory=256",
		actorID, c.token,
	)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apify error %d: %s", resp.StatusCode, string(data))
	}

	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return items, nil
}
