.PHONY: test smoke integration full-integration build release fmt
export VERSION

test:
	docker build --target test -t skytools-test .

smoke:
	docker build --target smoke -t skytools-smoke .

# Real downloads in a disposable, non-root home. The second ensure is offline.
integration: smoke
	docker build --target runtime -t skytools-runtime .
	@test_volume=$$(docker volume create); \
	  trap 'docker volume rm "$$test_volume" >/dev/null' EXIT; \
	  docker run --rm -v "$$test_volume:/home/dev" skytools-runtime ensure --only uv,go,gh && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" skytools-runtime ensure --only uv,go,gh && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" -v "$(CURDIR)/scripts:/checks:ro" \
	    --entrypoint sh skytools-runtime /checks/check-status.sh --only uv,go,gh

full-integration: smoke
	docker build --target full -t skytools-full .
	@test_volume=$$(docker volume create); \
	  trap 'docker volume rm "$$test_volume" >/dev/null' EXIT; \
	  docker run --rm -v "$$test_volume:/home/dev" skytools-full ensure && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" skytools-full ensure && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" -v "$(CURDIR)/scripts:/checks:ro" \
	    --entrypoint sh skytools-full /checks/check-status.sh

build:
	docker build --target binary --output type=local,dest=dist .

release:
	docker build --target source -t skytools-source .
	docker run --rm --entrypoint sh -e VERSION -v "$(CURDIR)/dist:/out" -v "$(CURDIR)/scripts:/scripts:ro" skytools-source /scripts/release.sh

fmt:
	docker build --target source -t skytools-source .
	docker run --rm -v "$(CURDIR):/src" skytools-source gofmt -w cmd internal
