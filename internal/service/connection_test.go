package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/mattermost"
)

var errSocket = errors.New("socket failed")

type scriptedConnectionClient struct {
	attempts []func(context.Context, func(), func(mattermost.Event)) error
	next     int
}

func (c *scriptedConnectionClient) RunWebSocket(ctx context.Context, onReady func(), onEvent func(mattermost.Event), _ func(error)) error {
	if c.next >= len(c.attempts) {
		return errors.New("unexpected socket attempt")
	}
	attempt := c.attempts[c.next]
	c.next++
	return attempt(ctx, onReady, onEvent)
}

func validConnectionManager(client ConnectionClient) ConnectionManager {
	return ConnectionManager{
		Client:    client,
		OnEvent:   func(mattermost.Event) {},
		OnState:   func(mattermost.ConnectionState) {},
		Reconcile: func(context.Context) error { return nil },
		OnError:   func(error) {},
		Wait:      func(context.Context, time.Duration) error { return nil },
		Jitter:    func(delay time.Duration) time.Duration { return delay },
	}
}

func TestConnectionStateFirstConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			cancel()
			return ctx.Err()
		},
	}}
	var states []mattermost.ConnectionState
	reconciles := 0
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
	manager.Reconcile = func(context.Context) error {
		reconciles++
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	wantStates := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateConnected,
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	if reconciles != 0 {
		t.Fatalf("reconciles = %d, want 0", reconciles)
	}
}

func TestConnectionStateForwardsEventsSynchronously(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	event := mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1"}}
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			onEvent(event)
			cancel()
			return ctx.Err()
		},
	}}
	var events []mattermost.Event
	manager := validConnectionManager(client)
	manager.OnEvent = func(event mattermost.Event) { events = append(events, event) }

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(events, []mattermost.Event{event}) {
		t.Fatalf("events = %#v, want %#v", events, []mattermost.Event{event})
	}
}

func TestReconnectRunsReconciliationAfterReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sequence := make([]string, 0, 10)
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			return errSocket
		},
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			if !reflect.DeepEqual(sequence, []string{"connecting", "connected", "offline", "reconnecting", "wait", "connected", "reconcile"}) {
				t.Fatalf("sequence while socket remains active = %v", sequence)
			}
			cancel()
			return ctx.Err()
		},
	}}
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { sequence = append(sequence, state.String()) }
	manager.OnError = func(err error) {
		if !errors.Is(err, errSocket) {
			t.Fatalf("OnError error = %v, want errSocket", err)
		}
	}
	manager.Wait = func(context.Context, time.Duration) error {
		sequence = append(sequence, "wait")
		return nil
	}
	manager.Reconcile = func(context.Context) error {
		sequence = append(sequence, "reconcile")
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestReconnectBackoffIsExponentialAndCapped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	const attemptCount = 8
	attempts := make([]func(context.Context, func(), func(mattermost.Event)) error, attemptCount)
	for i := range attempts {
		attempts[i] = func(context.Context, func(), func(mattermost.Event)) error { return errSocket }
	}
	client := &scriptedConnectionClient{attempts: attempts}
	var waits []time.Duration
	manager := validConnectionManager(client)
	manager.Wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		if len(waits) == attemptCount {
			cancel()
			return context.Canceled
		}
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

func TestRejectedAuthenticationDoesNotResetBackoffOrConnectOrReconcile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	authErr := errors.New("authenticate Mattermost WebSocket: rejected")
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(context.Context, func(), func(mattermost.Event)) error { return authErr },
		func(context.Context, func(), func(mattermost.Event)) error { return authErr },
		func(context.Context, func(), func(mattermost.Event)) error { return authErr },
	}}
	var states []mattermost.ConnectionState
	var waits []time.Duration
	reconciles := 0
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
	manager.Reconcile = func(context.Context) error { reconciles++; return nil }
	manager.Wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		if len(waits) == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v want context.Canceled", err)
	}
	if got, want := waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("waits=%v want %v", got, want)
	}
	for _, state := range states {
		if state == mattermost.ConnectionStateConnected {
			t.Fatalf("rejected authentication emitted connected: %v", states)
		}
	}
	if reconciles != 0 {
		t.Fatalf("reconciles=%d want 0", reconciles)
	}
}

