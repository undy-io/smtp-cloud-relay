.PHONY: run build test image

IMAGE ?= smtp-cloud-relay:dev

run:
	go run ./cmd/relay

build:
	go build ./...

test:
	go test ./...

image:
	docker build -t $(IMAGE) .
