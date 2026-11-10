.PHONY: build install test integration ssh-integration check formal

build:
	go build -o bin/dibs .

install:
	go install .

test:
	go test -race ./...

integration:
	go test -tags integration -count=1 -v -run '^TestIntegration' -timeout 15m .

ssh-integration:
	go test -race -tags sshintegration -count=1 -v -run '^TestSSHIntegration$$' -timeout 5m .

# Java 17+, Python 3, and Go. TLC is checksum-pinned by verification/check.py.
formal:
	go test -race -tags formal -count=1 -timeout 1m .
	python3 verification/check.py
	python3 verification/mutations.py

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...
	go build -o /dev/null .
