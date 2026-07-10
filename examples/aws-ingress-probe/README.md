# AWS ingress probe

This example deploys the same voker-backed `http.Handler` behind all four
`vokerhttp` adapters:

- Application Load Balancer with multi-value headers enabled
- API Gateway v1 REST API with Lambda proxy integration
- API Gateway v2 HTTP API with payload format 2.0
- Lambda Function URL

It uses the default VPC in `us-west-2`, creates a temporary S3 artifact bucket,
and prints each public endpoint after the stack is ready.

```sh
make deploy AWS_PROFILE=applyology STACK_NAME=vokerhttp-aws-ingress-probe
```

Send requests to the printed endpoints. `/` echoes the reconstructed
`net/http` request, `/binary` returns five non-text bytes, and `/status` returns
HTTP 418. Every invocation logs its typed Lambda event with the `VOKER_EVENT`
marker so it can be retrieved from the function's CloudWatch log group.

Delete every resource created by the example when finished:

```sh
make delete AWS_PROFILE=applyology STACK_NAME=vokerhttp-aws-ingress-probe
```
