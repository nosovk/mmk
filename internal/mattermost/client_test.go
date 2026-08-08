package mattermost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

func TestClientRejectsURLWithQuery(t *testing.T) {
	if _, err := NewClient("https://chat.example.com?redirect=elsewhere", "token"); err == nil {
		t.Fatal("NewClient accepted a URL with a query")
	}
}

func TestClientRejectsURLWithFragment(t *testing.T) {
	if _, err := NewClient("https://chat.example.com#section", "token"); err == nil {
		t.Fatal("NewClient accepted a URL with a fragment")
	}
}

func TestClientRejectsEncodedForwardSlashInDeploymentPath(t *testing.T) {
	if _, err := NewClient("https://chat.example.com/mattermost%2fprivate", "token"); err == nil {
		t.Fatal("NewClient accepted an encoded forward slash")
	}
}

func TestClientRejectsUppercaseEncodedForwardSlashInDeploymentPath(t *testing.T) {
	if _, err := NewClient("https://chat.example.com/mattermost%2Fprivate", "token"); err == nil {
		t.Fatal("NewClient accepted an uppercase encoded forward slash")
	}
}

func TestClientRejectsEncodedBackslashInDeploymentPath(t *testing.T) {
	if _, err := NewClient("https://chat.example.com/mattermost%5Cprivate", "token"); err == nil {
		t.Fatal("NewClient accepted an encoded backslash")
	}
}

func TestClientRejectsEmptyToken(t *testing.T) {
	if _, err := NewClient("https://chat.example.com", ""); err == nil {
		t.Fatal("NewClient accepted an empty token")
	}
}

func TestClientRejectsWhitespaceToken(t *testing.T) {
	if _, err := NewClient("https://chat.example.com", " \t\n "); err == nil {
		t.Fatal("NewClient accepted a whitespace-only token")
	}
}

func TestClientRejectsNilOption(t *testing.T) {
	if _, err := NewClient("https://chat.example.com", "token", nil); err == nil {
		t.Fatal("NewClient accepted a nil option")
	}
}

func TestClientRejectsNilInjectedHTTPClient(t *testing.T) {
	if _, err := NewClient("https://chat.example.com", "token", WithHTTPClient(nil)); err == nil {
		t.Fatal("NewClient accepted a nil injected HTTP client")
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
	transport := &http.Transport{}
	jar := &testCookieJar{}
	callerRedirect := func(*http.Request, []*http.Request) error { return nil }
	injected := &http.Client{
		Transport:     transport,
		CheckRedirect: callerRedirect,
		Jar:           jar,
		Timeout:       time.Second,
	}
	client, err := NewClient("https://chat.example.com", "token", WithHTTPClient(injected))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if client.httpClient == injected {
		t.Fatal("client retained the caller-owned HTTP client instead of cloning it")
	}
	if client.httpClient.Transport != transport || client.httpClient.Jar != jar || client.httpClient.Timeout != time.Second {
		t.Fatal("cloned HTTP client did not preserve transport, jar, and timeout")
	}
	if fmt.Sprintf("%p", client.httpClient.CheckRedirect) == fmt.Sprintf("%p", callerRedirect) {
		t.Fatal("client trusted the caller's redirect policy")
	}
	if injected.CheckRedirect == nil {
		t.Fatal("NewClient mutated the caller-owned HTTP client")
	}
}

func TestClientDoesNotFollowSameHostDifferentPortRedirect(t *testing.T) {
	target, targetRequests := newRedirectTarget(t)
	defer target.Close()

	assertRedirectNotFollowed(t, target.URL, nil, targetRequests)
}

func TestClientDoesNotFollowCrossHostRedirect(t *testing.T) {
	target, targetRequests := newRedirectTarget(t)
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	targetURL.Host = "localhost:" + targetURL.Port()
	assertRedirectNotFollowed(t, targetURL.String(), nil, targetRequests)
}

func TestClientDoesNotFollowSubdomainRedirect(t *testing.T) {
	target, targetRequests := newRedirectTarget(t)
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}

	dialer := &net.Dialer{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "subdomain.example:") {
			address = targetURL.Host
		}
		return dialer.DialContext(ctx, network, address)
	}
	injected := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return nil
		},
	}
	redirectURL := "http://subdomain.example:" + targetURL.Port() + "/redirect-target"
	assertRedirectNotFollowed(t, redirectURL, injected, targetRequests)
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

