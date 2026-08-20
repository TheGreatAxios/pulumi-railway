package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientAuthenticationHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		auth         authKind
		header       string
		expected     string
		absentHeader string
	}{
		{
			name: "account token", auth: accountAuth,
			header: "Authorization", expected: "Bearer test-token",
			absentHeader: "Project-Access-Token",
		},
		{
			name: "project token", auth: projectAuth,
			header: "Project-Access-Token", expected: "test-token",
			absentHeader: "Authorization",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(test.header); got != test.expected {
					t.Errorf("%s = %q, want %q", test.header, got, test.expected)
				}
				if got := r.Header.Get(test.absentHeader); got != "" {
					t.Errorf("%s should not be set, got %q", test.absentHeader, got)
				}
				if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "pulumi-railway/") {
					t.Errorf("User-Agent = %q", got)
				}
				writeGraphQL(t, w, map[string]interface{}{"ok": true})
			}))
			defer server.Close()

			client := newClient(server.URL, "  test-token  ", test.auth, server.Client())
			if err := client.query(t.Context(), "query { ok }", nil, nil); err != nil {
				t.Fatalf("query failed: %v", err)
			}
		})
	}
}

func TestClientRetriesRateLimitsAndServerErrors(t *testing.T) {
	t.Parallel()
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		switch attempts {
		case 1:
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		case 2:
			http.Error(w, "unavailable", http.StatusBadGateway)
		default:
			writeGraphQL(t, w, map[string]interface{}{"ok": true})
		}
	}))
	defer server.Close()

	client := newClient(server.URL, "token", accountAuth, server.Client())
	client.retryBase = time.Millisecond
	if err := client.query(t.Context(), "query retryTest { ok }", nil, nil); err != nil {
		t.Fatalf("query failed after retries: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestClientRejectsEmptyToken(t *testing.T) {
	t.Parallel()
	client := newClient("https://example.invalid", "", accountAuth, &http.Client{})
	err := client.query(t.Context(), "query { ok }", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "token is empty") {
		t.Fatalf("expected empty token error, got %v", err)
	}
}

func TestClientPreservesGraphQLErrorsAndNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Service not found","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer server.Close()

	client := newClient(server.URL, "test-token", accountAuth, server.Client())
	err := client.query(t.Context(), "query { service }", nil, nil)
	if !isNotFound(err) {
		t.Fatalf("expected not-found error, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
}

func TestIsNotFoundPrefersErrorCodesAndUsesMessagesOnlyAsFallback(t *testing.T) {
	t.Parallel()
	withValidationCode := &APIError{Errors: []gqlError{{Message: "referenced service not found"}}}
	withValidationCode.Errors[0].Extensions.Code = "BAD_USER_INPUT"
	if isNotFound(withValidationCode) {
		t.Fatal("validation error with a non-not-found code must not be treated as missing")
	}

	legacy := &APIError{Errors: []gqlError{{Message: "Service could not find"}}}
	if !isNotFound(legacy) {
		t.Fatal("legacy message without an error code should remain a not-found fallback")
	}
}

func TestClientHonorsHTTPTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	client := newClient(server.URL, "test-token", accountAuth, httpClient)
	if err := client.query(context.Background(), "query { ok }", nil, nil); err == nil {
		t.Fatal("expected timeout error")
	}
}

func writeGraphQL(t *testing.T, w http.ResponseWriter, data interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"data": data}); err != nil {
		t.Errorf("write response: %v", err)
	}
}
