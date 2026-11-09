.PHONY: build install test integration check

build:
	go build -o bin/berth .

install:
	go install .

test:
	go test -race ./...

integration:
	BERTH_INTEGRATION=1 go test -v -run Integration -timeout 15m ./...

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...
	go build -o /dev/null .
