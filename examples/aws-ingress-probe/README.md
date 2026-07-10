# AWS ingress probe

This example deploys the same voker-backed `http.Handler` behind every
supported buffered and streaming ingress mode:

- Application Load Balancer with multi-value headers enabled
- API Gateway v1 REST API with buffered Lambda proxy integration
- API Gateway v1 REST API with streaming Lambda proxy integration
- API Gateway v2 HTTP API with payload format 2.0
- Lambda Function URL in `BUFFERED` mode
- Lambda Function URL in `RESPONSE_STREAM` mode

It uses the default VPC in `us-west-2`, creates a temporary S3 artifact bucket,
and prints each public endpoint after the stack is ready.

```sh
make deploy AWS_PROFILE=applyology STACK_NAME=vokerhttp-aws-ingress-probe
```

Send requests to the printed endpoints. `/` echoes the reconstructed
`net/http` request, `/binary` returns five non-text bytes, `/status` returns
HTTP 418, and `/stream` writes three SSE chunks 750 milliseconds apart.
The streaming Function URL and API Gateway v1 endpoints send those chunks as
they are flushed. Their buffered counterparts, ALB, and API Gateway v2 release
the response after the handler finishes. Every invocation logs its typed
Lambda event with the `VOKER_EVENT` marker so it can be retrieved from the
function's CloudWatch log group.

Delete every resource created by the example when finished:

```sh
make delete AWS_PROFILE=applyology STACK_NAME=vokerhttp-aws-ingress-probe
```
