SNAPSHOT_NAME ?= claude-playwright
SNAPSHOT_TAG ?= 1

.PHONY: up down logs build-snapshot push-snapshot example clean

up:
	bash bootstrap.sh

down:
	docker compose -f $$HOME/src/daytona/docker/docker-compose.yaml down

logs:
	docker compose -f $$HOME/src/daytona/docker/docker-compose.yaml logs -f --tail=100

build-snapshot:
	docker build --platform=linux/amd64 -t $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) sandbox-image

push-snapshot: build-snapshot
	# Pushes the local image into Daytona's internal registry as a snapshot.
	# Requires the `daytona` CLI to be logged in (daytona login).
	daytona snapshot push $(SNAPSHOT_NAME):$(SNAPSHOT_TAG)

example:
	uv run --with daytona python examples/spawn.py

clean:
	docker rmi $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) || true
