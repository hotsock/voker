package voker

import (
	"context"
)

// ClientApplication contains metadata about the client application
type ClientApplication struct {
	InstallationID string `json:"installation_id"`
	AppTitle       string `json:"app_title"`
	AppVersionCode string `json:"app_version_code"`
	AppPackageName string `json:"app_package_name"`
}

// ClientContext contains information about the client application and device
type ClientContext struct {
	Client ClientApplication `json:"client"`
	Env    map[string]string `json:"env"`
	Custom map[string]string `json:"custom"`
}

// CognitoIdentity contains Cognito identity information
type CognitoIdentity struct {
	CognitoIdentityID     string `json:"cognito_identity_id"`
	CognitoIdentityPoolID string `json:"cognito_identity_pool_id"`
}

// LambdaContext contains the metadata for a Lambda invocation
type LambdaContext struct {
	// AwsRequestID is the unique request identifier
	AwsRequestID string

	// InvokedFunctionArn is the ARN of the Lambda function being invoked
	InvokedFunctionArn string

	// Identity contains Cognito identity information
	Identity CognitoIdentity

	// ClientContext contains client application information
	ClientContext ClientContext
}

type contextKey struct{}

var lambdaContextKey = &contextKey{}

// NewContext returns a new context with the LambdaContext attached
func NewContext(parent context.Context, lc *LambdaContext) context.Context {
	return context.WithValue(parent, lambdaContextKey, lc)
}

// FromContext extracts the LambdaContext from the context, if present
func FromContext(ctx context.Context) (*LambdaContext, bool) {
	lc, ok := ctx.Value(lambdaContextKey).(*LambdaContext)
	return lc, ok
}
