.PHONY: test

test:
	go clean -testcache && go test -race -count=1 ./...
