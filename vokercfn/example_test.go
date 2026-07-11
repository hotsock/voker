package vokercfn_test

import (
	"context"
	"fmt"

	"github.com/hotsock/voker/vokercfn"
)

type exampleProperties struct {
	Name string `json:"Name"`
}

type exampleData struct {
	ARN string `json:"Arn"`
}

func ExampleWrap() {
	handler := func(_ context.Context, event vokercfn.Event[exampleProperties]) (vokercfn.Result[exampleData], error) {
		switch event.RequestType {
		case vokercfn.RequestCreate:
			return vokercfn.Result[exampleData]{
				PhysicalResourceID: event.ResourceProperties.Name,
				Data:               exampleData{ARN: "arn:example:" + event.ResourceProperties.Name},
			}, nil
		case vokercfn.RequestUpdate, vokercfn.RequestDelete:
			return vokercfn.Result[exampleData]{PhysicalResourceID: event.PhysicalResourceID}, nil
		default:
			return vokercfn.Result[exampleData]{}, fmt.Errorf("unknown request type %q", event.RequestType)
		}
	}

	// Pass the result to voker.Start when composing your own entrypoint. Most
	// applications can simply call vokercfn.Start(handler).
	_ = vokercfn.Wrap(handler)
}
