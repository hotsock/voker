package voker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLambdaContext(t *testing.T) {
	lc := &LambdaContext{
		AwsRequestID:       "request-123",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123456789012:function:test",
		Identity: CognitoIdentity{
			CognitoIdentityID:     "identity-456",
			CognitoIdentityPoolID: "pool-789",
		},
		ClientContext: ClientContext{
			Client: ClientApplication{
				InstallationID: "install-abc",
				AppTitle:       "MyApp",
			},
			Custom: map[string]string{
				"key": "value",
			},
		},
	}

	ctx := NewContext(context.Background(), lc)

	retrieved, ok := FromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, lc.AwsRequestID, retrieved.AwsRequestID)
	assert.Equal(t, lc.InvokedFunctionArn, retrieved.InvokedFunctionArn)
	assert.Equal(t, lc.Identity.CognitoIdentityID, retrieved.Identity.CognitoIdentityID)
	assert.Equal(t, lc.ClientContext.Client.InstallationID, retrieved.ClientContext.Client.InstallationID)
	assert.Equal(t, "value", retrieved.ClientContext.Custom["key"])
}

func TestFromContext_NotPresent(t *testing.T) {
	ctx := context.Background()
	lc, ok := FromContext(ctx)
	assert.False(t, ok)
	assert.Nil(t, lc)
}
