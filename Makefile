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
BOOKING_TABLE ?= jemlih-booking
CUSTOMER_TABLE ?= jemlih-customer
DOCUMENT_BUCKET ?= jemlih-customer-documents
FLEET_URL ?= http://localhost:$(or $(FLEET_PORT),8081)
RENTAL_ID ?= default
EVENT_BUS_NAME ?= jemlih
export FLOCI_PORT AWS_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY JEMLIH_AWS_ENDPOINT FLEET_TABLE BOOKING_TABLE CUSTOMER_TABLE DOCUMENT_BUCKET FLEET_URL RENTAL_ID EVENT_BUS_NAME

TOFU_VARS := -var aws_endpoint=$(JEMLIH_AWS_ENDPOINT) -var region=$(AWS_REGION) \
	-var fleet_table=$(FLEET_TABLE) -var booking_table=$(BOOKING_TABLE) -var customer_table=$(CUSTOMER_TABLE) \
	-var document_bucket=$(DOCUMENT_BUCKET) -var rental_id=$(RENTAL_ID) -var event_bus_name=$(EVENT_BUS_NAME)

# Exports OpenTofu outputs the services and tests need; tokens always name floci as their issuer.
TOFU_ENV = export COGNITO_USER_POOL_ID="$$($(TOFU) output -raw user_pool_id)" \
	EVENT_ARCHIVE_BUCKET="$$($(TOFU) output -raw event_archive_bucket)" \
	COGNITO_CLIENT_ID="$$($(TOFU) output -raw web_client_id)" && \
	export COGNITO_ISSUER="http://floci:4566/$$COGNITO_USER_POOL_ID" \
	COGNITO_JWKS_URL="$(JEMLIH_AWS_ENDPOINT)/$$COGNITO_USER_POOL_ID/.well-known/jwks.json"

# Lambda packages as name=package; each builds to build/<name>.zip
LAMBDAS := account-postconfirm=./services/account/cmd/postconfirm events-relay=./events/cmd/relay

.PHONY: help up down test vet fmt lambdas seed-owner

help: ## List available commands
	@grep -E '^[a-z-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-11s %s\n", $$1, $$2}'

up: lambdas ## Start the emulator, apply infrastructure and start the services
	docker compose up -d --wait floci
	$(TOFU) init -input=false -no-color > /dev/null
	$(TOFU) apply -input=false -auto-approve $(TOFU_VARS)
	$(TOFU_ENV) && docker compose up -d --build --wait fleet account booking customer

down: ## Stop everything and delete emulator data and local infrastructure state
	docker compose down --volumes --remove-orphans
	@# Lambda containers are started by the emulator, outside compose
	docker ps -aq --filter name=floci-aws- | xargs -r docker rm -f > /dev/null
	rm -f infra/local/terraform.tfstate infra/local/terraform.tfstate.backup

test: ## Run the test suite against the running environment (run `make up` first)
	$(TOFU_ENV) && go test ./... -count=1

seed-owner: ## Create the first Owner: make seed-owner EMAIL=you@example.com [PASSWORD=...]
	@$(TOFU_ENV) && SEED_OWNER_PASSWORD='$(PASSWORD)' go run ./services/account/cmd/seed -email "$(or $(EMAIL),owner@jemlih.local)"

lambdas: ## Build the Lambda packages into build/
	@mkdir -p build
	@for entry in $(LAMBDAS); do \
		fn=$${entry%%=*}; pkg=$${entry#*=}; \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -tags lambda.norpc \
			-o build/$$fn/bootstrap $$pkg && \
		(cd build/$$fn && rm -f ../$$fn.zip && zip -q -X ../$$fn.zip bootstrap) || exit 1; \
	done

vet: ## Run static checks
	go vet ./...

fmt: ## Format Go and OpenTofu code
	gofmt -w pkg services events
	$(TOFU) fmt
