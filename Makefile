BIN := bin/wehelpd

.PHONY: build test vet run migrate db-migrate seed compose-up compose-down ios ios-build clean

build:
	go build -o $(BIN) ./cmd/wehelp

test:
	go test ./...

vet:
	go vet ./...

run: build
	$(BIN) serve

migrate: build
	$(BIN) migrate

# Containerized Flyway — no local install needed. Starts db if down.
db-migrate:
	docker compose -f deploy/docker-compose.yml run --rm migrate

seed: build
	$(BIN) seed

compose-up:
	docker compose -f deploy/docker-compose.yml up --build -d

compose-down:
	docker compose -f deploy/docker-compose.yml down

ios:
	cd ios && xcodegen generate
	open ios/WeHelp.xcodeproj

ios-build:
	cd ios && xcodegen generate
	xcodebuild -project ios/WeHelp.xcodeproj -scheme WeHelp \
		-destination 'generic/platform=iOS Simulator' \
		CODE_SIGNING_ALLOWED=NO build

clean:
	rm -rf bin ios/WeHelp.xcodeproj
