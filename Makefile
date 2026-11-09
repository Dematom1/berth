.PHONY: build install test integration check

build:
	go build -o bin/berth .

install:
	go install .

test:
	go test -race ./...

integration:
	go test -tags integration -count=1 -v -run '^TestIntegration' -timeout 15m .

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...
	go build -o /dev/null .
