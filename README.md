# Voker

A minimal, modern AWS Lambda runtime for Go that focuses on performance, simplicity, and type safety.

## Overview

Voker is a simplified alternative to [`aws-lambda-go`](https://github.com/aws/aws-lambda-go) that maintains full compatibility with the AWS Lambda Runtime API. It uses Go generics to provide compile-time type safety with a clean, single-function-signature design. It supports structured logging with `slog` and proper log levels for errors.

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
- `TOut` is your output type (must be JSON-serializable)
- `error` is required for error handling

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
      "line": 23,
      "path": "/Users/me/Code/voker/examples/error/main.go"
    },
    {
      "label": "handlerWithLambdaLogging[...].func1",
      "line": 48,
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
      "line": 13,
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
