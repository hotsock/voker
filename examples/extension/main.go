package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/hotsock/voker"
)

type Response struct {
	RequestID string `json:"requestId"`
}

func handler(ctx context.Context, _ any) (Response, error) {
	log.Printf("Handler invoked")

	lc, _ := voker.FromContext(ctx)
	return Response{RequestID: lc.AwsRequestID}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	var invocationCount int

	voker.Start(handler, voker.WithInternalExtension(voker.InternalExtension{
		Name: "Extension.Example",

		OnInit: func() error {
			log.Println("[Extension] OnInit: Extension initializing...")
			log.Println("[Extension] OnInit: Setting up connections and resources")
			return nil
		},

		OnInvoke: func(ctx context.Context, event voker.ExtensionEventPayload) {
			log.Println(event)
			invocationCount++
			log.Printf("[Extension] OnInvoke: Function invocation #%d detected", invocationCount)

			if deadline, ok := ctx.Deadline(); ok {
				remaining := time.Until(deadline)
				log.Printf("[Extension] OnInvoke: Time remaining: %v", remaining)
			}
		},

		OnSIGTERM: func(ctx context.Context) {
			log.Printf("[Extension] OnSIGTERM: Total invocations processed: %d", invocationCount)

			if deadline, ok := ctx.Deadline(); ok {
				remaining := time.Until(deadline)
				log.Printf("[Extension] OnSIGTERM: Time to cleanup: %v", remaining)
			}
		},
	}))
}
