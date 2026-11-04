package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is an HTTP client for consuming the ntfuzz JSON API from the Bubble Tea TUI and CLI.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new API client pointed at baseURL (defaults to "http://127.0.0.1:8080").
func NewClient(baseURL string) *Client {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = "http://127.0.0.1:8080"
	}
	return &Client{
		baseURL: trimmed,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// BaseURL returns the configured API base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetStatus fetches overall fleet health and active jobs from GET /api/status.
func (c *Client) GetStatus(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	err := c.doJSON(ctx, http.MethodGet, "/api/status", nil, nil, &out)
	return out, err
}

// GetProjects fetches all projects with latest status summaries from GET /api/projects.
func (c *Client) GetProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := c.doJSON(ctx, http.MethodGet, "/api/projects", nil, nil, &out)
	return out, err
}

// GetTargets fetches targets (optionally filtered by project) from GET /api/targets.
func (c *Client) GetTargets(ctx context.Context, project string) ([]Target, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	var out []Target
	err := c.doJSON(ctx, http.MethodGet, "/api/targets", q, nil, &out)
	return out, err
}

// GetRuns fetches recent runs for a target from GET /api/runs.
func (c *Client) GetRuns(ctx context.Context, target string, limit int) ([]Run, error) {
	q := url.Values{}
	if target != "" {
		q.Set("target", target)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []Run
	err := c.doJSON(ctx, http.MethodGet, "/api/runs", q, nil, &out)
	return out, err
}

// GetCrashes fetches crashes from GET /api/crashes.
func (c *Client) GetCrashes(ctx context.Context, openOnly bool, target string) ([]Crash, error) {
	q := url.Values{}
	q.Set("open", strconv.FormatBool(openOnly))
	if target != "" {
		q.Set("target", target)
	}
	var out []Crash
	err := c.doJSON(ctx, http.MethodGet, "/api/crashes", q, nil, &out)
	return out, err
}

// MarkCrashFixed marks a crash as fixed via POST /api/crashes/{id}/fix.
func (c *Client) MarkCrashFixed(ctx context.Context, crashID int64, issueURL string) error {
	path := fmt.Sprintf("/api/crashes/%d/fix", crashID)
	return c.doJSON(ctx, http.MethodPost, path, nil, FixCrashRequest{IssueURL: issueURL}, nil)
}

// GetCoverageHistory fetches chronological coverage points from GET /api/coverage/history.
func (c *Client) GetCoverageHistory(ctx context.Context, target string) ([]CoveragePoint, error) {
	q := url.Values{}
	q.Set("target", target)
	var out []CoveragePoint
	err := c.doJSON(ctx, http.MethodGet, "/api/coverage/history", q, nil, &out)
	return out, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, reqBody, dst any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encode %s %s body: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("create request %s %s: %w", method, u, err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, u, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s returned HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}
