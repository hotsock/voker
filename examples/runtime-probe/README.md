# AWS runtime probe

This short-lived stack validates Voker's low-level runtime behavior against
AWS Lambda in `us-west-2`. It covers conditional buffered and streaming
responses from one function, typed custom error payloads, continued container
use after a custom error with a user-supplied stack trace, automatic
`io.Closer` cleanup on successful and failed streams, streaming error trailers,
fatal streaming panic exit, extension initialization errors and panics, and
extension registration failure reporting through `/runtime/init/error`.

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
when necessary. The probe creates a temporary artifact bucket and four Lambda
functions; always run `make delete` when finished so both the CloudFormation
stack and artifact bucket are removed.
