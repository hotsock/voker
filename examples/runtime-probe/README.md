# AWS runtime probe

This short-lived stack validates Voker's low-level runtime behavior against
AWS Lambda in `us-west-2`. It covers conditional buffered and streaming
responses from one function, typed custom error payloads, continued container
use after a custom error with a user-supplied stack trace, automatic
`io.Closer` cleanup on successful and failed streams, streaming error trailers,
fatal streaming panic exit, extension initialization errors and panics, and
extension registration failure reporting through `/runtime/init/error`.

It also validates invocation metadata end to end. A raw-headers function runs
a hand-rolled Runtime API loop (no voker) and echoes every `Lambda-Runtime-*`
header verbatim, capturing ground-truth payloads for voker's test fixtures.
The probe script uses it to verify:

- **Cognito identity and client context**: an `AWS::Cognito::IdentityPool`
  with unauthenticated identities vends credentials that the script uses to
  invoke the probe functions, making Lambda deliver the
  `Lambda-Runtime-Cognito-Identity` header (camelCase keys) and, with
  `--client-context`, the `Lambda-Runtime-Client-Context` header. The parsed
  values are checked against `voker.FromContext` via the `echo-context`
  action.
- **Tenant isolation mode**: the script creates a temporary function with
  `TenancyConfig.TenantIsolationMode=PER_TENANT` (created and deleted by the
  script; SAM does not manage it), invokes it with `--tenant-id`, verifies the
  raw `Lambda-Runtime-Aws-Tenant-Id` header, then flips the function to voker
  and verifies `LambdaContext.TenantID`.

The Function URL is configured with `RESPONSE_STREAM`. AWS delivers a buffered
Runtime API response from that URL verbatim, so the `/buffered` assertion
expects the Lambda response envelope itself; `/stream` verifies the HTTP
integration prelude and streamed body. Direct invocation separately verifies
ordinary buffered output from the same function.

```sh
make deploy
make probe
make delete
```

Set `AWS_PROFILE` when a named AWS CLI profile is required, for example
`make deploy AWS_PROFILE=my-profile`. When it is omitted, the AWS CLI uses its
normal default credential resolution. Override `AWS_REGION` or `STACK_NAME`
when necessary. The probe creates a temporary artifact bucket, five Lambda
functions, and a Cognito identity pool; always run `make delete` when finished
so both the CloudFormation stack and artifact bucket are removed.
