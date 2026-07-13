package voker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// InternalExtension represents an internal Lambda extension, as documented in
// https://docs.aws.amazon.com/lambda/latest/dg/runtimes-extensions-api.html.
// Internal extensions are not supported on Lambda Managed Instances because
// their invocation lifecycle events cannot represent concurrent invocations.
//
// An OnInit failure is reported through the Runtime API's init/error endpoint
// (the runtime process is failing initialization as a whole), not the
// Extensions API's extension-scoped init/error endpoint, which is intended
// for external extensions.
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
		client:     newExtensionAPIClient(runtimeAPI, len(extensions)),
		done:       make(chan struct{}),
		logger:     logger,
	}
}

func (m *extensionManager) start() error {
	for _, ext := range m.extensions {
		if ext.OnInit != nil {
			if err := callExtensionInit(ext); err != nil {
				return err
			}
		}

		var events []ExtensionEventType
		if ext.OnInvoke != nil {
			events = append(events, ExtensionEventInvoke)
		}

		id, err := m.client.register(ext.Name, events)
		if err != nil {
			return fmt.Errorf("failed to register extension %s: %w", ext.Name, err)
		}

		m.wg.Go(func() { m.eventLoop(ext, id) })
	}
	return nil
}

func callExtensionInit(ext InternalExtension) (responseErr *ErrorResponse) {
	defer func() {
		if recovered := recover(); recovered != nil {
			responseErr = newPanicResponse(recovered)
			responseErr.Message = fmt.Sprintf("extension %s init panicked: %s", ext.Name, responseErr.Message)
		}
	}()

	if err := ext.OnInit(); err != nil {
		original := newErrorResponse(err)
		response := *original
		response.Message = fmt.Sprintf("extension %s init failed: %s", ext.Name, original.Message)
		return &response
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

// callOnInvoke invokes an extension's OnInvoke callback with a context that
// carries the event's deadline. The context is canceled as soon as the
// callback returns so long-lived event loops release each invocation's
// resources immediately.
func callOnInvoke(ext InternalExtension, eventPayload *ExtensionEventPayload) {
	ctx := context.Background()
	if eventPayload.DeadlineMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(eventPayload.DeadlineMs))
		defer cancel()
	}
	ext.OnInvoke(ctx, *eventPayload)
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
			case ExtensionEventInvoke:
				if ext.OnInvoke != nil {
					callOnInvoke(ext, res.eventPayload)
				}
			default:
				// Log unknown event types but continue processing
				m.logger.ErrorContext(ctx, "extension received unknown event type", "extension", ext.Name, "eventType", res.eventPayload.EventType)
			}
		}
	}
}
