package mattermost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const (
	defaultHTTPTimeout  = 30 * time.Second
	maxSuccessBodyBytes = 10 << 20
	// The unpaginated cross-team channel endpoint needs a higher finite cap.
	maxChannelBodyBytes = 64 << 20
	maxErrorBodyBytes   = 1 << 20
	bulkLookupBatchSize = 100
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
	root, err := CanonicalServerRoot(baseURL)
	if err != nil {
		return nil, err
	}
	normalizedURL, err := url.Parse(root + "/api/v4")
	if err != nil {
		return nil, fmt.Errorf("parse canonical Mattermost URL: %w", err)
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

// CanonicalServerRoot returns the stable deployment root used by both config
// identity and the REST client. It never includes Mattermost's /api/v4 suffix.
func CanonicalServerRoot(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse Mattermost base URL: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Mattermost base URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("Mattermost base URL must include a host")
	}
	if parsed.User != nil {
		return "", errors.New("Mattermost base URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Mattermost base URL must not include a query or fragment")
	}
	escapedPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return "", errors.New("Mattermost base URL must not include encoded path separators")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if net.ParseIP(hostname) == nil {
		hostname = strings.TrimSuffix(hostname, ".")
		if hostname == "" || strings.Contains(hostname, "..") {
			return "", errors.New("Mattermost base URL contains an invalid hostname")
		}
		hostname, err = idna.Lookup.ToASCII(hostname)
		if err != nil {
			return "", fmt.Errorf("normalize Mattermost hostname: %w", err)
		}
	}
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	parsed.Host = hostname
	if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	}
	if port != "" {
		parsed.Host += ":" + port
	}

	cleanPath := path.Clean("/" + strings.TrimLeft(parsed.Path, "/"))
	if cleanPath == "/" {
		cleanPath = ""
	}
	if strings.HasSuffix(strings.ToLower(cleanPath), "/api/v4") {
		cleanPath = cleanPath[:len(cleanPath)-len("/api/v4")]
		cleanPath = strings.TrimRight(cleanPath, "/")
	}
	parsed.Path = cleanPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

// ServerID derives a filesystem- and keyring-safe stable ID from a canonical
// deployment root. The full 128-bit hash suffix keeps host-slug collisions safe.
func ServerID(canonicalRoot string) string {
	parsed, _ := url.Parse(canonicalRoot)
	slug := strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(parsed.Hostname())), "-")
	if slug == "" {
		slug = "mattermost"
	}
	sum := sha256.Sum256([]byte(canonicalRoot))
	return fmt.Sprintf("%s-%x", slug, sum[:16])
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

