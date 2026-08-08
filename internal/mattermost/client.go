package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout  = 30 * time.Second
	maxSuccessBodyBytes = 10 << 20
	maxErrorBodyBytes   = 1 << 20
	maxChannelsPerPage  = 200
)

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type ClientOption func(*Client) error

// WithHTTPClient configures the HTTP client used for Mattermost requests.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) error {
		if httpClient == nil {
			return errors.New("Mattermost HTTP client must not be nil")
		}
		client.httpClient = cloneHTTPClient(httpClient)
		return nil
	}
}

// NewClient creates an authenticated Mattermost REST API client.
func NewClient(baseURL, token string, options ...ClientOption) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Mattermost token must not be empty")
	}
	normalizedURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	client := &Client{
		baseURL: normalizedURL,
		token:   token,
		httpClient: cloneHTTPClient(&http.Client{
			Timeout: defaultHTTPTimeout,
		}),
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("Mattermost client option must not be nil")
		}
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func cloneHTTPClient(httpClient *http.Client) *http.Client {
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func normalizeBaseURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Mattermost base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Mattermost base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("Mattermost base URL must include a host")
	}
	if parsed.User != nil {
		return nil, errors.New("Mattermost base URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Mattermost base URL must not include a query or fragment")
	}
	escapedPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return nil, errors.New("Mattermost base URL must not include encoded path separators")
	}

	path := strings.TrimRight(parsed.Path, "/")
	for strings.HasSuffix(path, "/api/v4") {
		path = strings.TrimSuffix(path, "/api/v4")
		path = strings.TrimRight(path, "/")
	}
	parsed.Path = path + "/api/v4"
	parsed.RawPath = ""
	return parsed, nil
}

// CurrentUser returns the authenticated Mattermost user.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var wire userResponse
	if err := c.do(ctx, http.MethodGet, "users/me", nil, &wire); err != nil {
		return nil, err
	}
	return wire.domain(), nil
}

// TeamsForUser returns the teams the user belongs to. ServerID is left empty
// because the REST client has no configured application server identifier.
func (c *Client) TeamsForUser(ctx context.Context, userID string) ([]Team, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Mattermost user ID must not be empty")
	}

	var wire []teamResponse
	if err := c.do(ctx, http.MethodGet, "users/"+url.PathEscape(userID)+"/teams", nil, &wire); err != nil {
		return nil, err
	}
	teams := make([]Team, len(wire))
	for i := range wire {
		teams[i] = wire[i].domain()
	}
	return teams, nil
}

// ChannelsForUser returns one locally paginated page from Mattermost's
// cross-team channel list. The REST endpoint itself returns the complete list
// and does not accept page or per_page query parameters. perPage must be 1..200.
// ServerID and per-user read metadata are left empty; use
// ChannelMembershipsForUser for the latter.
func (c *Client) ChannelsForUser(ctx context.Context, userID string, page, perPage int) ([]Channel, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Mattermost user ID must not be empty")
	}
	if page < 0 {
		return nil, errors.New("Mattermost channel page must not be negative")
	}
	if perPage < 1 || perPage > maxChannelsPerPage {
		return nil, fmt.Errorf("Mattermost channels per page must be between 1 and %d", maxChannelsPerPage)
	}

	var wire []channelResponse
	if err := c.do(ctx, http.MethodGet, "users/"+url.PathEscape(userID)+"/channels", nil, &wire); err != nil {
		return nil, err
	}
	if page > len(wire)/perPage {
		return []Channel{}, nil
	}
	start := page * perPage
	if start >= len(wire) {
		return []Channel{}, nil
	}
	end := min(start+perPage, len(wire))
	channels := make([]Channel, end-start)
	for i := start; i < end; i++ {
		channel, err := wire[i].domain()
		if err != nil {
			return nil, fmt.Errorf("convert Mattermost channel %q: %w", wire[i].ID, err)
		}
		channels[i-start] = channel
	}
	return channels, nil
}

