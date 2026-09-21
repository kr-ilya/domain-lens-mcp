COMPOSE ?= docker compose

.DEFAULT_GOAL := up

.PHONY: up stop down restart logs ps build

## Build (if needed) and start the container in the background.
up:
	$(COMPOSE) up -d --build

## Stop the container without removing it.
stop:
	$(COMPOSE) stop

## Stop and remove the container, network and anonymous volumes.
down:
	$(COMPOSE) down

## Restart a stopped container.
restart:
	$(COMPOSE) restart

## Follow the container logs.
logs:
	$(COMPOSE) logs -f

## Show the container status.
ps:
	$(COMPOSE) ps

## Build the image without starting the container.
build:
	$(COMPOSE) build