func TestAPIErrorDiscardsPartiallyDecodedMalformedPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"id":"partial-id","message":`)
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
	if apiErr.ID != "" || apiErr.RequestID != "" || apiErr.DetailedError != "" {
		t.Fatalf("malformed payload leaked partial fields: %#v", apiErr)
	}
	if got, want := apiErr.Message, http.StatusText(http.StatusBadRequest); got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestAPIErrorRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"`+strings.Repeat("x", (1<<20)+1)+`"}`)
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
	if !strings.Contains(apiErr.Message, "exceeds") {
		t.Fatalf("Message = %q, want response size fallback", apiErr.Message)
	}
}

func TestAPIErrorSanitizesTokenFromExportedFields(t *testing.T) {
	const token = "highly-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{
			"id":"id-highly-secret-token",
			"message":"message highly-secret-token",
			"request_id":"request-highly-secret-token",
			"detailed_error":"detail highly-secret-token"
		}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	assertStringsDoNotContain(t, token, apiErr.Error(), apiErr.ID, apiErr.Message, apiErr.RequestID, apiErr.DetailedError)
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

func TestClientPreservesContextDeadlineExceeded(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	_, err = client.CurrentUser(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
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

func TestClientClosesResponseBodyForAPIError(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader(`{"message":"unauthorized"}`)}
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if _, err := client.CurrentUser(context.Background()); err == nil {
		t.Fatal("CurrentUser returned nil error")
	}
	if !body.closed {
		t.Fatal("API error response body was not closed")
	}
}

func TestClientClosesResponseBodyForDecodeError(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader(`{"id":`)}
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

	if _, err := client.CurrentUser(context.Background()); err == nil {
		t.Fatal("CurrentUser returned nil error")
	}
	if !body.closed {
		t.Fatal("malformed response body was not closed")
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

func TestClientRejectsMultipleSuccessJSONValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"user-1"} {"id":"user-2"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("error = %v, want multiple JSON values error", err)
	}
}

func TestClientRejectsOversizedSuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"`+strings.Repeat("x", (10<<20)+1)+`"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want response size error", err)
	}
}

func TestClientDoesNotExposeTokenInErrors(t *testing.T) {
	const token = "highly-secret-token"
	sentinel := errors.New("transport sentinel")
	transportErr := &secretTransportError{token: token, sentinel: sentinel}
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	client, err := NewClient("https://chat.example.com", token, WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	_, err = client.CurrentUser(context.Background())
	if err == nil {
		t.Fatal("CurrentUser returned nil error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want transport sentinel match", err)
	}
	assertErrorChainDoesNotContain(t, err, token)

	var recovered *secretTransportError
	if errors.As(err, &recovered) {
		t.Fatalf("errors.As recovered unsanitized transport error: %v", recovered)
	}
	assertStringsDoNotContain(t, token, fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))
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

type secretTransportError struct {
	token    string
	sentinel error
}

func (e *secretTransportError) Error() string {
	return "transport accidentally included " + e.token
}

func (e *secretTransportError) Is(target error) bool {
	return target == e.sentinel
}

func assertErrorChainDoesNotContain(t *testing.T, err error, secret string) {
	t.Helper()
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), secret) {
			t.Fatalf("error chain exposed token through %T: %v", current, current)
		}
	}
}

func assertStringsDoNotContain(t *testing.T, secret string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(value, secret) {
			t.Fatalf("value exposed token: %q", value)
		}
	}
}

type testCookieJar struct{}

func (*testCookieJar) SetCookies(*url.URL, []*http.Cookie) {}

func (*testCookieJar) Cookies(*url.URL) []*http.Cookie { return nil }

func newRedirectTarget(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Errorf("redirect target received Authorization %q", authorization)
		}
		_, _ = io.WriteString(w, `{"id":"redirected-user"}`)
	}))
	return server, requests
}

func assertRedirectNotFollowed(t *testing.T, redirectURL string, injected *http.Client, targetRequests *atomic.Int32) {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, redirectURL, http.StatusFound)
	}))
	defer origin.Close()

	options := []ClientOption{}
	if injected != nil {
		options = append(options, WithHTTPClient(injected))
	}
	client, err := NewClient(origin.URL, "secret-token", options...)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError for redirect", err, err)
	}
	if got, want := apiErr.StatusCode, http.StatusFound; got != want {
		t.Fatalf("StatusCode = %d, want %d", got, want)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
}
