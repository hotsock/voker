GOLANG ?= go

.PHONY: test
test:
	$(GOLANG) test ./...
