IMAGE := dfhack-proto-gen
DFHACK_VERSION ?=

DOCKER_USER := $(shell docker info 2>/dev/null | grep -q rootless || echo --user $$(id -u):$$(id -g))

.PHONY: proto
proto:
	docker build -t $(IMAGE) -f scripts/proto.Dockerfile .
	docker run --rm \
	  --env HOME=/tmp \
	  --env DFHACK_VERSION=$(DFHACK_VERSION) \
	  $(DOCKER_USER) \
	  --volume $(CURDIR):/workspace \
	  --workdir /workspace \
	  $(IMAGE)
	@command -v go >/dev/null 2>&1 && go mod tidy || \
	  echo "go not found on PATH; skipping 'go mod tidy'"
