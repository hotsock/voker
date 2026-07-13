GOLANG ?= go
SAM ?= sam
AWS_PROFILE ?=
AWS_REGION ?=
STACK_NAME ?= voker-managed-instances
MAX_VCPU_COUNT ?= 30
MAX_CONCURRENCY ?= 8
GO_BUILD_FLAGS ?= -trimpath -ldflags="-s -w"

SAM_ARGS :=
ifneq ($(strip $(AWS_REGION)),)
SAM_ARGS += --region $(AWS_REGION)
endif
ifneq ($(strip $(AWS_PROFILE)),)
SAM_ARGS += --profile $(AWS_PROFILE)
endif

.PHONY: test
test:
	$(GOLANG) test -count=1 ./...

.PHONY: validate
validate:
	$(SAM) validate --lint --template-file template.yml $(SAM_ARGS)

.PHONY: build
build: test
	$(SAM) build --template-file template.yml

.PHONY: build-ManagedFunction
build-ManagedFunction:
	mkdir -p "$(ARTIFACTS_DIR)"
	cd "$(abspath $(ARTIFACTS_DIR)/../../..)" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GOLANG) build $(GO_BUILD_FLAGS) -o "$(ARTIFACTS_DIR)/bootstrap" .

.PHONY: deploy
deploy: build
	$(SAM) deploy $(SAM_ARGS) \
		--stack-name $(STACK_NAME) \
		--resolve-s3 \
		--capabilities CAPABILITY_IAM \
		--parameter-overrides MaxVCpuCount=$(MAX_VCPU_COUNT) PerExecutionEnvironmentMaxConcurrency=$(MAX_CONCURRENCY) \
		--no-fail-on-empty-changeset

.PHONY: probe
probe:
	AWS_PROFILE="$(AWS_PROFILE)" AWS_REGION="$(AWS_REGION)" STACK_NAME="$(STACK_NAME)" ./probe.sh

.PHONY: delete
delete:
	$(SAM) delete $(SAM_ARGS) --stack-name $(STACK_NAME) --no-prompts
