# Lambda Managed Instances example

This SAM application deploys Voker on AWS Lambda Managed Instances and verifies
that one execution environment safely processes several invocations at once.
The live probe checks concurrent request/response matching, invocation-scoped
request and X-Ray metadata, isolated application errors, and cooperative work
stoppage shortly before the Lambda deadline.

The function and capacity provider use Arm64. Lambda selects a supported
Graviton instance type for the capacity provider. Although C9g instances are
available through EC2 in some Regions, Lambda Managed Instances must separately
support an instance type before it can be placed on an allow-list.

The stack is intentionally self-contained. It creates a VPC, three public
subnets, an internet gateway, a Lambda capacity provider, and a published
function alias. Public subnets are the simplest and lowest-cost connectivity
choice for a short-lived development probe; use private subnets with NAT or VPC
endpoints for production workloads.

## Prerequisites

- Go matching the version in `go.mod`
- AWS CLI v2, AWS SAM CLI, and `jq`
- An AWS Region where Lambda Managed Instances is available
- Deployment permissions for CloudFormation, Lambda, EC2, IAM, S3, and
  `lambda:PassCapacityProvider`
- `iam:CreateServiceLinkedRole` if the account has never created a Lambda
  Managed Instances capacity provider

Deploying the first published version can take several minutes. Lambda launches
three managed EC2 instances by default for Availability Zone resilience, and
those instances remain billable until the capacity provider is deleted.

## Deploy and test

```sh
make validate
make deploy
make probe
make delete
```

The default stack name is `voker-managed-instances`. Region and credentials use
normal AWS CLI resolution unless `AWS_REGION` and `AWS_PROFILE` are passed on
the command. For example:

```sh
make deploy AWS_REGION=your-region AWS_PROFILE=your-profile STACK_NAME=my-voker-probe
make probe AWS_REGION=your-region AWS_PROFILE=your-profile STACK_NAME=my-voker-probe
make delete AWS_REGION=your-region AWS_PROFILE=your-profile STACK_NAME=my-voker-probe
```

Always run `make delete` after testing. The template configures a maximum of 30
capacity-provider vCPUs and eight concurrent invocations per execution
environment. Override them with `MAX_VCPU_COUNT` and `MAX_CONCURRENCY` on the
`make deploy` command when needed.

## What the handler demonstrates

The handler uses atomic counters for shared process state and returns the event
ID, Lambda request ID, trace ID, execution-environment ID, configured maximum
concurrency, and observed concurrency. A real Managed Instances handler must
apply the same concurrency discipline to all mutable globals, caches, clients,
and shared `/tmp` paths.

Each invocation receives an independent `context.Context`. Managed Instances do
not forcibly stop Go code when an invocation times out, so the example installs
a 500 ms deadline guard and returns a typed error before Lambda's deadline. Real
applications should choose a buffer large enough for their next unit of work
and stop all side effects when the context is done.
