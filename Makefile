include .env
export

MIGRATE := migrate -path migrations -database "$(DATABASE_URL)"

.PHONY: run build test lint migrate-up migrate-down migrate-version migrate-create

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test -race ./...

lint:
	golangci-lint run ./...

migrate-up:
	$(MIGRATE) up

migrate-down:
	$(MIGRATE) down 1

migrate-version:
	$(MIGRATE) version

# usage: make migrate-create name=add_pull_requests_table
migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)
