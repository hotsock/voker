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
make deploy STACK_NAME=vokerhttp-aws-ingress-probe
```

Send requests to the printed endpoints. `/` echoes the reconstructed
`net/http` request, `/binary` returns five non-text bytes, `/status` returns
HTTP 418, and `/stream` writes three SSE chunks 750 milliseconds apart.
The streaming Function URL and API Gateway v1 endpoints send those chunks as
they are flushed. Their buffered counterparts, ALB, and API Gateway v2 release
the response after the handler finishes. Every invocation logs its typed
Lambda event with the `VOKER_EVENT` marker so it can be retrieved from the
function's CloudWatch log group.

## Path encoding matrix

Run the reserved-character probe against all six deployed endpoints:

```sh
python3 paths.py --profile default --region us-west-2 \
  --stack-name vokerhttp-aws-ingress-probe --output path-captures.json
```

This checks spaces, `?`, `%`, `#`, encoded slashes, Unicode, and double-encoded
sequences, with an independent query string. It exits nonzero on a mismatch
and saves every result, including HTTP failures. Successful responses include
the typed AWS event and the reconstructed `Path`, `RawPath`, `RawQuery`, and
`RequestURI`. Events are also logged **before** adapter conversion, so a parse
failure can be investigated through CloudWatch's `VOKER_EVENT` records.

The probe checks the captured AWS behavior recorded in
`vokerhttp/testdata/ingress-paths.json`, as well as fidelity from event to handler.
It reports whether the original wire path survives separately. Capture files
contain deployment metadata; the regression fixture retains only relevant
path fields.

### Observed AWS behavior (us-west-2, 2026-09-13)

REST, Function URLs, and ALB preserve the encoded path in their events. All ten
cases reach the handler decoded exactly once, in both buffered and streaming
modes where supported.

HTTP API v2 delivers a decoded `rawPath`, with additional AWS-side limitations:

| Sent path suffix | Event `rawPath` suffix |
| --- | --- |
| `question%3Fvalue` | `question` (truncated before invocation) |
| `percent%25done` | HTTP 400; no Lambda invocation observed |
| `hash%23value` | `hash#value` |
| `literal%252Fvalue` | `literal/value` |
| `literal%253Fvalue` | `literal?value` |
| `literal%2525value` | `literal%value` |

The HTTP API adapter must preserve the path it receives, including literal
`#`, `?`, and `%`, rather than parse those characters again as URL syntax.
It cannot recover characters AWS already removed or decoded. The probe treats
the documented HTTP 400 as an expected rejection and checks the other paths
against these observed event values. An AWS behavior change therefore fails
the probe and calls for reviewing the captures and fixture together.

Delete every resource created by the example when finished:

```sh
make delete STACK_NAME=vokerhttp-aws-ingress-probe
```
