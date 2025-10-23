# Voker

A minimal, modern AWS Lambda runtime for Go that focuses on performance, simplicity, and type safety.

## Overview

Voker is a simplified alternative to [`aws-lambda-go`](https://github.com/aws/aws-lambda-go) that maintains full compatibility with the AWS Lambda Runtime API. It uses Go generics to provide compile-time type safety with a clean, single-function-signature design.

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

    // Access deadline
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
    panic("unexpected error")
}
// Returns: {"errorMessage":"unexpected error","errorType":"string","stackTrace":[...]}
// Note: Process exits after panic
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
GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o bootstrap main.go
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
    lambda.Start(handler)
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

## License

See [LICENSE](LICENSE).
