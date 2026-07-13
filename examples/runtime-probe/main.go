package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/hotsock/voker"
	"github.com/hotsock/voker/vokerhttp"
)

const streamingContentType = "application/vnd.awslambda.http-integration-response"

type request struct {
	Action  string `json:"action"`
	RawPath string `json:"rawPath"`
}

type response struct {
	Action    string `json:"action"`
	RequestID string `json:"requestId"`
}

type probeStream struct {
	reader    io.Reader
	requestID string
}

func (s *probeStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *probeStream) Close() error {
	fmt.Fprintf(os.Stderr, "VOKER_PROBE closer_closed request_id=%s\n", s.requestID)
	if closer, ok := s.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (*probeStream) ContentType() string { return streamingContentType }

type trailingErrorReader struct {
	payload []byte
	err     error
}

func (r *trailingErrorReader) Read(p []byte) (int, error) {
	if r.payload == nil {
		return 0, r.err
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		r.payload = nil
		return n, r.err
	}
	return n, nil
}

type trailingPanicReader struct {
	payload []byte
}

func (r *trailingPanicReader) Read(p []byte) (int, error) {
	if r.payload == nil {
		panic("probe stream panic")
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		r.payload = nil
	}
	return n, nil
}

func streamPayload(body string) []byte {
	metadata, err := json.Marshal(vokerhttp.StreamingResponseMetadata{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "text/plain; charset=utf-8"},
	})
	if err != nil {
		panic(err)
	}
	payload := append(metadata, make([]byte, 8)...)
	return append(payload, body...)
}

func handler(ctx context.Context, raw json.RawMessage) (any, error) {
	var event request
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	action := event.Action
	if action == "" {
		switch event.RawPath {
		case "/buffered":
			action = "function-url-buffered"
		case "/stream":
			action = "stream"
		case "/stream-error":
			action = "stream-error"
		case "/stream-panic":
			action = "stream-panic"
		}
	}

	requestID := ""
	if lc, ok := voker.FromContext(ctx); ok {
		requestID = lc.AwsRequestID
	}

	switch action {
	case "echo-context":
		lc, ok := voker.FromContext(ctx)
		if !ok {
			return nil, errors.New("missing Lambda context")
		}
		return map[string]any{
			"requestId":          lc.AwsRequestID,
			"invokedFunctionArn": lc.InvokedFunctionArn,
			"traceId":            lc.TraceID,
			"tenantId":           lc.TenantID,
			"identity":           lc.Identity,
			"clientContext":      lc.ClientContext,
			"maxConcurrency":     voker.MaxConcurrency(),
		}, nil
	case "custom-error":
		return nil, &voker.ErrorResponse{
			Type:    "Application.CustomError",
			Message: "custom probe error",
			StackTrace: []voker.StackFrame{{
				Path:  "examples/runtime-probe/main.go",
				Line:  1,
				Label: "handler",
			}},
		}
	case "function-url-buffered":
		return vokerhttp.FunctionURLResponse{
			StatusCode: 200,
			Headers:    map[string]string{"content-type": "text/plain; charset=utf-8"},
			Body:       "buffered response",
		}, nil
	case "stream":
		return &probeStream{reader: bytes.NewReader(streamPayload("streamed response")), requestID: requestID}, nil
	case "stream-error":
		return &probeStream{
			reader:    &trailingErrorReader{payload: streamPayload("partial response"), err: errors.New("probe stream error")},
			requestID: requestID,
		}, nil
	case "stream-panic":
		return &probeStream{
			reader:    &trailingPanicReader{payload: streamPayload("partial panic response")},
			requestID: requestID,
		}, nil
	default:
		return response{Action: action, RequestID: requestID}, nil
	}
}

// rawHeaderLoop is a minimal hand-rolled Runtime API loop that echoes every
// Lambda-Runtime-* header verbatim. It captures ground-truth Runtime API
// payloads (Cognito identity, client context, tenant ID) for voker's test
// fixtures without any voker parsing in between.
func rawHeaderLoop() {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	client := &http.Client{}

	for {
		resp, err := client.Get("http://" + api + "/2018-06-01/runtime/invocation/next")
		if err != nil {
			log.Fatalf("next: %v", err)
		}

		requestID := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
		headers := map[string][]string{}
		for name, values := range resp.Header {
			if strings.HasPrefix(strings.ToLower(name), "lambda-runtime-") {
				headers[name] = values
			}
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Fatalf("read payload: %v", err)
		}

		response, err := json.Marshal(map[string]any{
			"headers": headers,
			"payload": string(payload),
		})
		if err != nil {
			log.Fatalf("marshal response: %v", err)
		}
		fmt.Fprintf(os.Stderr, "VOKER_PROBE raw_headers %s\n", response)

		postResp, err := client.Post(
			"http://"+api+"/2018-06-01/runtime/invocation/"+requestID+"/response",
			"application/json",
			bytes.NewReader(response),
		)
		if err != nil {
			log.Fatalf("respond: %v", err)
		}
		_, _ = io.Copy(io.Discard, postResp.Body)
		postResp.Body.Close()
	}
}

func main() {
	var options []voker.Option
	switch os.Getenv("VOKER_PROBE_MODE") {
	case "raw-headers":
		rawHeaderLoop()
		return
	case "init-error":
		options = append(options, voker.WithInternalExtension(voker.InternalExtension{
			Name: "InitErrorProbe",
			OnInit: func() error {
				return &voker.ErrorResponse{Type: "Extension.InitError", Message: "probe init error"}
			},
		}))
	case "init-panic":
		options = append(options, voker.WithInternalExtension(voker.InternalExtension{
			Name: "InitPanicProbe",
			OnInit: func() error {
				panic("probe init panic")
			},
		}))
	case "register-error":
		options = append(options, voker.WithInternalExtension(voker.InternalExtension{Name: ""}))
	}

	voker.Start(handler, options...)
}
