.PHONY: build test vet typecheck secretscan test-gadak docker scenario

build:
	npm run build
	CGO_ENABLED=0 go build -trimpath -o bin/issuetap ./cmd/issuetap

vet:
	go vet ./...

test:
	go test ./... -count=1

typecheck:
	npm run typecheck

secretscan:
	bash scripts/secretscan.sh

test-gadak:
	ISSUETAP_REQUIRE_GADAK=1 ISSUETAP_GADAK_SRC=/Users/hckim/repo/gadak \
		go test ./internal/conformance -count=1 -timeout 180s

scenario:
	go run ./cmd/issuetap scenario run examples/scenarios/locale-ko-name-trap.yaml
	go run ./cmd/issuetap scenario run examples/scenarios/credential-revoked.yaml
	go run ./cmd/issuetap scenario run examples/scenarios/rate-limit-burst.yaml
	go run ./cmd/issuetap scenario run examples/scenarios/confluence-401-stops-watch.yaml

docker:
	docker build -t issuetap:local .