func TestReconnectBackoffBoundsInjectedJitter(t *testing.T) {
	tests := []struct {
		name       string
		jitter     time.Duration
		wantDelay  time.Duration
		wantJitter time.Duration
	}{
		{name: "below zero", jitter: -time.Second, wantDelay: 0, wantJitter: time.Second},
		{name: "above cap", jitter: time.Minute, wantDelay: 30 * time.Second, wantJitter: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
				func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
			}}
			manager := validConnectionManager(client)
			manager.Jitter = func(delay time.Duration) time.Duration {
				if delay != tt.wantJitter {
					t.Fatalf("Jitter input = %v, want %v", delay, tt.wantJitter)
				}
				return tt.jitter
			}
			manager.Wait = func(_ context.Context, delay time.Duration) error {
				if delay != tt.wantDelay {
					t.Fatalf("Wait delay = %v, want %v", delay, tt.wantDelay)
				}
				cancel()
				return context.Canceled
			}

			err := manager.Run(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestReconnectDefaultJitterUsesBoundedRange(t *testing.T) {
	tests := []struct {
		name  string
		delay time.Duration
		draw  func(int64) int64
		want  time.Duration
	}{
		{
			name:  "uncapped lower boundary",
			delay: 16 * time.Second,
			draw:  func(int64) int64 { return 0 },
			want:  12 * time.Second,
		},
		{
			name:  "uncapped upper boundary",
			delay: 16 * time.Second,
			draw:  func(limit int64) int64 { return limit - 1 },
			want:  20 * time.Second,
		},
		{
			name:  "capped lower boundary",
			delay: connectionBackoffCap,
			draw:  func(int64) int64 { return 0 },
			want:  22500 * time.Millisecond,
		},
		{
			name:  "capped middle retains spread",
			delay: connectionBackoffCap,
			draw:  func(limit int64) int64 { return limit / 2 },
			want:  26250 * time.Millisecond,
		},
		{
			name:  "capped upper boundary",
			delay: connectionBackoffCap,
			draw:  func(limit int64) int64 { return limit - 1 },
			want:  connectionBackoffCap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionJitter(tt.delay, tt.draw); got != tt.want {
				t.Fatalf("connectionJitter(%v) = %v, want %v", tt.delay, got, tt.want)
			}
		})
	}
}

func TestReconnectCancellationFromOfflineStopsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
	}}
	var states []mattermost.ConnectionState
	errorsReported := 0
	waits := 0
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) {
		states = append(states, state)
		if state == mattermost.ConnectionStateOffline {
			cancel()
		}
	}
	manager.OnError = func(error) { errorsReported++ }
	manager.Wait = func(context.Context, time.Duration) error {
		waits++
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	wantStates := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateOffline,
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	if errorsReported != 0 || waits != 0 {
		t.Fatalf("errors reported = %d, waits = %d, want 0, 0", errorsReported, waits)
	}
}

func TestReconnectCancellationFromConnectedSkipsReconciliation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(_ context.Context, onReady func(), _ func(mattermost.Event)) error {
			onReady()
			return errSocket
		},
		func(ctx context.Context, onReady func(), _ func(mattermost.Event)) error {
			onReady()
			return ctx.Err()
		},
	}}
	reconciles := 0
	connected := 0
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) {
		if state == mattermost.ConnectionStateConnected {
			connected++
			if connected == 2 {
				cancel()
			}
		}
	}
	manager.Reconcile = func(context.Context) error {
		reconciles++
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if reconciles != 0 {
		t.Fatalf("reconciles = %d, want 0", reconciles)
	}
}

func TestReconnectCancellationFromOnErrorStopsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
	}}
	var states []mattermost.ConnectionState
	errorsReported := 0
	waits := 0
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
	manager.OnError = func(error) {
		errorsReported++
		cancel()
	}
	manager.Wait = func(context.Context, time.Duration) error {
		waits++
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	wantStates := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateOffline,
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	if errorsReported != 1 || waits != 0 {
		t.Fatalf("errors reported = %d, waits = %d, want 1, 0", errorsReported, waits)
	}
}

func TestReconnectCancellationInterruptsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
	}}
	var states []mattermost.ConnectionState
	var reported []error
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
	manager.OnError = func(err error) { reported = append(reported, err) }
	manager.Wait = func(ctx context.Context, delay time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	wantStates := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateOffline,
		mattermost.ConnectionStateReconnecting,
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	if !reflect.DeepEqual(reported, []error{errSocket}) {
		t.Fatalf("reported errors = %v, want [%v]", reported, errSocket)
	}
}

func TestReconnectCancellationDuringSocketExitsSilently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			cancel()
			return ctx.Err()
		},
	}}
	var states []mattermost.ConnectionState
	var reported []error
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
	manager.OnError = func(err error) { reported = append(reported, err) }

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	wantStates := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateConnected,
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	if len(reported) != 0 {
		t.Fatalf("reported errors = %v, want none", reported)
	}
}

func TestReconnectResetsBackoffAfterReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			return errSocket
		},
		func(context.Context, func(), func(mattermost.Event)) error { return errSocket },
	}}
	var waits []time.Duration
	manager := validConnectionManager(client)
	manager.Wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		if len(waits) == 4 {
			cancel()
			return context.Canceled
		}
		return nil
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, time.Second, 2 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

