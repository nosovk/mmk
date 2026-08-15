package mattermost

import (
	"context"
	"crypto/tls"
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

func TestChannelViewedWebSocketDeliversKnownEventsInOrderAndIgnoresUnknown(t *testing.T) {
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
		if err := writeWebSocketAuthOK(conn); err != nil {
			serverErrors <- err
			return
		}

		if err := conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"channel-1","user_id":"user-1","message":"first","create_at":10}`}}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{"event": "future_event", "data": map[string]any{"value": true}}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{"event": "multiple_channels_viewed", "data": map[string]any{"channel_times": map[string]any{"channel-1": 20}}, "broadcast": map[string]any{"user_id": "user-1"}}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-2","channel_id":"channel-1","user_id":"user-1","message":"second","create_at":30}`}}); err != nil {
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
	events := make(chan Event, 3)
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(event Event) { events <- event }, nil) }()

	waitWebSocket(t, authenticated, "authentication challenge")
	want := []Event{
		PostedEvent{Message: Message{ID: "post-1", ChannelID: "channel-1", UserID: "user-1", Text: "first", CreatedAt: 10}},
		ChannelViewedEvent{UserID: "user-1", Updates: []ChannelViewUpdate{{ChannelID: "channel-1", ViewedAt: 20, HasViewedAt: true}}},
		PostedEvent{Message: Message{ID: "post-2", ChannelID: "channel-1", UserID: "user-1", Text: "second", CreatedAt: 30}},
	}
	for i := range want {
		got := waitWebSocket(t, events, "ordered known event")
		if !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("event %d = %#v, want %#v", i, got, want[i])
		}
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

func TestWebSocketContinuesAfterMalformedPostedFrame(t *testing.T) {
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
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"broken"`}})
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"channel-1","message":"valid","create_at":1}`}})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 1)
	diagnostics := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.RunWebSocket(ctx, func() {}, func(event Event) { events <- event }, func(err error) { diagnostics <- err })
	}()

	if err := waitWebSocket(t, diagnostics, "malformed frame diagnostic"); err == nil || strings.Contains(err.Error(), "broken") {
		t.Fatalf("diagnostic=%v", err)
	}
	got := waitWebSocket(t, events, "valid event after malformed frame")
	want := PostedEvent{Message: Message{ID: "post-1", ChannelID: "channel-1", Text: "valid", CreatedAt: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event=%#v want %#v", got, want)
	}
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
}

func TestWebSocketStopsAfterConsecutiveMalformedFrameBudget(t *testing.T) {
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
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		for range maxConsecutiveMalformedWebSocketFrames {
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": "sentinel-malformed-payload"}})
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := make(chan error, maxConsecutiveMalformedWebSocketFrames)
	err = client.RunWebSocket(context.Background(), func() {}, func(Event) {}, func(err error) { diagnostics <- err })
	if err == nil || !strings.Contains(err.Error(), "malformed frame budget") || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("error=%v", err)
	}
	if len(diagnostics) != maxConsecutiveMalformedWebSocketFrames {
		t.Fatalf("diagnostics=%d want %d", len(diagnostics), maxConsecutiveMalformedWebSocketFrames)
	}
}

