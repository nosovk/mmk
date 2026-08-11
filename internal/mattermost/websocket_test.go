package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const webSocketTestTimeout = 2 * time.Second

func TestWebSocketConnectsAuthenticatesDecodesAndToleratesUnknownEvents(t *testing.T) {
	authenticated := make(chan struct{})
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/mattermost/api/v4/websocket"; got != want {
			serverErrors <- &webSocketTestError{got: got, want: want}
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()

		var challenge struct {
			Seq    int             `json:"seq"`
			Action string          `json:"action"`
			Data   json.RawMessage `json:"data"`
		}
		if err := conn.ReadJSON(&challenge); err != nil {
			serverErrors <- err
			return
		}
		var data struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(challenge.Data, &data); err != nil {
			serverErrors <- err
			return
		}
		if challenge.Seq != 1 || challenge.Action != "authentication_challenge" || data.Token != "test-token" {
			serverErrors <- &webSocketTestError{got: challenge.Action, want: "authentication_challenge with seq 1 and test token"}
			return
		}
		close(authenticated)

		if err := conn.WriteJSON(map[string]any{"event": "future_event", "data": map[string]any{"value": true}}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"channel-1","user_id":"user-1","message":"hello","create_at":10}`}}); err != nil {
			serverErrors <- err
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL+"/mattermost", "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan Event, 1)
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func(event Event) { events <- event }) }()

	waitWebSocket(t, authenticated, "authentication challenge")
	got := waitWebSocket(t, events, "posted event")
	want := PostedEvent{Message: Message{ID: "post-1", ChannelID: "channel-1", UserID: "user-1", Text: "hello", CreatedAt: 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	select {
	case err := <-serverErrors:
		t.Fatal(err)
	default:
	}
}

func TestWebSocketRespondsToPing(t *testing.T) {
	pong := make(chan struct{})
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			serverErrors <- err
			return
		}
		conn.SetPongHandler(func(string) error {
			select {
			case <-pong:
			default:
				close(pong)
			}
			return nil
		})
		if err := conn.WriteControl(websocket.PingMessage, []byte("probe"), time.Now().Add(webSocketTestTimeout)); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(webSocketTestTimeout)); err != nil {
			serverErrors <- err
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				serverErrors <- err
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func(Event) {}) }()
	waitWebSocket(t, pong, "pong")
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestWebSocketCancellationUnblocksRead(t *testing.T) {
	connected := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		close(connected)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func(Event) {}) }()
	waitWebSocket(t, connected, "connection")
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestWebSocketRejectsOversizedMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"posted","data":{"post":"{\"id\":\"post-1\",\"channel_id\":\"channel-1\",\"message\":\"`+strings.Repeat("x", maxSuccessBodyBytes)+`\"}"}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	delivered := atomic.Bool{}
	err = client.RunWebSocket(context.Background(), func(Event) { delivered.Store(true) })
	if !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("error = %v, want websocket.ErrReadLimit", err)
	}
	if delivered.Load() {
		t.Fatal("oversized message was delivered")
	}
}

func TestWebSocketRejectsUnsupportedHTTPTransport(t *testing.T) {
	client, err := NewClient("https://chat.example.com", "test-token", WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("must not be called")
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = client.RunWebSocket(context.Background(), func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "HTTP transport") {
		t.Fatalf("error = %v, want unsupported HTTP transport setup error", err)
	}
}

func TestWebSocketUsesHTTPClientCookieJar(t *testing.T) {
	authenticated := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "cookie-value" {
			http.Error(w, "missing cookie", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err == nil {
			close(authenticated)
			<-r.Context().Done()
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(&http.Client{
		Transport: server.Client().Transport,
		Jar:       staticWebSocketCookieJar{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func(Event) {}) }()
	waitWebSocket(t, authenticated, "cookie-authenticated WebSocket")
	cancel()
	err = waitWebSocket(t, done, "cookie test cancellation")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled after authenticated cookie request", err)
	}
}

func TestWebSocketCancellationInterruptsAuthenticationWrite(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	connected := make(chan struct{})
	transport := server.Client().Transport.(*http.Transport).Clone()
	baseDial := transport.DialContext
	if baseDial == nil {
		baseDial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := baseDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &blockingAuthConn{Conn: conn, connected: connected}, nil
	}
	client, err := NewClient(server.URL, "test-token", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func(Event) {}) }()
	waitWebSocket(t, connected, "WebSocket handshake")
	cancel()
	if err := waitWebSocket(t, done, "authentication cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

type webSocketTestError struct {
	got  string
	want string
}

func (e *webSocketTestError) Error() string { return "got " + e.got + ", want " + e.want }

type staticWebSocketCookieJar struct{}

func (staticWebSocketCookieJar) SetCookies(*url.URL, []*http.Cookie) {}

func (staticWebSocketCookieJar) Cookies(*url.URL) []*http.Cookie {
	return []*http.Cookie{{Name: "session", Value: "cookie-value"}}
}

type blockingAuthConn struct {
	net.Conn
	connected chan<- struct{}
	writes    atomic.Int32
	once      sync.Once
}

func (c *blockingAuthConn) Write(p []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		return c.Conn.Write(p)
	}
	c.once.Do(func() { close(c.connected) })
	buffer := make([]byte, 1)
	_, err := c.Conn.Read(buffer)
	return 0, err
}

func waitWebSocket[T any](t *testing.T, ch <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(webSocketTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}
