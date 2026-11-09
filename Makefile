.PHONY: build install test integration ssh-integration check

build:
	go build -o bin/berth .

install:
	go install .

test:
	go test -race ./...

integration:
	go test -tags integration -count=1 -v -run '^TestIntegration' -timeout 15m .

ssh-integration:
	go test -race -tags sshintegration -count=1 -v -run '^TestSSHIntegration$$' -timeout 5m .

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...
	go build -o /dev/null .
