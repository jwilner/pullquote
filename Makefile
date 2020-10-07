.PHONY: test fix lint

test:
	@go test ./...

fix:
	@golangci-lint run --fix

lint:
	@golangci-lint run