func TestWebSocketValidUnknownEventResetsMalformedFrameBudget(t *testing.T) {
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
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		for range maxConsecutiveMalformedWebSocketFrames - 1 {
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": "bad"}})
		}
		_ = conn.WriteJSON(map[string]any{"event": "future_event", "data": map[string]any{"ok": true}})
		for range maxConsecutiveMalformedWebSocketFrames - 1 {
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": "bad"}})
		}
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"c1","create_at":1}`}})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.RunWebSocket(ctx, func() {}, func(event Event) { events <- event }, func(error) {})
	}()
	waitWebSocket(t, events, "valid event after budget resets")
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestChannelViewedWebSocketResetsMalformedFrameBudget(t *testing.T) {
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
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		for range maxConsecutiveMalformedWebSocketFrames - 1 {
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": "bad"}})
		}
		_ = conn.WriteJSON(map[string]any{
			"event":     "multiple_channels_viewed",
			"data":      map[string]any{"channel_times": map[string]any{"channel-1": 20}},
			"broadcast": map[string]any{"user_id": "user-1"},
		})
		for range maxConsecutiveMalformedWebSocketFrames - 1 {
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": "bad"}})
		}
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"channel-1","create_at":1}`}})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 2)
	done := make(chan error, 1)
	go func() {
		done <- client.RunWebSocket(ctx, func() {}, func(event Event) { events <- event }, func(error) {})
	}()

	viewed := waitWebSocket(t, events, "viewed event after first malformed frame run")
	wantViewed := ChannelViewedEvent{UserID: "user-1", Updates: []ChannelViewUpdate{{ChannelID: "channel-1", ViewedAt: 20, HasViewedAt: true}}}
	if !reflect.DeepEqual(viewed, wantViewed) {
		t.Fatalf("viewed event = %#v, want %#v", viewed, wantViewed)
	}
	posted := waitWebSocket(t, events, "posted event after second malformed frame run")
	wantPosted := PostedEvent{Message: Message{ID: "post-1", ChannelID: "channel-1", CreatedAt: 1}}
	if !reflect.DeepEqual(posted, wantPosted) {
		t.Fatalf("posted event = %#v, want %#v", posted, wantPosted)
	}
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
}

