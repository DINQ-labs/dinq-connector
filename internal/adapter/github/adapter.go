// Package github implements the PlatformAdapter for GitHub.
// Tools: list repos, list issues, create issue, get user profile.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const apiBase = "https://api.github.com"

// Adapter implements adapter.PlatformAdapter for GitHub.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string                   { return "github" }
func (a *Adapter) DisplayName() string            { return "GitHub" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthOAuth2 }

func (a *Adapter) OAuthConfig() *adapter.OAuthConfig {
	return &adapter.OAuthConfig{
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		Scopes:       []string{"repo", "read:user", "read:org"},
	}
}

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("github_get_user",
			mcp.WithDescription("Get the authenticated GitHub user's profile information"),
		),
		mcp.NewTool("github_list_repos",
			mcp.WithDescription("List repositories for the authenticated user or a specified user/org"),
			mcp.WithString("owner", mcp.Description("Username or org (omit for authenticated user)")),
			mcp.WithString("type", mcp.Description("Filter: all, owner, public, private, member"), mcp.Enum("all", "owner", "public", "private", "member")),
			mcp.WithNumber("per_page", mcp.Description("Results per page (max 100)")),
		),
		mcp.NewTool("github_list_issues",
			mcp.WithDescription("List issues for a repository"),
			mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
			mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
			mcp.WithString("state", mcp.Description("Filter: open, closed, all"), mcp.Enum("open", "closed", "all")),
			mcp.WithNumber("per_page", mcp.Description("Results per page (max 100)")),
		),
		mcp.NewTool("github_create_issue",
			mcp.WithDescription("Create a new issue in a repository"),
			mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
			mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
			mcp.WithString("title", mcp.Required(), mcp.Description("Issue title")),
			mcp.WithString("body", mcp.Description("Issue body (markdown)")),
			mcp.WithString("labels", mcp.Description("Comma-separated labels")),
		),
		mcp.NewTool("github_list_notifications",
			mcp.WithDescription("List unread notifications for the authenticated user"),
			mcp.WithBoolean("all", mcp.Description("Include read notifications")),
		),
		mcp.NewTool("github_list_starred",
			mcp.WithDescription("List repositories starred by the authenticated user"),
			mcp.WithNumber("per_page", mcp.Description("Results per page")),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, token, _ string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "get_user":
		return a.getUser(ctx, token)
	case "list_repos":
		return a.listRepos(ctx, args, token)
	case "list_issues":
		return a.listIssues(ctx, args, token)
	case "create_issue":
		return a.createIssue(ctx, args, token)
	case "list_notifications":
		return a.listNotifications(ctx, args, token)
	case "list_starred":
		return a.listStarred(ctx, args, token)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

// --- Tool implementations ---

func (a *Adapter) getUser(ctx context.Context, token string) (*mcp.CallToolResult, error) {
	data, err := githubGet(ctx, "/user", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listRepos(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	owner, _ := args["owner"].(string)
	repoType, _ := args["type"].(string)
	perPage := intArg(args, "per_page", 30)

	var path string
	if owner == "" {
		path = fmt.Sprintf("/user/repos?type=%s&per_page=%d&sort=updated", defaultStr(repoType, "all"), perPage)
	} else {
		path = fmt.Sprintf("/users/%s/repos?type=%s&per_page=%d&sort=updated", owner, defaultStr(repoType, "public"), perPage)
	}

	data, err := githubGet(ctx, path, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listIssues(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	state := defaultStr(argStr(args, "state"), "open")
	perPage := intArg(args, "per_page", 30)

	path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=%d", owner, repo, state, perPage)
	data, err := githubGet(ctx, path, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) createIssue(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)

	payload := map[string]any{"title": title}
	if body != "" {
		payload["body"] = body
	}
	if labels, ok := args["labels"].(string); ok && labels != "" {
		payload["labels"] = splitLabels(labels)
	}

	data, err := githubPost(ctx, fmt.Sprintf("/repos/%s/%s/issues", owner, repo), payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listNotifications(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	all := "false"
	if v, ok := args["all"].(bool); ok && v {
		all = "true"
	}
	data, err := githubGet(ctx, "/notifications?all="+all, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listStarred(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	perPage := intArg(args, "per_page", 30)
	data, err := githubGet(ctx, fmt.Sprintf("/user/starred?per_page=%d", perPage), token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// --- HTTP helpers ---

func githubGet(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func githubPost(ctx context.Context, path string, payload any, token string) ([]byte, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+path, io.NopCloser(
		jsonReader(data),
	))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// --- Util ---

func argStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return def
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func splitLabels(s string) []string {
	var labels []string
	for _, l := range splitComma(s) {
		if l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

func splitComma(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			result = append(result, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	result = append(result, trimSpace(s[start:]))
	return result
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && s[i] == ' ' {
		i++
	}
	for j > i && s[j-1] == ' ' {
		j--
	}
	return s[i:j]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
func jsonReader(data []byte) io.Reader { return &bytesReader{data: data} }
