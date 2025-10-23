package main

import (
	"context"

	"github.com/hotsock/voker"
)

type Response struct {
	RequestID string `json:"requestId"`
}

func handler(ctx context.Context, _ any) (Response, error) {
	lc, _ := voker.FromContext(ctx)
	return Response{RequestID: lc.AwsRequestID}, nil
}

func main() {
	voker.Start(handler)
}
