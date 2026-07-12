#!/bin/sh
set -eu

AWS_REGION=${AWS_REGION:-us-west-2}
STACK_NAME=${STACK_NAME:-voker-runtime-probe}
AWS_ARGS="--region $AWS_REGION"
if [ -n "${AWS_PROFILE:-}" ]; then
	AWS_ARGS="$AWS_ARGS --profile $AWS_PROFILE"
fi
TMPDIR_PROBE=$(mktemp -d "${TMPDIR:-/tmp}/voker-runtime-probe.XXXXXX")
START_TIME_MS=$(($(date +%s) * 1000))
trap 'rm -rf "$TMPDIR_PROBE"' EXIT

output() {
	aws cloudformation describe-stacks $AWS_ARGS --stack-name "$STACK_NAME" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue | [0]" --output text
}

invoke() {
	function_name=$1
	payload=$2
	destination=$3
	aws lambda invoke $AWS_ARGS --function-name "$function_name" --cli-binary-format raw-in-base64-out --payload "$payload" "$destination"
}

assert_init_error() {
	function_name=$1
	want_type=$2
	want_message=$3
	destination=$4
	metadata=$(invoke "$function_name" '{}' "$destination")
	printf '%s' "$metadata" | jq -e '.FunctionError == "Unhandled"' >/dev/null
	jq -e --arg type "$want_type" --arg message "$want_message" '.errorType == $type and (.errorMessage | contains($message))' "$destination" >/dev/null
}

wait_for_log_count() {
	log_group=$1
	pattern=$2
	minimum=$3
	description=$4
	attempt=0
	while :; do
		count=$(aws logs filter-log-events $AWS_ARGS --no-paginate --log-group-name "$log_group" --start-time "$START_TIME_MS" --filter-pattern "$pattern" --query 'length(events)' --output text)
		if [ "$count" -ge "$minimum" ]; then
			return
		fi
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 20 ]; then
			echo "timed out waiting for $description" >&2
			exit 1
		fi
		sleep 3
	done
}

PROBE_FUNCTION=$(output ProbeFunctionName)
INIT_ERROR_FUNCTION=$(output InitErrorFunctionName)
INIT_PANIC_FUNCTION=$(output InitPanicFunctionName)
REGISTER_ERROR_FUNCTION=$(output RegisterErrorFunctionName)
PROBE_URL=$(output ProbeFunctionURL)

buffered=$(curl --fail --silent --show-error "${PROBE_URL}buffered")
printf '%s' "$buffered" | jq -e '.statusCode == 200 and .body == "buffered response"' >/dev/null

streamed=$(curl --fail --silent --show-error "${PROBE_URL}stream")
test "$streamed" = "streamed response"

stream_error=$(curl --fail --silent --show-error "${PROBE_URL}stream-error")
test "$stream_error" = "partial response"

stream_panic=$(curl --fail --silent --show-error "${PROBE_URL}stream-panic")
test "$stream_panic" = "partial panic response"

custom_metadata=$(invoke "$PROBE_FUNCTION" '{"action":"custom-error"}' "$TMPDIR_PROBE/custom-error.json")
printf '%s' "$custom_metadata" | jq -e '.FunctionError == "Unhandled"' >/dev/null
jq -e '.errorType == "Application.CustomError" and .errorMessage == "custom probe error" and (.stackTrace | length) == 1' "$TMPDIR_PROBE/custom-error.json" >/dev/null

invoke "$PROBE_FUNCTION" '{"action":"after-custom-error"}' "$TMPDIR_PROBE/after-error.json" >/dev/null
jq -e '.action == "after-custom-error" and (.requestId | length) > 0' "$TMPDIR_PROBE/after-error.json" >/dev/null

assert_init_error "$INIT_ERROR_FUNCTION" "Extension.InitError" "probe init error" "$TMPDIR_PROBE/init-error.json"
assert_init_error "$INIT_PANIC_FUNCTION" "Runtime.Panic.string" "probe init panic" "$TMPDIR_PROBE/init-panic.json"
jq -e '(.stackTrace | length) > 0' "$TMPDIR_PROBE/init-panic.json" >/dev/null

register_metadata=$(invoke "$REGISTER_ERROR_FUNCTION" '{}' "$TMPDIR_PROBE/register-error.json")
printf '%s' "$register_metadata" | jq -e '.FunctionError == "Unhandled"' >/dev/null
jq -e '.errorMessage | contains("failed to register extension")' "$TMPDIR_PROBE/register-error.json" >/dev/null

log_group="/aws/lambda/$PROBE_FUNCTION"
wait_for_log_count "$log_group" '"VOKER_PROBE closer_closed"' 3 "io.Closer markers"
wait_for_log_count "$log_group" '"streaming invocation error"' 2 "streaming error logs"
wait_for_log_count "$log_group" '"Runtime.ExitError"' 1 "fatal streaming panic exit"

printf '%s\n' "runtime probe passed"
