package main

import (
	"context"
	"encoding/json/jsontext"

	"github.com/hotsock/voker"
)

type Response struct {
	RequestID string `json:"requestId"`
}

func handler(ctx context.Context, _ jsontext.Value) (Response, error) {
	lc, _ := voker.FromContext(ctx)
	return Response{RequestID: lc.AwsRequestID}, nil
}

func main() {
	voker.Start(handler)
}
