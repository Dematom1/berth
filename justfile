# List development recipes.
default:
    @just --list

build:
    go build -o bin/berth .

cross:
    #!/usr/bin/env bash
    set -euo pipefail
    for os in linux darwin; do
        for arch in amd64 arm64; do
            GOOS=$os GOARCH=$arch go build -o /dev/null .
        done
    done

install:
    go install .

test:
    go test -race -timeout 10m ./...

fmt:
    gofmt -w .

fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    output=$(gofmt -l .)
    if [ -n "$output" ]; then
        printf 'Files not formatted:\n%s\n' "$output"
        exit 1
    fi

vet:
    go vet ./...

lint: fmt-check vet

check: lint test
    go build -o /dev/null .

deps-check:
    go mod verify
    go mod tidy
    git diff --exit-code -- go.mod go.sum

vulncheck:
    go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

formal:
    go test -race -tags formal -count=1 -timeout 1m ./...
    python3 verification/check.py
    python3 verification/mutations.py

integration:
    go test -tags integration -count=1 -v -run '^TestIntegration' -timeout 15m ./...

ssh-integration:
    go test -race -tags sshintegration -count=1 -v -run '^TestSSHIntegration$' -timeout 5m ./...

release-check:
    goreleaser check

release-snapshot: release-check
    goreleaser release --snapshot --clean --skip=publish
