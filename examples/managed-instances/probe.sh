#!/bin/sh
set -eu

STACK_NAME=${STACK_NAME:-voker-managed-instances}
AWS_ARGS=""
if [ -n "${AWS_REGION:-}" ]; then
	AWS_ARGS="--region $AWS_REGION"
else
	unset AWS_REGION
fi
if [ -n "${AWS_PROFILE:-}" ]; then
	AWS_ARGS="$AWS_ARGS --profile $AWS_PROFILE"
else
	unset AWS_PROFILE
fi

TMPDIR_PROBE=$(mktemp -d "${TMPDIR:-/tmp}/voker-managed-instances.XXXXXX")
trap 'rm -rf "$TMPDIR_PROBE"' EXIT

output() {
	aws cloudformation describe-stacks $AWS_ARGS --stack-name "$STACK_NAME" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue | [0]" --output text
}

FUNCTION_ALIAS=$(output FunctionAlias)
MAX_CONCURRENCY=$(output MaxConcurrency)

attempt=0
while :; do
	state=$(aws lambda get-function-configuration $AWS_ARGS --function-name "$FUNCTION_ALIAS" --query State --output text 2>/dev/null || true)
	if [ "$state" = "Active" ]; then
		break
	fi
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 90 ]; then
		echo "timed out waiting for $FUNCTION_ALIAS to become Active (last state: $state)" >&2
		exit 1
	fi
	sleep 10
done

invoke() {
	id=$1
	fail=$2
	response_file=$3
	metadata_file=$4
	payload=$(printf '{"id":"%s","delayMs":2000,"fail":%s}' "$id" "$fail")
	aws lambda invoke $AWS_ARGS \
		--function-name "$FUNCTION_ALIAS" \
		--cli-binary-format raw-in-base64-out \
		--payload "$payload" \
		"$response_file" >"$metadata_file"
}

invocations=24
pids=""
i=0
while [ "$i" -lt "$invocations" ]; do
	if [ $((i % 6)) -eq 5 ]; then
		invoke "event-$i" true "$TMPDIR_PROBE/error-$i.json" "$TMPDIR_PROBE/metadata-$i.json" &
	else
		invoke "event-$i" false "$TMPDIR_PROBE/success-$i.json" "$TMPDIR_PROBE/metadata-$i.json" &
	fi
	pids="$pids $!"
	i=$((i + 1))
done

for pid in $pids; do
	wait "$pid"
done

i=0
while [ "$i" -lt "$invocations" ]; do
	if [ $((i % 6)) -eq 5 ]; then
		jq -e '.FunctionError == "Unhandled"' "$TMPDIR_PROBE/metadata-$i.json" >/dev/null
		jq -e --arg id "event-$i" \
			'.errorType == "Application.ExpectedFailure" and .errorMessage == ("event " + $id + " requested a failure")' \
			"$TMPDIR_PROBE/error-$i.json" >/dev/null
	else
		jq -e '.FunctionError == null' "$TMPDIR_PROBE/metadata-$i.json" >/dev/null
		jq -e --arg id "event-$i" --argjson concurrency "$MAX_CONCURRENCY" '
			.id == $id and
			(.requestId | length) > 0 and
			(.traceId | length) > 0 and
			(.environmentId | length) > 0 and
			.initializationType == "lambda-managed-instances" and
			.architecture == "arm64" and
			.maxConcurrency == $concurrency
		' "$TMPDIR_PROBE/success-$i.json" >/dev/null
	fi
	i=$((i + 1))
done

jq -s -e '
	group_by(.environmentId) |
	any(.[]; (map(.peakConcurrency) | max) >= 2)
' "$TMPDIR_PROBE"/success-*.json >/dev/null

deadline_metadata=$(aws lambda invoke $AWS_ARGS \
	--function-name "$FUNCTION_ALIAS" \
	--cli-binary-format raw-in-base64-out \
	--payload '{"id":"deadline","deadlineProbe":true}' \
	"$TMPDIR_PROBE/deadline.json")
printf '%s' "$deadline_metadata" | jq -e '.FunctionError == "Unhandled"' >/dev/null
jq -e '
	.errorType == "Application.DeadlineGuard" and
	.errorMessage == "event deadline stopped before the Lambda deadline"
' "$TMPDIR_PROBE/deadline.json" >/dev/null

printf '%s\n' "Lambda Managed Instances probe passed for $FUNCTION_ALIAS"
