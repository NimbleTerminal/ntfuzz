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

// NewClient creates a new API client pointed at baseURL (e.g. "http://localhost:8080").
func NewClient(baseURL string) *Client {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = "http://localhost:8080"
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
	err := c.getJSON(ctx, "/api/status", nil, &out)
	return out, err
}

// GetProjects fetches all projects with latest status summaries from GET /api/projects.
func (c *Client) GetProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := c.getJSON(ctx, "/api/projects", nil, &out)
	return out, err
}

// GetTargets fetches targets (optionally filtered by project) from GET /api/targets.
func (c *Client) GetTargets(ctx context.Context, project string) ([]Target, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	var out []Target
	err := c.getJSON(ctx, "/api/targets", q, &out)
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
	err := c.getJSON(ctx, "/api/runs", q, &out)
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
	err := c.getJSON(ctx, "/api/crashes", q, &out)
	return out, err
}

// MarkCrashFixed marks a crash as fixed via POST /api/crashes/{id}/fix.
func (c *Client) MarkCrashFixed(ctx context.Context, crashID int64, issueURL string) error {
	path := fmt.Sprintf("/api/crashes/%d/fix", crashID)
	body, err := json.Marshal(FixCrashRequest{IssueURL: issueURL})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s returned HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// GetCoverageHistory fetches chronological coverage points from GET /api/coverage/history.
func (c *Client) GetCoverageHistory(ctx context.Context, target string) ([]CoveragePoint, error) {
	q := url.Values{}
	q.Set("target", target)
	var out []CoveragePoint
	err := c.getJSON(ctx, "/api/coverage/history", q, &out)
	return out, err
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("create request %s: %w", u, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s returned HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
