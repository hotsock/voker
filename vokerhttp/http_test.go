package vokerhttp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsTextContent(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"text/html", true},
		{"text/plain", true},
		{"text/css", true},
		{"text/csv", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/xml", true},
		{"application/javascript", true},
		{"application/x-www-form-urlencoded", true},
		{"application/vnd.api+json", true},
		{"application/atom+xml", true},
		{"application/octet-stream", false},
		{"image/png", false},
		{"image/jpeg", false},
		{"application/protobuf", false},
		{"application/gzip", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			assert.Equal(t, tt.want, isTextContent(tt.contentType))
		})
	}
}
