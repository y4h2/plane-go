DATABASE_URL ?= postgres://plane:plane@localhost:4010/plane_go?sslmode=disable
MIGRATIONS := internal/db/migrations

.PHONY: tidy generate migrate-up migrate-down build run test check
tidy:
	go mod tidy

generate:
	sqlc generate

migrate-up:
	goose -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" down

build:
	go build ./...

run:
	go run ./cmd/server

test:
	go test ./...

# L1 gate: everything must pass.
check: build
	go vet ./...
	go test ./...