func TestReconciliationFailureDoesNotStopConnectedSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reconcileErr := errors.New("reconcile failed")
	sequence := make([]string, 0, 8)
	client := &scriptedConnectionClient{attempts: []func(context.Context, func(), func(mattermost.Event)) error{
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			return errSocket
		},
		func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			sequence = append(sequence, "socket-still-open")
			cancel()
			return ctx.Err()
		},
	}}
	manager := validConnectionManager(client)
	manager.OnState = func(state mattermost.ConnectionState) { sequence = append(sequence, state.String()) }
	manager.Reconcile = func(context.Context) error {
		sequence = append(sequence, "reconcile")
		return reconcileErr
	}
	manager.OnError = func(err error) {
		if !errors.Is(err, reconcileErr) && !errors.Is(err, errSocket) {
			t.Fatalf("OnError error = %v", err)
		}
		sequence = append(sequence, "error")
	}

	err := manager.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	want := []string{
		"connecting", "connected", "offline", "error", "reconnecting",
		"connected", "reconcile", "error", "socket-still-open",
	}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("sequence = %v, want %v", sequence, want)
	}
}

func TestConnectionManagersFailIndependently(t *testing.T) {
	type result struct {
		name   string
		states []mattermost.ConnectionState
		err    error
	}
	run := func(name string, failures int, results chan<- result) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := make([]func(context.Context, func(), func(mattermost.Event)) error, failures+1)
		for i := range failures {
			attempts[i] = func(context.Context, func(), func(mattermost.Event)) error { return errSocket }
		}
		attempts[failures] = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
			onReady()
			cancel()
			return ctx.Err()
		}
		manager := validConnectionManager(&scriptedConnectionClient{attempts: attempts})
		states := make([]mattermost.ConnectionState, 0, failures*2+2)
		manager.OnState = func(state mattermost.ConnectionState) { states = append(states, state) }
		results <- result{name: name, states: states, err: manager.Run(ctx)}
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup
	for name, failures := range map[string]int{"stable": 0, "retrying": 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(name, failures, results)
		}()
	}
	wg.Wait()
	close(results)

	got := make(map[string]result)
	for item := range results {
		got[item.name] = item
	}
	for name, item := range got {
		if !errors.Is(item.err, context.Canceled) {
			t.Fatalf("%s Run error = %v, want context.Canceled", name, item.err)
		}
	}
	if want := []mattermost.ConnectionState{mattermost.ConnectionStateConnecting, mattermost.ConnectionStateConnected}; !reflect.DeepEqual(got["stable"].states, want) {
		t.Fatalf("stable states = %v, want %v", got["stable"].states, want)
	}
	wantRetrying := []mattermost.ConnectionState{
		mattermost.ConnectionStateConnecting,
		mattermost.ConnectionStateOffline,
		mattermost.ConnectionStateReconnecting,
		mattermost.ConnectionStateOffline,
		mattermost.ConnectionStateReconnecting,
		mattermost.ConnectionStateConnected,
	}
	if !reflect.DeepEqual(got["retrying"].states, wantRetrying) {
		t.Fatalf("retrying states = %v, want %v", got["retrying"].states, wantRetrying)
	}
}

func TestConnectionStateValidatesDependencies(t *testing.T) {
	client := &scriptedConnectionClient{}
	tests := []struct {
		name    string
		manager *ConnectionManager
		want    string
	}{
		{name: "nil manager", manager: nil, want: "manager"},
		{name: "nil context", manager: pointerTo(validConnectionManager(client)), want: "context"},
		{name: "nil client", manager: pointerTo(func() ConnectionManager { m := validConnectionManager(client); m.Client = nil; return m }()), want: "client"},
		{name: "typed nil client", manager: pointerTo(func() ConnectionManager {
			m := validConnectionManager(client)
			var nilClient *scriptedConnectionClient
			m.Client = nilClient
			return m
		}()), want: "client"},
		{name: "nil event callback", manager: pointerTo(func() ConnectionManager { m := validConnectionManager(client); m.OnEvent = nil; return m }()), want: "event"},
		{name: "nil state callback", manager: pointerTo(func() ConnectionManager { m := validConnectionManager(client); m.OnState = nil; return m }()), want: "state"},
		{name: "nil reconcile callback", manager: pointerTo(func() ConnectionManager { m := validConnectionManager(client); m.Reconcile = nil; return m }()), want: "reconcile"},
		{name: "nil error callback", manager: pointerTo(func() ConnectionManager { m := validConnectionManager(client); m.OnError = nil; return m }()), want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.name == "nil context" {
				ctx = nil
			}
			err := tt.manager.Run(ctx)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("Run error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func pointerTo[T any](value T) *T {
	return &value
}
