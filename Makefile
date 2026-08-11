SHELL := /bin/bash

ROUTER_DIR := agent-platform/router
DASHBOARD_API_DIR := agent-platform/dashboard-api

.PHONY: build test vet lint fmt tidy run-router run-dashboard-api compose-up compose-down clean

## build: compile both Go services
build:
	cd $(ROUTER_DIR) && go build ./...
	cd $(DASHBOARD_API_DIR) && go build ./...

## test: run unit tests for both Go services
test:
	cd $(ROUTER_DIR) && go test ./...
	cd $(DASHBOARD_API_DIR) && go test ./...

## vet: run go vet on both Go services
vet:
	cd $(ROUTER_DIR) && go vet ./...
	cd $(DASHBOARD_API_DIR) && go vet ./...

## lint: run golangci-lint on both Go services (see .golangci.yml)
lint:
	cd $(ROUTER_DIR) && golangci-lint run ./...
	cd $(DASHBOARD_API_DIR) && golangci-lint run ./...

## fmt: run gofmt across both Go services
fmt:
	cd $(ROUTER_DIR) && gofmt -l -w .
	cd $(DASHBOARD_API_DIR) && gofmt -l -w .

## tidy: run go mod tidy for both Go services
tidy:
	cd $(ROUTER_DIR) && go mod tidy
	cd $(DASHBOARD_API_DIR) && go mod tidy

## run-router: run the router service locally (needs NATS/Ollama reachable)
run-router:
	cd $(ROUTER_DIR) && go run ./cmd/router

## run-dashboard-api: run the dashboard API locally (needs NATS reachable)
run-dashboard-api:
	cd $(DASHBOARD_API_DIR) && go run ./cmd/dashboard-api

## compose-up: start the full stack via podman compose
compose-up:
	cd agent-platform && podman compose up --build

## compose-down: stop the full stack
compose-down:
	cd agent-platform && podman compose down

## clean: remove stray local build binaries
clean:
	rm -f $(ROUTER_DIR)/router $(DASHBOARD_API_DIR)/dashboard-api
