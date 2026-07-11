GOLANG ?= go
AWS ?= aws
AWS_PROFILE ?= default
AWS_REGION ?= us-west-2
STACK_NAME ?= voker-cloudformation-example
GOARCH ?= arm64
PACKAGED_TEMPLATE ?= /tmp/$(STACK_NAME)-packaged.yml

AWS_ARGS := --profile $(AWS_PROFILE) --region $(AWS_REGION)
ACCOUNT_ID = $(shell $(AWS) sts get-caller-identity $(AWS_ARGS) --query Account --output text)
ARTIFACT_BUCKET ?= $(STACK_NAME)-$(ACCOUNT_ID)-$(AWS_REGION)
VOKER_SOURCES := $(wildcard ../../*.go ../../vokercfn/*.go)

bin/bootstrap: main.go go.mod ../../go.mod $(VOKER_SOURCES)
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GOLANG) build -trimpath -ldflags="-s -w" -o $@ .

.PHONY: test
test:
	$(GOLANG) test ./...

.PHONY: build
build: test bin/bootstrap

.PHONY: artifact-bucket
artifact-bucket:
	$(AWS) s3api head-bucket $(AWS_ARGS) --bucket $(ARTIFACT_BUCKET) 2>/dev/null || $(AWS) s3api create-bucket $(AWS_ARGS) --bucket $(ARTIFACT_BUCKET) --create-bucket-configuration LocationConstraint=$(AWS_REGION)
	$(AWS) s3api put-bucket-tagging $(AWS_ARGS) --bucket $(ARTIFACT_BUCKET) --tagging 'TagSet=[{Key=Purpose,Value=voker-cloudformation-example}]'

.PHONY: deploy
deploy: build artifact-bucket
	$(AWS) cloudformation package $(AWS_ARGS) --template-file template.yml --s3-bucket $(ARTIFACT_BUCKET) --output-template-file $(PACKAGED_TEMPLATE)
	$(AWS) cloudformation deploy $(AWS_ARGS) --stack-name $(STACK_NAME) --template-file $(PACKAGED_TEMPLATE) --capabilities CAPABILITY_IAM
	$(AWS) cloudformation describe-stacks $(AWS_ARGS) --stack-name $(STACK_NAME) --query 'Stacks[0].Outputs' --output table

.PHONY: delete
delete:
	$(AWS) cloudformation delete-stack $(AWS_ARGS) --stack-name $(STACK_NAME)
	$(AWS) cloudformation wait stack-delete-complete $(AWS_ARGS) --stack-name $(STACK_NAME)
	$(AWS) s3 rm s3://$(ARTIFACT_BUCKET) $(AWS_ARGS) --recursive
	$(AWS) s3api delete-bucket $(AWS_ARGS) --bucket $(ARTIFACT_BUCKET)
	$(AWS) logs delete-log-group $(AWS_ARGS) --log-group-name /aws/lambda/$(STACK_NAME)-handler 2>/dev/null || true
