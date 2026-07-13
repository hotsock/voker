package main

import (
	"context"
	"encoding/json"
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
	RequestURI string              `json:"requestUri"`
	Host       string              `json:"host"`
	RemoteAddr string              `json:"remoteAddr"`
	Proto      string              `json:"proto"`
	Headers    map[string][]string `json:"headers"`
	Cookies    []*http.Cookie      `json:"cookies"`
	Body       string              `json:"body"`
	RequestID  string              `json:"lambdaRequestId"`
}

func logEvent(adapter string, ctx context.Context) {
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

	b, err := json.Marshal(event)
	if err != nil {
		log.Printf("VOKER_EVENT_MARSHAL_ERROR adapter=%s error=%v", adapter, err)
		return
	}
	log.Printf("VOKER_EVENT adapter=%s json=%s", adapter, b)
}

func probeHandler(adapter string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logEvent(adapter, r.Context())

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
		if err := json.NewEncoder(w).Encode(out); err != nil {
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
		vokerhttp.Start(handler, &vokerhttp.ALB{MultiValueHeaders: true})
	case "apigwv1":
		if streaming {
			vokerhttp.StartStreaming(handler, &vokerhttp.APIGatewayV1{})
		} else {
			vokerhttp.Start(handler, &vokerhttp.APIGatewayV1{})
		}
	case "apigwv2":
		vokerhttp.Start(handler, &vokerhttp.APIGatewayV2{})
	case "functionurl":
		if streaming {
			vokerhttp.StartStreaming(handler, &vokerhttp.FunctionURL{})
		} else {
			vokerhttp.Start(handler, &vokerhttp.FunctionURL{})
		}
	default:
		panic(fmt.Sprintf("unknown VOKER_ADAPTER %q", adapter))
	}
}
