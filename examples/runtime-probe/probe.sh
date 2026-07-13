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
RAW_HEADERS_FUNCTION=$(output RawHeadersFunctionName)
IDENTITY_POOL_ID=$(output ProbeIdentityPoolId)
PROBE_ROLE_ARN=$(output ProbeRoleArn)
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

# --- Lambda context metadata: standard invocations carry no tenant ID -------

invoke "$PROBE_FUNCTION" '{"action":"echo-context"}' "$TMPDIR_PROBE/echo-context.json" >/dev/null
jq -e '(.requestId | length) > 0 and .tenantId == "" and (.traceId | length) > 0' "$TMPDIR_PROBE/echo-context.json" >/dev/null

# --- Tenant isolation mode ---------------------------------------------------
# Created via CLI: the function needs TenancyConfig, and starts in raw-headers
# mode to capture the ground-truth Lambda-Runtime-Aws-Tenant-Id header before
# switching to voker to validate parsing end to end.

TENANT_FUNCTION="$STACK_NAME-tenant"
(cd bin && zip -q "$TMPDIR_PROBE/tenant.zip" bootstrap)
aws lambda create-function $AWS_ARGS \
	--function-name "$TENANT_FUNCTION" \
	--runtime provided.al2023 \
	--architectures arm64 \
	--handler bootstrap \
	--role "$PROBE_ROLE_ARN" \
	--zip-file "fileb://$TMPDIR_PROBE/tenant.zip" \
	--tenancy-config TenantIsolationMode=PER_TENANT \
	--environment 'Variables={VOKER_PROBE_MODE=raw-headers}' \
	--logging-config LogFormat=JSON >/dev/null
trap 'aws lambda delete-function $AWS_ARGS --function-name "$TENANT_FUNCTION" >/dev/null 2>&1 || true; rm -rf "$TMPDIR_PROBE"' EXIT
aws lambda wait function-active-v2 $AWS_ARGS --function-name "$TENANT_FUNCTION"

aws lambda invoke $AWS_ARGS --function-name "$TENANT_FUNCTION" --tenant-id tenant-blue \
	--cli-binary-format raw-in-base64-out --payload '{}' "$TMPDIR_PROBE/tenant-raw.json" >/dev/null
jq -e '.headers["Lambda-Runtime-Aws-Tenant-Id"][0] == "tenant-blue"' "$TMPDIR_PROBE/tenant-raw.json" >/dev/null
printf 'tenant raw headers: %s\n' "$(jq -c '.headers | keys' "$TMPDIR_PROBE/tenant-raw.json")"

aws lambda update-function-configuration $AWS_ARGS --function-name "$TENANT_FUNCTION" \
	--environment 'Variables={VOKER_PROBE_MODE=runtime}' >/dev/null
aws lambda wait function-updated-v2 $AWS_ARGS --function-name "$TENANT_FUNCTION"

aws lambda invoke $AWS_ARGS --function-name "$TENANT_FUNCTION" --tenant-id tenant-blue \
	--cli-binary-format raw-in-base64-out --payload '{"action":"echo-context"}' "$TMPDIR_PROBE/tenant-parsed.json" >/dev/null
jq -e '.tenantId == "tenant-blue"' "$TMPDIR_PROBE/tenant-parsed.json" >/dev/null

aws lambda delete-function $AWS_ARGS --function-name "$TENANT_FUNCTION" >/dev/null
# Lambda auto-creates this log group outside the stack; remove it too.
aws logs delete-log-group $AWS_ARGS --log-group-name "/aws/lambda/$TENANT_FUNCTION" 2>/dev/null || true

# --- Cognito identity + client context ---------------------------------------
# Invoke with credentials vended by the identity pool so the Runtime API
# delivers Lambda-Runtime-Cognito-Identity, and pass a client context so it
# delivers Lambda-Runtime-Client-Context. Capture both raw (ground truth for
# fixtures) and voker-parsed.

IDENTITY_ID=$(aws cognito-identity get-id $AWS_ARGS --identity-pool-id "$IDENTITY_POOL_ID" --query IdentityId --output text)
COGNITO_CREDS=$(aws cognito-identity get-credentials-for-identity $AWS_ARGS --identity-id "$IDENTITY_ID" --query Credentials --output json)
COGNITO_ACCESS_KEY_ID=$(printf '%s' "$COGNITO_CREDS" | jq -r .AccessKeyId)
COGNITO_SECRET_KEY=$(printf '%s' "$COGNITO_CREDS" | jq -r .SecretKey)
COGNITO_SESSION_TOKEN=$(printf '%s' "$COGNITO_CREDS" | jq -r .SessionToken)

CLIENT_CONTEXT=$(printf '%s' '{"client":{"installation_id":"probe-install-1","app_title":"voker-probe","app_version_code":"1.0","app_package_name":"com.hotsock.voker.probe"},"env":{"platform":"probe"},"custom":{"probe":"cognito"}}' | base64)

cognito_invoke() {
	env -u AWS_PROFILE \
		AWS_ACCESS_KEY_ID="$COGNITO_ACCESS_KEY_ID" \
		AWS_SECRET_ACCESS_KEY="$COGNITO_SECRET_KEY" \
		AWS_SESSION_TOKEN="$COGNITO_SESSION_TOKEN" \
		aws lambda invoke --region "$AWS_REGION" --function-name "$1" \
		--client-context "$CLIENT_CONTEXT" \
		--cli-binary-format raw-in-base64-out --payload "$2" "$3"
}

cognito_invoke "$RAW_HEADERS_FUNCTION" '{}' "$TMPDIR_PROBE/cognito-raw.json" >/dev/null
jq -e --arg id "$IDENTITY_ID" '.headers["Lambda-Runtime-Cognito-Identity"][0] | fromjson | .cognitoIdentityId == $id' "$TMPDIR_PROBE/cognito-raw.json" >/dev/null
jq -e '.headers["Lambda-Runtime-Client-Context"][0] | fromjson | .client.installation_id == "probe-install-1"' "$TMPDIR_PROBE/cognito-raw.json" >/dev/null
printf 'cognito identity header: %s\n' "$(jq -r '.headers["Lambda-Runtime-Cognito-Identity"][0]' "$TMPDIR_PROBE/cognito-raw.json")"
printf 'client context header: %s\n' "$(jq -r '.headers["Lambda-Runtime-Client-Context"][0]' "$TMPDIR_PROBE/cognito-raw.json")"

cognito_invoke "$PROBE_FUNCTION" '{"action":"echo-context"}' "$TMPDIR_PROBE/cognito-parsed.json" >/dev/null
jq -e --arg id "$IDENTITY_ID" --arg pool "$IDENTITY_POOL_ID" \
	'.identity.cognitoIdentityId == $id and .identity.cognitoIdentityPoolId == $pool and .clientContext.client.installation_id == "probe-install-1" and .clientContext.custom.probe == "cognito"' \
	"$TMPDIR_PROBE/cognito-parsed.json" >/dev/null

printf '%s\n' "runtime probe passed"
