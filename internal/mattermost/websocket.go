package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	webSocketHandshakeTimeout              = 30 * time.Second
	webSocketAuthWriteTimeout              = 10 * time.Second
	maxConsecutiveMalformedWebSocketFrames = 8
	// Realtime posts contain the same message data bounded REST responses carry.
	maxWebSocketMessageBytes = maxSuccessBodyBytes
)

type webSocketAuthChallenge struct {
	Seq    int               `json:"seq"`
	Action string            `json:"action"`
	Data   webSocketAuthData `json:"data"`
}

type webSocketAuthData struct {
	Token string `json:"token"`
}

type webSocketResponseEnvelope struct {
	Status   string          `json:"status"`
	SeqReply int             `json:"seq_reply"`
	Error    json.RawMessage `json:"error"`
}

// RunWebSocket connects to Mattermost and delivers realtime events until the
// context is canceled or the connection fails. The caller owns reconnection.
// Callbacks run synchronously on the read goroutine, preserve server event
// ordering, and must return promptly.
func (c *Client) RunWebSocket(ctx context.Context, onReady func(), handle func(Event), diagnostic func(error)) error {
	if onReady == nil {
		return errors.New("Mattermost WebSocket ready callback must not be nil")
	}
	if handle == nil {
		return errors.New("Mattermost WebSocket event handler must not be nil")
	}
	if diagnostic == nil {
		diagnostic = func(error) {}
	}

	endpoint := c.webSocketURL()
	dialer, err := c.webSocketDialer()
	if err != nil {
		return err
	}
	conn, response, err := dialer.DialContext(ctx, endpoint.String(), nil)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if response != nil {
			return fmt.Errorf("connect Mattermost WebSocket: HTTP %s", response.Status)
		}
		return fmt.Errorf("connect Mattermost WebSocket: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(maxWebSocketMessageBytes)

	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-finished:
		}
	}()

	var writeMu sync.Mutex
	writeControl := func(messageType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteControl(messageType, data, time.Now().Add(webSocketHandshakeTimeout))
	}
	conn.SetPingHandler(func(data string) error {
		return writeControl(websocket.PongMessage, []byte(data))
	})

	writeMu.Lock()
	err = conn.SetWriteDeadline(time.Now().Add(webSocketAuthWriteTimeout))
	if err == nil {
		err = conn.WriteJSON(webSocketAuthChallenge{
			Seq:    1,
			Action: "authentication_challenge",
			Data:   webSocketAuthData{Token: c.token},
		})
	}
	writeMu.Unlock()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("authenticate Mattermost WebSocket: %w", err)
	}
	if err := waitForWebSocketAuthentication(conn, 1); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	onReady()

	consecutiveMalformed := 0
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read Mattermost WebSocket: %w", err)
		}
		event, err := decodeWebSocketEvent(message)
		if err != nil {
			consecutiveMalformed++
			diagnostic(errors.New("Mattermost WebSocket application frame was malformed"))
			if consecutiveMalformed >= maxConsecutiveMalformedWebSocketFrames {
				return errors.New("Mattermost WebSocket malformed frame budget exhausted")
			}
			continue
		}
		consecutiveMalformed = 0
		if event != nil {
			handle(event)
		}
	}
}

func waitForWebSocketAuthentication(conn *websocket.Conn, challengeSeq int) error {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("authenticate Mattermost WebSocket: read acknowledgement: %w", err)
		}
		var response webSocketResponseEnvelope
		if err := json.Unmarshal(message, &response); err != nil {
			return errors.New("authenticate Mattermost WebSocket: malformed acknowledgement")
		}
		if response.Status == "" {
			// Application events are not trustworthy until authentication is
			// acknowledged, so discard them rather than exposing pre-auth data.
			continue
		}
		if response.SeqReply != challengeSeq {
			continue
		}
		if response.Status != "OK" || len(response.Error) != 0 && string(response.Error) != "null" {
			return errors.New("authenticate Mattermost WebSocket: server rejected authentication")
		}
		return nil
	}
}

func (c *Client) webSocketURL() *url.URL {
	endpoint := *c.baseURL
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path += "/websocket"
	endpoint.RawPath = ""
	return &endpoint
}

func (c *Client) webSocketDialer() (*websocket.Dialer, error) {
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = webSocketHandshakeTimeout
	dialer.Jar = c.httpClient.Jar

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if c.httpClient.Transport == nil {
		transport = http.DefaultTransport.(*http.Transport)
		ok = true
	}
	if !ok {
		return nil, fmt.Errorf("configure Mattermost WebSocket: HTTP transport %T is not supported", c.httpClient.Transport)
	}
	dialer.Proxy = transport.Proxy
	dialer.NetDialContext = transport.DialContext
	if transport.TLSClientConfig != nil {
		dialer.TLSClientConfig = transport.TLSClientConfig.Clone()
		dialer.TLSClientConfig.NextProtos = nil
	}
	return &dialer, nil
}