func TestWebSocketSignalsReadyAfterAuthentication(t *testing.T) {
	authenticated := make(chan struct{})
	sendAck := make(chan struct{})
	sendEvent := make(chan struct{})
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
		close(authenticated)
		<-sendAck
		_ = conn.WriteJSON(map[string]any{"status": "OK", "seq_reply": 1})
		<-sendEvent
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"post-1","channel_id":"channel-1","user_id":"user-1","message":"hello","create_at":10}`}})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan struct{}, 2)
	events := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.RunWebSocket(ctx, func() { ready <- struct{}{} }, func(event Event) { events <- event }, nil)
	}()

	waitWebSocket(t, authenticated, "authentication challenge")
	select {
	case <-ready:
		t.Fatal("ready callback fired before authentication acknowledgement")
	default:
	}
	close(sendAck)
	waitWebSocket(t, ready, "ready callback")
	close(sendEvent)
	waitWebSocket(t, events, "event after ready callback")
	select {
	case <-ready:
		t.Fatal("ready callback fired more than once")
	default:
	}
	cancel()
	if err := waitWebSocket(t, done, "cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	select {
	case <-ready:
		t.Fatal("ready callback fired again during shutdown")
	default:
	}
}

func TestWebSocketRejectsAuthenticationAcknowledgementAndDoesNotDeliverPreAuthEvent(t *testing.T) {
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
		_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{"post": `{"id":"pre-auth","channel_id":"c1"}`}})
		_ = conn.WriteJSON(map[string]any{
			"status": "FAIL", "seq_reply": 1,
			"error": map[string]any{"message": "sentinel-raw-auth-error", "detailed_error": "sentinel-token"},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "sentinel-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ready := atomic.Bool{}
	delivered := atomic.Bool{}
	err = client.RunWebSocket(context.Background(), func() { ready.Store(true) }, func(Event) { delivered.Store(true) }, nil)
	if err == nil || !strings.Contains(err.Error(), "authenticate Mattermost WebSocket") {
		t.Fatalf("error=%v want authentication error", err)
	}
	if strings.Contains(err.Error(), "sentinel-token") || strings.Contains(err.Error(), "sentinel-raw-auth-error") {
		t.Fatalf("authentication error exposed secret or raw response: %v", err)
	}
	if ready.Load() || delivered.Load() {
		t.Fatalf("ready=%v delivered=%v want both false", ready.Load(), delivered.Load())
	}
}

func TestWebSocketCancellationWhileWaitingForAuthenticationAcknowledgement(t *testing.T) {
	challengeRead := make(chan struct{})
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
		close(challengeRead)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
	waitWebSocket(t, challengeRead, "authentication challenge")
	cancel()
	if err := waitWebSocket(t, done, "authentication acknowledgement cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
}

func TestWebSocketDoesNotSignalReadyWhenDialFails(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:0", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	ready := atomic.Bool{}

	err = client.RunWebSocket(context.Background(), func() { ready.Store(true) }, func(Event) {}, nil)
	if err == nil {
		t.Fatal("error = nil, want dial error")
	}
	if ready.Load() {
		t.Fatal("ready callback fired after dial failure")
	}
}

func TestWebSocketDoesNotSignalReadyWhenAuthenticationWriteFails(t *testing.T) {
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
		return &failingAuthConn{Conn: conn}, nil
	}
	client, err := NewClient(server.URL, "test-token", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	ready := atomic.Bool{}

	err = client.RunWebSocket(context.Background(), func() { ready.Store(true) }, func(Event) {}, nil)
	if err == nil || !strings.Contains(err.Error(), "authenticate Mattermost WebSocket") {
		t.Fatalf("error = %v, want authentication error", err)
	}
	if ready.Load() {
		t.Fatal("ready callback fired after authentication failure")
	}
}

func TestWebSocketRejectsNilCallbacks(t *testing.T) {
	client, err := NewClient("https://chat.example.com", "test-token")
	if err != nil {
		t.Fatal(err)
	}

	if err := client.RunWebSocket(context.Background(), nil, func(Event) {}, nil); err == nil || !strings.Contains(err.Error(), "ready callback") {
		t.Fatalf("nil readiness callback error = %v, want validation error", err)
	}
	if err := client.RunWebSocket(context.Background(), func() {}, nil, nil); err == nil || !strings.Contains(err.Error(), "event handler") {
		t.Fatalf("nil event callback error = %v, want validation error", err)
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
		if err := writeWebSocketAuthOK(conn); err != nil {
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
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
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
		if err := writeWebSocketAuthOK(conn); err != nil {
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
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
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
		if err := writeWebSocketAuthOK(conn); err != nil {
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
	err = client.RunWebSocket(context.Background(), func() {}, func(Event) { delivered.Store(true) }, nil)
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

	err = client.RunWebSocket(context.Background(), func() {}, func(Event) {}, nil)
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
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		close(authenticated)
		<-r.Context().Done()
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
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
	waitWebSocket(t, authenticated, "cookie-authenticated WebSocket")
	cancel()
	err = waitWebSocket(t, done, "cookie test cancellation")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled after authenticated cookie request", err)
	}
}

func TestWebSocketUsesHTTP11WithHTTP2CapableTLSTransport(t *testing.T) {
	authenticated := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if err := writeWebSocketAuthOK(conn); err != nil {
			return
		}
		close(authenticated)
		<-r.Context().Done()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	transport := server.Client().Transport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig.Clone()
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	transport.DialTLSContext = (&tls.Dialer{Config: tlsConfig}).DialContext
	client, err := NewClient(server.URL, "test-token", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
	select {
	case <-authenticated:
	case err := <-done:
		t.Fatalf("RunWebSocket returned before TLS authentication: %v", err)
	case <-time.After(webSocketTestTimeout):
		t.Fatal("timed out waiting for TLS WebSocket authentication")
	}
	cancel()
	if err := waitWebSocket(t, done, "TLS WebSocket cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
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
	go func() { done <- client.RunWebSocket(ctx, func() {}, func(Event) {}, nil) }()
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

type failingAuthConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *failingAuthConn) Write(p []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		return c.Conn.Write(p)
	}
	return 0, errors.New("forced authentication write failure")
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

func writeWebSocketAuthOK(conn *websocket.Conn) error {
	return conn.WriteJSON(map[string]any{"status": "OK", "seq_reply": 1})
}
