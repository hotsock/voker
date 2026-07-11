package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/hotsock/voker/vokercfn"
)

type properties struct {
	ServiceToken string `json:"ServiceToken"`
	Message      string `json:"Message"`
	// Ref returns Number parameters to custom resources as strings.
	Count string `json:"Count"`
}

type data struct {
	Echo        string `json:"Echo"`
	Count       string `json:"Count"`
	RequestType string `json:"RequestType"`
}

func handler(_ context.Context, event vokercfn.Event[properties]) (vokercfn.Result[data], error) {
	eventJSON, _ := json.Marshal(event)
	log.Printf("VOKER_CFN_EVENT %s", eventJSON)

	result := vokercfn.Result[data]{
		PhysicalResourceID: event.PhysicalResourceID,
		Data: data{
			Echo:        event.ResourceProperties.Message,
			Count:       event.ResourceProperties.Count,
			RequestType: string(event.RequestType),
		},
	}
	if result.PhysicalResourceID == "" {
		result.PhysicalResourceID = "voker-cloudformation-observed-resource"
	}

	resultJSON, _ := json.Marshal(result)
	log.Printf("VOKER_CFN_RESULT %s", resultJSON)
	return result, nil
}

func main() {
	vokercfn.Start(handler)
}
