GOLANG ?= go

.PHONY: test
test:
	$(GOLANG) test -race ./...

.PHONY: bench
bench:
	$(GOLANG) test -bench=. -benchmem -run='^$$' ./...

.PHONY: examples
examples:
	for dir in examples/*/; do \
		(cd $$dir && $(GOLANG) build ./...) || exit 1; \
	done
