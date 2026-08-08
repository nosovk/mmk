package mattermost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientNormalizesHostURL(t *testing.T) {
	client, err := NewClient("https://chat.example.com", "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if got, want := client.baseURL.String(), "https://chat.example.com/api/v4"; got != want {
		t.Fatalf("base URL = %q, want %q", got, want)
	}
}

func TestClientNormalizesTrailingSlash(t *testing.T) {
	client, err := NewClient("https://chat.example.com/", "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if got, want := client.baseURL.String(), "https://chat.example.com/api/v4"; got != want {
		t.Fatalf("base URL = %q, want %q", got, want)
	}
}

func TestClientPreservesExistingAPIPrefix(t *testing.T) {
	client, err := NewClient("https://chat.example.com/api/v4/", "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if got, want := client.baseURL.String(), "https://chat.example.com/api/v4"; got != want {
		t.Fatalf("base URL = %q, want %q", got, want)
	}
}

func TestClientPreservesDeploymentSubpath(t *testing.T) {
	client, err := NewClient("https://chat.example.com/mattermost/", "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if got, want := client.baseURL.String(), "https://chat.example.com/mattermost/api/v4"; got != want {
		t.Fatalf("base URL = %q, want %q", got, want)
	}
}

func TestClientRejectsMalformedURL(t *testing.T) {
	if _, err := NewClient("://bad", "token"); err == nil {
		t.Fatal("NewClient accepted a malformed URL")
	}
}

func TestClientRejectsNonHTTPURL(t *testing.T) {
	if _, err := NewClient("ftp://chat.example.com", "token"); err == nil {
		t.Fatal("NewClient accepted a non-HTTP URL")
	}
}

func TestClientRejectsURLWithoutHost(t *testing.T) {
	if _, err := NewClient("https:///mattermost", "token"); err == nil {
		t.Fatal("NewClient accepted a URL without a host")
	}
}

func TestClientRejectsCredentialsInURL(t *testing.T) {
	if _, err := NewClient("https://user:password@chat.example.com", "token"); err == nil {
		t.Fatal("NewClient accepted credentials in the URL")
	}
}

func TestClientUsesSensibleDefaultTimeout(t *testing.T) {
	client, err := NewClient("https://chat.example.com", "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if client.httpClient.Timeout <= 0 {
		t.Fatalf("default timeout = %v, want a positive timeout", client.httpClient.Timeout)
	}
}

func TestClientUsesInjectedHTTPClient(t *testing.T) {
	injected := &http.Client{Timeout: time.Second}
	client, err := NewClient("https://chat.example.com", "token", WithHTTPClient(injected))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if client.httpClient != injected {
		t.Fatal("client did not retain the injected HTTP client")
	}
}

func TestClientCurrentUserCallsUsersMe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v4/users/me"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"user-1"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestClientCurrentUserSendsAuthenticationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer secret-token"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"id":"user-1"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestClientCurrentUserRequestsJSONWithoutContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Accept"), "application/json"; got != want {
			t.Errorf("Accept = %q, want %q", got, want)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty header for bodyless request", got)
		}
		_, _ = io.WriteString(w, `{"id":"user-1"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestClientSetsContentTypeWhenRequestHasBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("Content-Type = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.do(context.Background(), http.MethodPost, "test", strings.NewReader(`{}`), nil); err != nil {
		t.Fatalf("do returned error: %v", err)
	}
}

func TestClientCurrentUserConvertsWireResponseToDomainUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"id":"user-1",
			"username":"mgarcia",
			"nickname":"Mags",
			"first_name":"Maria",
			"last_name":"Garcia",
			"email":"ignored@example.com"
		}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	user, err := client.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
	want := User{ID: "user-1", Username: "mgarcia", Nickname: "Mags", FirstName: "Maria", LastName: "Garcia"}
	if *user != want {
		t.Fatalf("user = %#v, want %#v", *user, want)
	}
}

func TestAPIErrorPreservesMattermostFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{
			"id":"api.context.session_expired.app_error",
			"message":"Invalid or expired session",
			"request_id":"request-1",
			"detailed_error":"token rejected"
		}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if got, want := apiErr.StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("StatusCode = %d, want %d", got, want)
	}
	if got, want := apiErr.ID, "api.context.session_expired.app_error"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
	if got, want := apiErr.Message, "Invalid or expired session"; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
	if got, want := apiErr.RequestID, "request-1"; got != want {
		t.Fatalf("RequestID = %q, want %q", got, want)
	}
	if got, want := apiErr.DetailedError, "token rejected"; got != want {
		t.Fatalf("DetailedError = %q, want %q", got, want)
	}
}

func TestAPIErrorFormatsUserReadableMessage(t *testing.T) {
	err := (&APIError{StatusCode: http.StatusUnauthorized, ID: "mattermost.error", Message: "authentication failed"}).Error()
	if !strings.Contains(err, "401") || !strings.Contains(err, "authentication failed") {
		t.Fatalf("Error() = %q, want status and message", err)
	}
}

func TestAPIErrorUsesHTTPStatusForInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `not-json`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if got, want := apiErr.StatusCode, http.StatusBadGateway; got != want {
		t.Fatalf("StatusCode = %d, want %d", got, want)
	}
	if apiErr.Message == "" {
		t.Fatal("APIError message is empty")
	}
}

func TestAPIErrorUsesHTTPStatusForEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiErr.Message == "" {
		t.Fatal("APIError message is empty")
	}
}

func TestClientPreservesContextCancellation(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.CurrentUser(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestClientClosesResponseBody(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader(`{"id":"user-1"}`)}
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if _, err := client.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestClientReportsMalformedSuccessJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode Mattermost response") {
		t.Fatalf("error = %v, want contextual JSON decode error", err)
	}
}

func TestClientDoesNotExposeTokenInErrors(t *testing.T) {
	const token = "highly-secret-token"
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport accidentally included " + token)
	})}
	client, err := NewClient("https://chat.example.com", token, WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	_, err = client.CurrentUser(context.Background())
	if err == nil {
		t.Fatal("CurrentUser returned nil error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error exposed token: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
