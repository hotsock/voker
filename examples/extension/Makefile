MAKE_REL_PATH:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

GOLANG ?= go
GOARCH ?= arm64
GO_BUILD_FLAGS ?= -ldflags="-s -w"
GO_BUILD ?= CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} ${GOLANG} build ${GO_BUILD_FLAGS}
GO_BUILD_FILES := $(shell find . -type f -not -path "./.git/*" -not -path "./bin/*" -not -name "*_test.go")

bin/bootstrap: ${GO_BUILD_FILES}
	${GO_BUILD} -o $@

.PHONY: test
test:
	${GOLANG} test -count=1 ./...

.PHONY: build
build: bin/bootstrap

.PHONY: deploy
deploy: test build
	sam deploy
