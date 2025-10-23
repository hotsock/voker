package voker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// InternalExtension represents an internal Lambda extension, as documented in
// https://docs.aws.amazon.com/lambda/latest/dg/runtimes-extensions-api.html
type InternalExtension struct {
	// Name is the extension identifier (required).
	Name string

	// OnInit is called during extension initialization (optional).
	OnInit func() error

	// OnInvoke is called for each INVOKE event (optional).
	OnInvoke func(ctx context.Context, eventPayload ExtensionEventPayload)

	// OnSIGTERM is called when SIGTERM signal is received (optional).
	// Internal extensions cannot register for SHUTDOWN events via the Extensions
	// API, but Lambda sends SIGTERM to the runtime process 600ms before
	// SIGKILL. The context will have a deadline of 500ms to be safe.
	OnSIGTERM func(ctx context.Context)
}

const sigtermContextDeadline = 500 * time.Millisecond

type extensionManager struct {
	extensions []InternalExtension
	client     *extensionAPIClient
	done       chan struct{}
	wg         sync.WaitGroup
	logger     *slog.Logger
}

func newExtensionManager(runtimeAPI string, extensions []InternalExtension, logger *slog.Logger) *extensionManager {
	return &extensionManager{
		extensions: extensions,
		client:     newExtensionAPIClient(runtimeAPI),
		done:       make(chan struct{}),
		logger:     logger,
	}
}

func (m *extensionManager) start() error {
	for _, ext := range m.extensions {
		if ext.OnInit != nil {
			if err := ext.OnInit(); err != nil {
				return fmt.Errorf("extension %s init failed: %w", ext.Name, err)
			}
		}

		var events []extensionEventType
		if ext.OnInvoke != nil {
			events = append(events, eventTypeInvoke)
		}

		id, err := m.client.register(ext.Name, events)
		if err != nil {
			return fmt.Errorf("failed to register extension %s: %w", ext.Name, err)
		}

		m.wg.Go(func() { m.eventLoop(ext, id) })
	}
	return nil
}

func (m *extensionManager) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), sigtermContextDeadline)
	defer cancel()

	close(m.done)

	for _, ext := range m.extensions {
		if ext.OnSIGTERM != nil {
			ext.OnSIGTERM(ctx)
		}
	}

	m.wg.Wait()
}

func (m *extensionManager) eventLoop(ext InternalExtension, id string) {
	ctx := context.Background()

	for {
		// Use a channel to make the blocking next() call interruptible
		type result struct {
			eventPayload *ExtensionEventPayload
			err          error
		}
		resultCh := make(chan result, 1)

		go func() {
			event, err := m.client.next(id)
			resultCh <- result{event, err}
		}()

		select {
		case <-m.done:
			// SIGTERM signal received
			return
		case res := <-resultCh:
			if res.err != nil {
				m.logger.ErrorContext(ctx, "extension event loop error", "extension", ext.Name, "error", res.err)
				return
			}

			switch res.eventPayload.EventType {
			case eventTypeInvoke:
				if ext.OnInvoke != nil {
					onInvokeCtx := context.Background()
					if res.eventPayload.DeadlineMs > 0 {
						deadline := time.UnixMilli(res.eventPayload.DeadlineMs)
						var cancel context.CancelFunc
						onInvokeCtx, cancel = context.WithDeadline(onInvokeCtx, deadline)
						defer cancel()
					}
					ext.OnInvoke(onInvokeCtx, *res.eventPayload)
				}
			default:
				// Log unknown event types but continue processing
				m.logger.ErrorContext(ctx, "extension received unknown event type", "extension", ext.Name, "eventType", res.eventPayload.EventType)
			}
		}
	}
}
