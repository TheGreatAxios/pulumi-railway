package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/thegreataxios/pulumi-railway/provider/pkg/version"
)

const (
	railwayEndpoint = "https://backboard.railway.com/graphql/v2"
	maxResponseSize = 10 << 20
	maxRetries      = 3
)

type authKind int

const (
	accountAuth authKind = iota
	projectAuth
)

// Client is a thin GraphQL client for the Railway public API.
type Client struct {
	endpoint   string
	token      string
	auth       authKind
	httpClient *http.Client
	userAgent  string
	retryBase  time.Duration
}

func NewClient(token string) *Client {
	return newClient(railwayEndpoint, token, accountAuth, &http.Client{Timeout: 30 * time.Second})
}

func NewProjectClient(token string) *Client {
	return newClient(railwayEndpoint, token, projectAuth, &http.Client{Timeout: 30 * time.Second})
}

func newClient(endpoint, token string, auth authKind, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		endpoint:   endpoint,
		token:      strings.TrimSpace(token),
		auth:       auth,
		httpClient: httpClient,
		userAgent:  "pulumi-railway/" + version.Version,
		retryBase:  200 * time.Millisecond,
	}
}

type gqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors,omitempty"`
}

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions,omitempty"`
}

// APIError contains errors returned by Railway's GraphQL API.
type APIError struct {
	Errors []gqlError
}

func (e *APIError) Error() string {
	messages := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		messages = append(messages, item.Message)
	}
	return "railway api error: " + strings.Join(messages, "; ")
}

func isNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, item := range apiErr.Errors {
		code := strings.ToLower(item.Extensions.Code)
		if code == "not_found" || code == "not-found" || code == "notfound" {
			return true
		}
	}
	// Older Railway errors did not consistently include extensions.code.
	for _, item := range apiErr.Errors {
		if item.Extensions.Code != "" {
			continue
		}
		message := strings.ToLower(item.Message)
		if strings.Contains(message, "not found") || strings.Contains(message, "could not find") {
			return true
		}
	}
	return false
}

func (c *Client) do(ctx context.Context, query string, vars map[string]interface{}, result interface{}) error {
	if c.token == "" {
		return errors.New("railway API token is empty")
	}

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	operation := graphQLOperationName(query)
	provider.GetLogger(ctx).Debugf("Railway GraphQL operation: %s", operation)

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		switch c.auth {
		case accountAuth:
			req.Header.Set("Authorization", "Bearer "+c.token)
		case projectAuth:
			req.Header.Set("Project-Access-Token", c.token)
		default:
			return errors.New("unsupported Railway authentication mode")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("http request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close response: %w", closeErr)
		}
		if len(raw) > maxResponseSize {
			return fmt.Errorf("railway API response exceeded %d bytes", maxResponseSize)
		}

		if shouldRetry(resp.StatusCode) && attempt < maxRetries {
			delay := retryDelay(resp.Header.Get("Retry-After"), c.retryBase, attempt)
			provider.GetLogger(ctx).Debugf(
				"Railway GraphQL operation %s returned HTTP %d; retrying in %s",
				operation, resp.StatusCode, delay,
			)
			if err := sleepContext(ctx, delay); err != nil {
				return fmt.Errorf("wait to retry Railway API request: %w", err)
			}
			continue
		}

		var gqlResp gqlResponse
		if err := json.Unmarshal(raw, &gqlResp); err != nil {
			return fmt.Errorf("decode Railway API response (HTTP %d): %w", resp.StatusCode, err)
		}
		if len(gqlResp.Errors) > 0 {
			return &APIError{Errors: gqlResp.Errors}
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("railway API returned HTTP %d", resp.StatusCode)
		}

		if result != nil && len(gqlResp.Data) > 0 && string(gqlResp.Data) != "null" {
			if err := json.Unmarshal(gqlResp.Data, result); err != nil {
				return fmt.Errorf("decode Railway API data: %w", err)
			}
		}
		return nil
	}
}

func shouldRetry(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

func retryDelay(retryAfter string, base time.Duration, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(retryAfter); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return base * time.Duration(1<<attempt)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func graphQLOperationName(query string) string {
	fields := strings.Fields(query)
	if len(fields) >= 2 && (fields[0] == "query" || fields[0] == "mutation") {
		if index := strings.IndexByte(fields[1], '('); index >= 0 {
			return fields[1][:index]
		}
		return fields[1]
	}
	return "anonymous"
}

func (c *Client) query(ctx context.Context, query string, vars map[string]interface{}, result interface{}) error {
	return c.do(ctx, query, vars, result)
}

func (c *Client) mutate(ctx context.Context, mutation string, vars map[string]interface{}, result interface{}) error {
	return c.do(ctx, mutation, vars, result)
}
