SHELL := /bin/bash
.DEFAULT_GOAL := help

TOFU := tofu -chdir=infra/local

export AWS_REGION ?= us-east-1
export AWS_ACCESS_KEY_ID ?= test
export AWS_SECRET_ACCESS_KEY ?= test
export JEMLIH_AWS_ENDPOINT ?= http://localhost:4566

.PHONY: help up down test vet fmt

help: ## List available commands
	@grep -E '^[a-z-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-8s %s\n", $$1, $$2}'

up: ## Start the emulator, apply infrastructure and start the services
	docker compose up -d --wait floci
	$(TOFU) init -input=false -no-color > /dev/null
	$(TOFU) apply -input=false -auto-approve -var aws_endpoint=$(JEMLIH_AWS_ENDPOINT)
	docker compose up -d --build --wait fleet

down: ## Stop everything and delete emulator data and local infrastructure state
	docker compose down --volumes --remove-orphans
	rm -f infra/local/terraform.tfstate infra/local/terraform.tfstate.backup

test: ## Run the test suite against the running emulator (run `make up` first)
	go test ./... -count=1

vet: ## Run static checks
	go vet ./...

fmt: ## Format Go and OpenTofu code
	gofmt -w pkg services
	$(TOFU) fmt
