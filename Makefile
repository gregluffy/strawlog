IMAGE ?= strawlog:latest

.PHONY: ui build image test dev
ui:
	cd web && npm ci && npm run build

build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o strawlog ./cmd/strawlog

image:
	docker build -t $(IMAGE) .

test:
	go vet ./... && go test ./...

dev: # hub on :8080 reading this machine natively (no /host), UI via `cd web && npm run dev`
	STRAWLOG_DATA=./.devdata STRAWLOG_HOST_ROOT= go run ./cmd/strawlog hub
