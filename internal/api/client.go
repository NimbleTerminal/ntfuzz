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

// GetCrash fetches a single crash record from GET /api/crashes/{id}.
func (c *Client) GetCrash(ctx context.Context, crashID int64) (Crash, error) {
	path := fmt.Sprintf("/api/crashes/%d", crashID)
	var out Crash
	err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &out)
	return out, err
}

// GetCrashInput downloads the minimized corpus input bytes from GET /api/crashes/{id}/input.
func (c *Client) GetCrashInput(ctx context.Context, crashID int64) ([]byte, error) {
	path := fmt.Sprintf("/api/crashes/%d/input", crashID)
	return c.doBytes(ctx, http.MethodGet, path, nil)
}

// GetCrashReport fetches the formatted Markdown crash report from GET /api/crashes/{id}/report.md,
// using publicURL (or the client's BaseURL when publicURL is empty) for permalink and input URLs.
func (c *Client) GetCrashReport(ctx context.Context, crashID int64, publicURL string) (string, error) {
	path := fmt.Sprintf("/api/crashes/%d/report.md", crashID)
	q := url.Values{}
	base := strings.TrimSpace(publicURL)
	if base == "" {
		base = c.baseURL
	}
	if base != "" {
		q.Set("public_url", base)
	}
	raw, err := c.doBytes(ctx, http.MethodGet, path, q)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// MarkCrashFixed marks a crash as fixed via POST /api/crashes/{id}/fix.
func (c *Client) MarkCrashFixed(ctx context.Context, crashID int64, issueURL string) error {
	path := fmt.Sprintf("/api/crashes/%d/fix", crashID)
	return c.doJSON(ctx, http.MethodPost, path, nil, FixCrashRequest{IssueURL: issueURL}, nil)
}

// SetCrashIssue sets issue_url on an open crash without fixing it via POST /api/crashes/{id}/issue.
func (c *Client) SetCrashIssue(ctx context.Context, crashID int64, issueURL string) error {
	path := fmt.Sprintf("/api/crashes/%d/issue", crashID)
	return c.doJSON(ctx, http.MethodPost, path, nil, IssueCrashRequest{IssueURL: issueURL}, nil)
}

// GetCoverageHistory fetches chronological coverage points from GET /api/coverage/history.
func (c *Client) GetCoverageHistory(ctx context.Context, target string) ([]CoveragePoint, error) {
	q := url.Values{}
	q.Set("target", target)
	var out []CoveragePoint
	err := c.doJSON(ctx, http.MethodGet, "/api/coverage/history", q, nil, &out)
	return out, err
}

// GetStats fetches time-bucketed statistics for a canned query ("throughput", "coverage", "outcomes")
// from GET /api/stats/{query}.
func (c *Client) GetStats(ctx context.Context, query, window, project, target string) (StatsResponse, error) {
	qName := strings.TrimSpace(query)
	if qName == "" {
		qName = StatsQueryThroughput
	}
	q := url.Values{}
	if window != "" {
		q.Set("window", window)
	}
	if project != "" {
		q.Set("project", project)
	}
	if target != "" {
		q.Set("target", target)
	}
	var out StatsResponse
	err := c.doJSON(ctx, http.MethodGet, "/api/stats/"+url.PathEscape(qName), q, nil, &out)
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

func (c *Client) doBytes(ctx context.Context, method, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request %s %s: %w", method, u, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, u, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s %s returned HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
