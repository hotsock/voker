# Voker

A minimal, modern AWS Lambda runtime for Go that focuses on performance, simplicity, and type safety.

## Overview

Voker is a simplified alternative to [`aws-lambda-go`](https://github.com/aws/aws-lambda-go) that maintains full compatibility with the AWS Lambda Runtime API. It uses Go generics to provide compile-time type safety with a clean, single-function-signature design. It supports structured logging with `slog` and proper log levels for errors, including an optional `vokerslog` handler tuned for AWS Lambda.

## Installation

```bash
go get github.com/hotsock/voker
```

## Usage

### Basic Handler

```go
package main

import (
    "context"
    "github.com/hotsock/voker"
)

type MyEvent struct {
    Name string `json:"name"`
}

type MyResponse struct {
    Message string `json:"message"`
}

func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    return MyResponse{
        Message: "Hello, " + event.Name,
    }, nil
}

func main() {
    voker.Start(handler)
}
```

### Accessing Lambda Context

```go
func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    lc, ok := voker.FromContext(ctx)
    if ok {
        log.Printf("Request ID: %s", lc.AwsRequestID)
        log.Printf("Function ARN: %s", lc.InvokedFunctionArn)
        log.Printf("X-Ray trace ID: %s", lc.TraceID)
    }

    deadline, _ := ctx.Deadline()
    log.Printf("Function deadline: %s", deadline)

    return MyResponse{Message: "success"}, nil
}
```

### Error Handling

```go
func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    if event.Name == "" {
        return MyResponse{}, fmt.Errorf("name is required")
    }

    return MyResponse{Message: "Hello, " + event.Name}, nil
}
```

Return a `*voker.ErrorResponse` when the function error needs a stable,
application-specific error type. Voker preserves its `errorType`,
`errorMessage`, and optional stack trace in the Runtime API error payload:

```go
return MyResponse{}, &voker.ErrorResponse{
    Type:    "Application.ValidationError",
    Message: "name is required",
}
```

### Simple Event Types

```go
// Handle API Gateway events
func handler(ctx context.Context, event map[string]any) (map[string]any, error) {
    return map[string]any{
        "statusCode": 200,
        "body":       "Hello from Lambda!",
    }, nil
}

// Handle SQS events
type SQSEvent struct {
    Records []SQSRecord `json:"Records"`
}

type SQSRecord struct {
    Body string `json:"body"`
}

func handler(ctx context.Context, event SQSEvent) (string, error) {
    for _, record := range event.Records {
        log.Printf("Processing: %s", record.Body)
    }
    return "ok", nil
}
```

## Handler Signature

Voker supports **only one handler signature**:

```go
func(context.Context, TIn) (TOut, error)
```

Where:

- `context.Context` is required (provides deadline, cancellation, Lambda metadata)
- `TIn` is your input type (must be JSON-deserializable)
- `TOut` is your output type (JSON-serializable, or an `io.Reader` for streaming)
- `error` is required for error handling

### Lambda Managed Instances

Voker automatically supports Lambda Managed Instances. At startup it reads
`AWS_LAMBDA_MAX_CONCURRENCY` and starts that many Runtime API workers; when the
variable is missing or invalid it preserves standard Lambda's serial behavior.
`voker.MaxConcurrency()` returns the effective worker count.

Managed Instances can call the same handler concurrently within one process.
Handlers must protect mutable globals and shared caches, coordinate access to
shared `/tmp` paths, and use clients that permit concurrent calls. Each handler
receives an independent context, deadline, request ID, and `LambdaContext.TraceID`.
Handlers use `LambdaContext.TraceID` in both standard Lambda and Managed
Instances, keeping trace propagation invocation-scoped.

Lambda does not forcibly stop timed-out Managed Instances handlers. Watch
`ctx.Done()` and leave enough deadline margin to stop the next unit of work
before performing further side effects. Managed Instances also require JSON
logging; Voker's default logger honors Lambda's `AWS_LAMBDA_LOG_FORMAT=JSON`
setting. A logger supplied through `WithLogger` must likewise emit JSON.

Internal extensions are rejected during Managed Instances initialization
because the Extensions API does not support invocation events in that compute
mode. Use the Telemetry API's platform events when invocation reporting is
needed.

See [`examples/managed-instances`](examples/managed-instances/README.md) for a
self-contained SAM stack and live concurrency probe.

### Raw payloads

Declare `TIn` as `json.RawMessage` to receive the invocation payload verbatim.
Voker skips unmarshaling — and JSON validation — and hands the raw bytes
straight to your handler, which is then responsible for decoding them:

```go
func handler(ctx context.Context, payload json.RawMessage) (Response, error) {
    // payload is the raw request bytes, aliased (not copied) from the
    // invocation buffer. Decode it yourself however you like.
    var event MyEvent
    if err := json.Unmarshal(payload, &event); err != nil {
        return Response{}, err
    }
    // ...
}
```

This is useful for handlers that work with large payloads and want to measure
or control their own decoding rather than paying for an unmarshal up front.
Because validation is skipped, the handler also sees empty or malformed
payloads as-is instead of voker rejecting them.

### Response streaming

Return an `io.Reader` to stream bytes through the Lambda Runtime API instead of
JSON-encoding the response. If the returned value also implements
`ContentType() string`, voker propagates that content type; otherwise it uses
`application/octet-stream`. If it also implements `io.Closer`, voker closes it
after the Runtime API finishes consuming the response, including when the
stream fails.

```go
func handler(ctx context.Context, event MyEvent) (io.Reader, error) {
    reader, writer := io.Pipe()
    go func() {
        defer writer.Close()
        _, _ = io.WriteString(writer, "first\n")
        time.Sleep(time.Second)
        _, _ = io.WriteString(writer, "second\n")
    }()
    return reader, nil
}
```

For `net/http` handlers, use `vokerhttp.StartHTTPStreaming`. Its response
writer implements `http.Flusher` and preserves HTTP status, headers, repeated
headers, and cookies in Lambda's streaming metadata prelude.

```go
vokerhttp.StartHTTPStreaming(mux, &vokerhttp.FunctionURL{})
vokerhttp.StartHTTPStreaming(mux, &vokerhttp.APIGatewayV1{})
```

| Ingress                   | Buffered |                            Streaming |
| ------------------------- | -------: | -----------------------------------: |
| Lambda Function URL       |      Yes |              Yes (`RESPONSE_STREAM`) |
| API Gateway v1 REST API   |      Yes | Yes (`ResponseTransferMode: STREAM`) |
| API Gateway v2 HTTP API   |      Yes |                                   No |
| Application Load Balancer |      Yes |                                   No |

Streaming REST integrations must also use API Gateway's
`response-streaming-invocations` integration URI. See the complete deployable
matrix in [`examples/aws-ingress-probe`](examples/aws-ingress-probe/README.md).
For live Runtime API regression coverage—including buffered/streaming mode
selection, stream errors and cleanup, custom error payloads, and initialization
failure reporting—see [`examples/runtime-probe`](examples/runtime-probe/README.md).

### CloudFormation custom resources

Use `vokercfn.Start` to run a type-safe CloudFormation custom resource. It
handles the presigned response URL protocol, reports handler errors as
CloudFormation failures, and supplies safe physical resource ID fallbacks.

```go
package main

import (
    "context"
    "fmt"

    "github.com/hotsock/voker/vokercfn"
)

type Properties struct {
    Name string `json:"Name"`
}

type Data struct {
    ARN string `json:"Arn"`
}

func handler(ctx context.Context, event vokercfn.Event[Properties]) (vokercfn.Result[Data], error) {
    switch event.RequestType {
    case vokercfn.RequestCreate:
        return vokercfn.Result[Data]{
            PhysicalResourceID: "thing-123",
            Data: Data{ARN: "arn:example:thing-123"},
        }, nil
    case vokercfn.RequestUpdate, vokercfn.RequestDelete:
        return vokercfn.Result[Data]{
            PhysicalResourceID: event.PhysicalResourceID,
        }, nil
    default:
        return vokercfn.Result[Data]{}, fmt.Errorf("unknown request type %q", event.RequestType)
    }
}

func main() {
    vokercfn.Start(handler)
}
```

`Result.Data` values are available through `Fn::GetAtt`. Set `Result.NoEcho`
to mask them in CloudFormation responses. Returning a different physical
resource ID from an update tells CloudFormation the resource was replaced.
Responses that cannot be encoded or exceed CloudFormation's 4096-byte limit
are converted to compact `FAILED` responses so stack operations do not wait
for a timeout. See [`examples/cloudformation`](examples/cloudformation) for a
deployable example validated against Create, Update, and Delete events in AWS.

## Lambda Context

The `LambdaContext` type contains metadata about the invocation:

```go
type LambdaContext struct {
    AwsRequestID       string          // Unique request ID
    InvokedFunctionArn string          // ARN of the invoked function
    Identity           CognitoIdentity // Cognito identity (if present)
    ClientContext      ClientContext   // Client context (if present)
}
```

Access it using `voker.FromContext(ctx)`.

## Logging

Voker logs with the standard library's `log/slog`. By default it creates a logger
from `AWS_LAMBDA_LOG_FORMAT` and `AWS_LAMBDA_LOG_LEVEL` using slog's built-in JSON
or text handlers. Provide your own with `voker.WithLogger`.

For ideal Lambda logging behavior, the optional `vokerslog` subpackage offers a
`slog.Handler` tuned for [AWS Lambda advanced logging controls](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-cloudwatchlogs-advanced.html).
It is opt-in (import it only when you want it) and adds no extra dependency — the
request ID is read from `voker.FromContext` rather than `aws-lambda-go`.

```go
import (
    "log/slog"
    "os"

    "github.com/hotsock/voker"
    "github.com/hotsock/voker/vokerslog"
)

func main() {
    logger := slog.New(vokerslog.NewHandler(os.Stdout))
    slog.SetDefault(logger)

    voker.Start(handler, voker.WithLogger(logger))
}
```

`NewHandler` auto-configures format (JSON or text) and level from
`AWS_LAMBDA_LOG_FORMAT` and `AWS_LAMBDA_LOG_LEVEL`, and enriches every record with
Lambda metadata (function name, version, and the request ID from the invocation
context). Options override the environment values:

| Option                    | Description                                    |
| ------------------------- | ---------------------------------------------- |
| `WithJSON()`              | Output in JSON format                          |
| `WithText()`              | Output in text format                          |
| `WithLevel(slog.Leveler)` | Set the minimum log level                      |
| `WithSource()`            | Include source file, function, and line number |
| `WithType(string)`        | Set the `type` field (default: `"app.log"`)    |
| `WithoutTime()`           | Omit the timestamp                             |

In addition to the standard slog levels, the handler maps Lambda's `TRACE`
(`slog.LevelDebug - 4`) and `FATAL` (`slog.LevelError + 4`) levels. A JSON record
looks like:

```json
{
  "level": "INFO",
  "msg": "Lambda Invoked",
  "record": {
    "functionName": "my-func",
    "version": "$LATEST",
    "requestId": "abc-123"
  },
  "type": "app.log"
}
```

### The `type` field

The `type` + `record` envelope mirrors the shape of [AWS Lambda Telemetry API
events](https://docs.aws.amazon.com/lambda/latest/dg/telemetry-schema-reference.html),
so `type` works best as a low-cardinality category for filtering and routing
logs (e.g. `filter type = "app.request"` in CloudWatch Logs Insights) rather
than for per-request data. AWS reserves `function`, `extension`, and
`platform.*`; a dotted `app.<category>` namespace avoids collisions and matches
AWS's style — for example `app.log` (the default), `app.request`, or
`app.audit`.

The default comes from `WithType` (or `"app.log"`). A single record can override
it with a top-level string attribute keyed `vokerslog.TypeKey` (`"type"`); the
attribute sets the record's type instead of being emitted normally. Set it via
`With` to tag every record from a logger, or per call:

```go
// All records from this logger are tagged "app.request".
requests := slog.New(handler).With(vokerslog.TypeKey, "app.request")

// Or override a single record (this wins over any With value):
slog.InfoContext(ctx, "audit event", vokerslog.TypeKey, "app.audit")
```

Setting the type to `""` omits the field for that record. An attribute keyed
`type` inside a group is left untouched and emitted normally.

## Error Handling

Voker automatically handles errors and panics:

### Regular Errors

```go
func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    return MyResponse{}, errors.New("something went wrong")
}
// Returns: {"errorMessage":"something went wrong","errorType":"errorString"}
```

### Panics

```go
func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
	a := []string{"hey"}
	fmt.Println(a[1]) // panic

    // ...
}
```

Returns the following (and process exits after panic):

```json
{
  "errorType": "Runtime.Panic.boundsError",
  "errorMessage": "runtime error: index out of range [1] with length 1",
  "stackTrace": [
    {
      "label": "gopanic",
      "line": 783,
      "path": "/usr/local/go/src/runtime/panic.go"
    },
    {
      "label": "goPanicIndex",
      "line": 115,
      "path": "/usr/local/go/src/runtime/panic.go"
    },
    {
      "label": "handler",
      "line": 30,
      "path": "/Users/me/Code/voker/examples/error/main.go"
    },
    {
      "label": "callHandler[...]",
      "line": 198,
      "path": "Code/voker/voker.go"
    },
    {
      "label": "handleInvocation[...]",
      "line": 170,
      "path": "Code/voker/voker.go"
    },
    {
      "label": "Start[...]",
      "line": 120,
      "path": "Code/voker/voker.go"
    },
    {
      "label": "main",
      "line": 21,
      "path": "/Users/me/Code/voker/examples/error/main.go"
    },
    {
      "label": "main",
      "line": 285,
      "path": "/usr/local/go/src/runtime/proc.go"
    },
    {
      "label": "goexit",
      "line": 1268,
      "path": "/usr/local/go/src/runtime/asm_arm64.s"
    }
  ]
}
```

## Testing Your Handler

```go
func TestHandler(t *testing.T) {
    event := MyEvent{Name: "World"}
    response, err := handler(context.Background(), event)

    assert.NoError(t, err)
    assert.Equal(t, "Hello, World", response.Message)
}
```

No mocking required - your handler is just a function!

## Building and Deploying

### Build for Lambda

```bash
GOOS=linux GOARCH=arm64 go build -o bootstrap main.go
zip function.zip bootstrap
```

### Using with AWS SAM

```yaml
AWSTemplateFormatVersion: "2010-09-09"
Transform: AWS::Serverless-2016-10-31

Resources:
  MyFunction:
    Type: AWS::Serverless::Function
    Properties:
      CodeUri: .
      Handler: bootstrap
      Runtime: provided.al2023
      Architectures: [arm64]
```

### Using with AWS CDK

```go
lambda.NewFunction(stack, jsii.String("MyFunction"), &lambda.FunctionProps{
    Runtime: lambda.Runtime_PROVIDED_AL2023(),
    Handler: jsii.String("bootstrap"),
    Code:    lambda.Code_FromAsset(jsii.String("./function.zip"), nil),
    Architecture: lambda.Architecture_ARM_64(),
})
```

## Migration from aws-lambda-go

### Before (aws-lambda-go)

```go
import "github.com/aws/aws-lambda-go/lambda"

func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    // ...
}

func main() {
    lambda.StartHandlerFunc(handler)
}
```

### After (Voker)

```go
import "github.com/hotsock/voker"

func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
    // ...
}

func main() {
    voker.Start(handler)
}
```

That's it! If you were using the standard `func(context.Context, TIn) (TOut, error)` signature, it's a drop-in replacement.

If you were using `lambdacontext.LambdaContext` (most likely `lambdacontext.FromContext(ctx)` in your code, switch those to `voker.FromContext(ctx)`).

## License

See [LICENSE](LICENSE).