// ChannelsForUser returns Mattermost's complete cross-team channel list. The
// endpoint does not accept pagination parameters. ServerID and per-user read
// metadata are left empty; use ChannelMembershipsForUser for the latter.
func (c *Client) ChannelsForUser(ctx context.Context, userID string) ([]Channel, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Mattermost user ID must not be empty")
	}

	response, err := c.request(ctx, http.MethodGet, "users/"+url.PathEscape(userID)+"/channels", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	channels, err := decodeChannels(response.Body, maxChannelBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("decode Mattermost response: %w", err)
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

// UsersByIDs returns requested users in first-seen ID order. Mattermost users
// not present in the request are ignored, as are duplicate response users.
func (c *Client) UsersByIDs(ctx context.Context, ids []string) ([]User, error) {
	unique, err := uniqueNonBlankIDs(ids, "Mattermost user ID")
	if err != nil {
		return nil, err
	}
	if len(unique) == 0 {
		return []User{}, nil
	}

	usersByID := make(map[string]User, len(unique))
	requested := make(map[string]struct{}, len(unique))
	for _, id := range unique {
		requested[id] = struct{}{}
	}
	for batchIndex, batch := range batches(unique, bulkLookupBatchSize) {
		var wire []userResponse
		if err := c.doJSON(ctx, http.MethodPost, "users/ids", batch, &wire); err != nil {
			return nil, fmt.Errorf("fetch Mattermost users by IDs batch %d: %w", batchIndex+1, err)
		}
		for _, item := range wire {
			if _, ok := requested[item.ID]; !ok {
				continue
			}
			if _, exists := usersByID[item.ID]; exists {
				continue
			}
			usersByID[item.ID] = *item.domain()
		}
	}

	users := make([]User, 0, len(usersByID))
	for _, id := range unique {
		if user, ok := usersByID[id]; ok {
			users = append(users, user)
		}
	}
	return users, nil
}

// UsersByGroupChannelIDs returns group-channel participants for requested
// channels. Unexpected response keys are ignored.
func (c *Client) UsersByGroupChannelIDs(ctx context.Context, channelIDs []string) (map[string][]User, error) {
	unique, err := uniqueNonBlankIDs(channelIDs, "Mattermost channel ID")
	if err != nil {
		return nil, err
	}
	result := make(map[string][]User, len(unique))
	if len(unique) == 0 {
		return result, nil
	}

	requested := make(map[string]struct{}, len(unique))
	for _, id := range unique {
		requested[id] = struct{}{}
	}
	for batchIndex, batch := range batches(unique, bulkLookupBatchSize) {
		var wire map[string][]userResponse
		if err := c.doJSON(ctx, http.MethodPost, "users/group_channels", batch, &wire); err != nil {
			return nil, fmt.Errorf("fetch Mattermost group channel users batch %d: %w", batchIndex+1, err)
		}
		for channelID, items := range wire {
			if _, ok := requested[channelID]; !ok {
				continue
			}
			seen := make(map[string]struct{}, len(items))
			users := make([]User, 0, len(items))
			for _, item := range items {
				if _, exists := seen[item.ID]; exists {
					continue
				}
				seen[item.ID] = struct{}{}
				users = append(users, *item.domain())
			}
			result[channelID] = users
		}
	}
	return result, nil
}

func uniqueNonBlankIDs(ids []string, label string) ([]string, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%s at index %d must not be empty", label, index)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

func batches[T any](values []T, size int) [][]T {
	result := make([][]T, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := min(start+size, len(values))
		result = append(result, values[start:end])
	}
	return result
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, input, output any) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(input); err != nil {
		return fmt.Errorf("encode Mattermost request: %w", err)
	}
	return c.do(ctx, method, endpoint, &body, output)
}

func (c *Client) do(ctx context.Context, method, endpoint string, body io.Reader, output any) error {
	response, err := c.request(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if output == nil {
		return nil
	}
	if err := decodeJSON(response.Body, output, maxSuccessBodyBytes); err != nil {
		return fmt.Errorf("decode Mattermost response: %w", err)
	}
	return nil
}

// request performs shared request construction, authentication, transport, and
// status handling. The caller owns the returned successful response body.
func (c *Client) request(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	requestURL := *c.baseURL
	requestURL.RawPath = strings.TrimRight(requestURL.EscapedPath(), "/") + "/" + strings.TrimLeft(endpoint, "/")
	path, err := url.PathUnescape(requestURL.RawPath)
	if err != nil {
		return nil, fmt.Errorf("build Mattermost request path: %w", err)
	}
	requestURL.Path = path

	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create Mattermost request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, redactError("perform Mattermost request", err, c.token)
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		return nil, decodeAPIError(response, c.token)
	}
	return response, nil
}

type redactedError struct {
	message string
	matches func(error) bool
	commit  func() bool
}

func (e *redactedError) Error() string {
	return e.message
}

func (e *redactedError) Is(target error) bool {
	return e.matches != nil && e.matches(target)
}

func (e *redactedError) Committed() bool {
	return e.commit != nil && e.commit()
}

func redactError(operation string, err error, secrets ...string) error {
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return &redactedError{
		message: operation + ": " + message,
		matches: func(target error) bool { return errors.Is(err, target) },
		commit: func() bool {
			var committed interface{ Committed() bool }
			return errors.As(err, &committed) && committed.Committed()
		},
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

func decodeChannels(reader io.Reader, limit int64) ([]Channel, error) {
	counted := &countingReader{reader: io.LimitReader(reader, limit+1)}
	decoder := json.NewDecoder(counted)

	token, err := decoder.Token()
	if err != nil {
		return nil, channelDecodeError(err, counted.count, limit)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return nil, errors.New("Mattermost channel response must be a JSON array")
	}

	channels := make([]Channel, 0)
	for decoder.More() {
		var wire channelResponse
		if err := decoder.Decode(&wire); err != nil {
			return nil, channelDecodeError(err, counted.count, limit)
		}
		channel, err := wire.domain()
		if err != nil {
			return nil, fmt.Errorf("convert Mattermost channel %q: %w", wire.ID, err)
		}
		channels = append(channels, channel)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, channelDecodeError(err, counted.count, limit)
	}

	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if counted.count > limit {
			return nil, fmt.Errorf("%w of %d bytes", errResponseTooLarge, limit)
		}
		if err == nil {
			return nil, errors.New("response contains multiple JSON values")
		}
		return nil, err
	}
	if counted.count > limit {
		return nil, fmt.Errorf("%w of %d bytes", errResponseTooLarge, limit)
	}
	return channels, nil
}

func channelDecodeError(err error, count, limit int64) error {
	if count > limit {
		return fmt.Errorf("%w of %d bytes", errResponseTooLarge, limit)
	}
	return err
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
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
