# Live AWS fixtures

These request events were captured from CloudWatch on July 10, 2026 after
public requests to short-lived resources in `us-west-2`. The deployed probe is
in `examples/aws-ingress-probe`.

The request included an encoded path, repeated and encoded query parameters,
repeated headers, two cookies, and a JSON body. Fields that require optional
AWS features, such as Cognito or mutual TLS, retain the empty values emitted by
the typed event logger.
