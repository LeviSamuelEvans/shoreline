set dotenv-load := true

default:
    @just --list

build:
    go build ./cmd/sl

install:
    go install ./cmd/sl
    @bin="$(go env GOBIN)"; if [ -z "$bin" ]; then bin="$(go env GOPATH)/bin"; fi; echo "installed sl to $bin"; echo "make sure $bin is on your PATH"

install-hooks:
    install -m 755 scripts/pre-commit-secrets .git/hooks/pre-commit
    @echo "installed pre-commit secret scan hook"

run *args:
    go run ./cmd/sl {{args}}

player:
    go run ./cmd/sl player

test:
    go test ./...

fmt:
    gofmt -w cmd internal

tidy:
    go mod tidy

clean:
    rm -f sl
