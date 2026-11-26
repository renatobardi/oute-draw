# Local development needs Go, Node and a Postgres with a schema the role can write to.
DEV_DB ?= postgres://app_draw_dev:dev@127.0.0.1:5433/postgres

.PHONY: web build test check dev

web: ## build the web app into web/dist (embedded by the Go binary)
	cd web && npm ci && npm run build

build: web ## the single binary, web app included
	CGO_ENABLED=0 go build -trimpath -o bin/oute-draw ./cmd/oute-draw

test: ## Go tests; the server suite needs DRAW_TEST_DATABASE_URL
	DRAW_TEST_DATABASE_URL=$${DRAW_TEST_DATABASE_URL:-$(DEV_DB)} go test ./...

check: ## everything CI checks
	test -z "$$(gofmt -l .)"
	go vet ./...
	cd web && npm run check
	$(MAKE) test

dev: ## API on :8080 signed in as an admin; run `npm run dev` in web/ for the UI on :5173
	DRAW_AUTH_MODE=dev DRAW_DATABASE_URL=$(DEV_DB) DRAW_LISTEN=127.0.0.1:8080 go run ./cmd/oute-draw
