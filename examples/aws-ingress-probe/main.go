package main

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hotsock/voker"
	"github.com/hotsock/voker/vokerhttp"
)

type echoResponse struct {
	Adapter    string              `json:"adapter"`
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Path       string              `json:"path"`
	RawPath    string              `json:"rawPath"`
	RawQuery   string              `json:"rawQuery"`
	Event      any                 `json:"event"`
	RequestURI string              `json:"requestUri"`
	Host       string              `json:"host"`
	RemoteAddr string              `json:"remoteAddr"`
	Proto      string              `json:"proto"`
	Headers    map[string][]string `json:"headers"`
	Cookies    []*http.Cookie      `json:"cookies"`
	Body       string              `json:"body"`
	RequestID  string              `json:"lambdaRequestId"`
}

func requestEvent(adapter string, ctx context.Context) any {
	var event any
	switch adapter {
	case "alb":
		event, _ = vokerhttp.EventFromContext[vokerhttp.ALBRequest](ctx)
	case "apigwv1":
		event, _ = vokerhttp.EventFromContext[vokerhttp.APIGatewayV1Request](ctx)
	case "apigwv2":
		event, _ = vokerhttp.EventFromContext[vokerhttp.APIGatewayV2Request](ctx)
	case "functionurl":
		event, _ = vokerhttp.EventFromContext[vokerhttp.FunctionURLRequest](ctx)
	}

	return event
}

// Capture events before conversion, including those the adapter cannot parse.
type capturingAdapter[E, R any] struct {
	vokerhttp.Adapter[E, R]
}

func (a capturingAdapter[E, R]) Request(ctx context.Context, event E) (*http.Request, error) {
	b, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	log.Printf("VOKER_EVENT json=%s", b)
	return a.Adapter.Request(ctx, event)
}

func (a capturingAdapter[E, R]) StreamingResponseMetadata(status int, header http.Header) vokerhttp.StreamingResponseMetadata {
	return a.Adapter.(interface {
		StreamingResponseMetadata(int, http.Header) vokerhttp.StreamingResponseMetadata
	}).StreamingResponseMetadata(status, header)
}

func probeHandler(adapter string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Add("X-Voker-Value", "one")
		w.Header().Add("X-Voker-Value", "two")
		http.SetCookie(w, &http.Cookie{Name: "voker_a", Value: "one", Path: "/", HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "voker_b", Value: "two", Path: "/", SameSite: http.SameSiteLaxMode})

		switch r.URL.Path {
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", http.StatusInternalServerError)
				return
			}
			for _, chunk := range []string{"data: first\n\n", "data: second\n\n", "data: third\n\n"} {
				_, _ = io.WriteString(w, chunk)
				flusher.Flush()
				time.Sleep(750 * time.Millisecond)
			}
			return
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0xfe, 0xff})
			return
		case "/status":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("short and stout"))
			return
		}

		requestID := ""
		if lc, ok := voker.FromContext(r.Context()); ok {
			requestID = lc.AwsRequestID
		}
		out := echoResponse{
			Adapter:    adapter,
			Method:     r.Method,
			URL:        r.URL.String(),
			Path:       r.URL.Path,
			RawPath:    r.URL.RawPath,
			RawQuery:   r.URL.RawQuery,
			Event:      requestEvent(adapter, r.Context()),
			RequestURI: r.RequestURI,
			Host:       r.Host,
			RemoteAddr: r.RemoteAddr,
			Proto:      r.Proto,
			Headers:    r.Header,
			Cookies:    r.Cookies(),
			Body:       string(body),
			RequestID:  requestID,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.MarshalWrite(w, out); err != nil {
			log.Printf("response encode error: %v", err)
		}
	})
}

func main() {
	adapter := os.Getenv("VOKER_ADAPTER")
	streaming := os.Getenv("VOKER_STREAMING") == "true"
	handler := probeHandler(adapter)

	switch adapter {
	case "alb":
		vokerhttp.Start(handler, capturingAdapter[vokerhttp.ALBRequest, vokerhttp.ALBResponse]{&vokerhttp.ALB{MultiValueHeaders: true}})
	case "apigwv1":
		if streaming {
			vokerhttp.StartStreaming(handler, capturingAdapter[vokerhttp.APIGatewayV1Request, vokerhttp.APIGatewayV1Response]{&vokerhttp.APIGatewayV1{}})
		} else {
			vokerhttp.Start(handler, capturingAdapter[vokerhttp.APIGatewayV1Request, vokerhttp.APIGatewayV1Response]{&vokerhttp.APIGatewayV1{}})
		}
	case "apigwv2":
		vokerhttp.Start(handler, capturingAdapter[vokerhttp.APIGatewayV2Request, vokerhttp.APIGatewayV2Response]{&vokerhttp.APIGatewayV2{}})
	case "functionurl":
		if streaming {
			vokerhttp.StartStreaming(handler, capturingAdapter[vokerhttp.FunctionURLRequest, vokerhttp.FunctionURLResponse]{&vokerhttp.FunctionURL{}})
		} else {
			vokerhttp.Start(handler, capturingAdapter[vokerhttp.FunctionURLRequest, vokerhttp.FunctionURLResponse]{&vokerhttp.FunctionURL{}})
		}
	default:
		panic(fmt.Sprintf("unknown VOKER_ADAPTER %q", adapter))
	}
}