// ChannelMembershipsForUser returns per-user channel read metadata for one
// team. Mattermost exposes this data separately from cross-team channels.
func (c *Client) ChannelMembershipsForUser(ctx context.Context, userID, teamID string) ([]ChannelMembership, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Mattermost user ID must not be empty")
	}
	if strings.TrimSpace(teamID) == "" {
		return nil, errors.New("Mattermost team ID must not be empty")
	}

	endpoint := "users/" + url.PathEscape(userID) + "/teams/" + url.PathEscape(teamID) + "/channels/members"
	var wire []channelMembershipResponse
	if err := c.do(ctx, http.MethodGet, endpoint, nil, &wire); err != nil {
		return nil, err
	}
	memberships := make([]ChannelMembership, len(wire))
	for i := range wire {
		memberships[i] = wire[i].domain()
	}
	return memberships, nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, body io.Reader, output any) error {
	requestURL := *c.baseURL
	requestURL.RawPath = strings.TrimRight(requestURL.EscapedPath(), "/") + "/" + strings.TrimLeft(endpoint, "/")
	path, err := url.PathUnescape(requestURL.RawPath)
	if err != nil {
		return fmt.Errorf("build Mattermost request path: %w", err)
	}
	requestURL.Path = path

	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return fmt.Errorf("create Mattermost request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return redactError("perform Mattermost request", err, c.token)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeAPIError(response, c.token)
	}
	if output == nil {
		return nil
	}
	if err := decodeJSON(response.Body, output, maxSuccessBodyBytes); err != nil {
		return fmt.Errorf("decode Mattermost response: %w", err)
	}
	return nil
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string {
	return e.message
}

func (e *redactedError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func redactError(operation string, err error, secret string) error {
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return &redactedError{
		message: operation + ": " + message,
		cause:   err,
	}
}

func decodeAPIError(response *http.Response, token string) error {
	apiErr := &APIError{StatusCode: response.StatusCode}
	var wire apiErrorResponse
	if err := decodeJSON(response.Body, &wire, maxErrorBodyBytes); err != nil {
		if errors.Is(err, errResponseTooLarge) {
			apiErr.Message = fmt.Sprintf("Mattermost API error response exceeds %d bytes", maxErrorBodyBytes)
		} else {
			apiErr.Message = http.StatusText(response.StatusCode)
		}
	} else {
		apiErr.ID = wire.ID
		apiErr.Message = wire.Message
		apiErr.RequestID = wire.RequestID
		apiErr.DetailedError = wire.DetailedError
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(response.StatusCode)
	}
	apiErr.ID = redactSecret(apiErr.ID, token)
	apiErr.Message = redactSecret(apiErr.Message, token)
	apiErr.RequestID = redactSecret(apiErr.RequestID, token)
	apiErr.DetailedError = redactSecret(apiErr.DetailedError, token)
	return apiErr
}

func redactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

var errResponseTooLarge = errors.New("Mattermost response body exceeds limit")

func decodeJSON(reader io.Reader, output any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%w of %d bytes", errResponseTooLarge, limit)
	}

	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains multiple JSON values")
		}
		return err
	}
	return nil
}

type apiErrorResponse struct {
	ID            string `json:"id"`
	Message       string `json:"message"`
	RequestID     string `json:"request_id"`
	DetailedError string `json:"detailed_error"`
}

type userResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type teamResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (t teamResponse) domain() Team {
	return Team{
		ID:          t.ID,
		Name:        t.Name,
		DisplayName: t.DisplayName,
	}
}

type channelResponse struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

func (c channelResponse) domain() (Channel, error) {
	kind, err := ParseChannelKind(c.Type)
	if err != nil {
		return Channel{}, err
	}
	return Channel{
		ID:          c.ID,
		TeamID:      c.TeamID,
		Name:        c.Name,
		DisplayName: c.DisplayName,
		Kind:        kind,
	}, nil
}

type channelMembershipResponse struct {
	ChannelID    string `json:"channel_id"`
	UserID       string `json:"user_id"`
	MsgCount     int64  `json:"msg_count"`
	MentionCount int64  `json:"mention_count"`
	LastViewedAt int64  `json:"last_viewed_at"`
}

func (m channelMembershipResponse) domain() ChannelMembership {
	return ChannelMembership{
		ChannelID:    m.ChannelID,
		UserID:       m.UserID,
		MsgCount:     m.MsgCount,
		MentionCount: m.MentionCount,
		LastViewedAt: m.LastViewedAt,
	}
}

func (u userResponse) domain() *User {
	return &User{
		ID:        u.ID,
		Username:  u.Username,
		Nickname:  u.Nickname,
		FirstName: u.FirstName,
		LastName:  u.LastName,
	}
}
