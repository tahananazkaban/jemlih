SHELL := /bin/bash
.DEFAULT_GOAL := help

TOFU := tofu -chdir=infra/local

# Optional overrides of the defaults below, see .env.example
-include .env

FLOCI_PORT ?= 4566
AWS_REGION ?= us-east-1
AWS_ACCESS_KEY_ID ?= test
AWS_SECRET_ACCESS_KEY ?= test
JEMLIH_AWS_ENDPOINT ?= http://localhost:$(FLOCI_PORT)
FLEET_TABLE ?= jemlih-fleet
export FLOCI_PORT AWS_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY JEMLIH_AWS_ENDPOINT FLEET_TABLE

TOFU_VARS := -var aws_endpoint=$(JEMLIH_AWS_ENDPOINT) -var region=$(AWS_REGION) -var fleet_table=$(FLEET_TABLE)

.PHONY: help up down test vet fmt

help: ## List available commands
	@grep -E '^[a-z-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-8s %s\n", $$1, $$2}'

up: ## Start the emulator, apply infrastructure and start the services
	docker compose up -d --wait floci
	$(TOFU) init -input=false -no-color > /dev/null
	$(TOFU) apply -input=false -auto-approve $(TOFU_VARS)
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
