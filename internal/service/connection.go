package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/nosovk/mmk/internal/mattermost"
)

const (
	connectionBackoffBase = 1 * time.Second
	connectionBackoffCap  = 30 * time.Second
)

type ConnectionClient interface {
	RunWebSocket(context.Context, func(), func(mattermost.Event), func(error)) error
}

type ConnectionManager struct {
	Client    ConnectionClient
	OnEvent   func(mattermost.Event)
	OnState   func(mattermost.ConnectionState)
	Reconcile func(context.Context) error
	OnError   func(error)
	Wait      func(context.Context, time.Duration) error
	Jitter    func(time.Duration) time.Duration
}

func (m *ConnectionManager) Run(ctx context.Context) error {
	if err := m.validate(ctx); err != nil {
		return err
	}
	wait := m.Wait
	if wait == nil {
		wait = defaultConnectionWait
	}
	jitter := m.Jitter
	if jitter == nil {
		jitter = defaultConnectionJitter
	}

	m.OnState(mattermost.ConnectionStateConnecting)
	hasConnected := false
	failedAttempts := 0
	for {
		err := m.Client.RunWebSocket(ctx, func() {
			if ctx.Err() != nil {
				return
			}
			recovered := hasConnected
			hasConnected = true
			failedAttempts = 0
			if recovered {
				if err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
					m.OnError(err)
				}
			}
			if ctx.Err() == nil {
				m.OnState(mattermost.ConnectionStateConnected)
			}
		}, m.OnEvent, m.OnError)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err == nil {
			err = errors.New("Mattermost WebSocket stopped without an error")
		}

		m.OnState(mattermost.ConnectionStateOffline)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		m.OnError(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		m.OnState(mattermost.ConnectionStateReconnecting)

		delay := boundedConnectionDelay(jitter(connectionBackoff(failedAttempts)))
		if failedAttempts < 6 {
			failedAttempts++
		}
		if err := wait(ctx, delay); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("wait to reconnect Mattermost WebSocket: %w", err)
		}
	}
}

func (m *ConnectionManager) validate(ctx context.Context) error {
	if m == nil {
		return errors.New("Mattermost connection manager must not be nil")
	}
	if ctx == nil {
		return errors.New("Mattermost connection context must not be nil")
	}
	if isNilInterface(m.Client) {
		return errors.New("Mattermost connection client must not be nil")
	}
	if m.OnEvent == nil {
		return errors.New("Mattermost connection event callback must not be nil")
	}
	if m.OnState == nil {
		return errors.New("Mattermost connection state callback must not be nil")
	}
	if m.Reconcile == nil {
		return errors.New("Mattermost connection reconcile callback must not be nil")
	}
	if m.OnError == nil {
		return errors.New("Mattermost connection error callback must not be nil")
	}
	return nil
}

func connectionBackoff(failedAttempts int) time.Duration {
	delay := connectionBackoffBase
	for range failedAttempts {
		if delay >= connectionBackoffCap/2 {
			return connectionBackoffCap
		}
		delay *= 2
	}
	return delay
}

func boundedConnectionDelay(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	if delay > connectionBackoffCap {
		return connectionBackoffCap
	}
	return delay
}

func defaultConnectionWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultConnectionJitter(delay time.Duration) time.Duration {
	return connectionJitter(delay, rand.Int64N)
}

func connectionJitter(delay time.Duration, draw func(int64) int64) time.Duration {
	delay = boundedConnectionDelay(delay)
	spread := delay / 4
	if spread == 0 {
		return delay
	}
	lower := delay - spread
	upper := min(delay+spread, connectionBackoffCap)
	return lower + time.Duration(draw(int64(upper-lower)+1))
}
