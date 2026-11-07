.PHONY: test smoke integration playwright-integration full-integration build release fmt
export VERSION
INTEGRATION_TOOLS := uv,go,gh,btop,ripgrep,jq,fd,shellcheck,ast-grep,neovim,lazygit,delta,direnv,just,zoxide

test:
	docker build --target test -t skytools-test .

smoke:
	docker build --target smoke -t skytools-smoke .

# Real downloads in a disposable, non-root home. The second ensure is offline.
integration: smoke
	docker build --target runtime -t skytools-runtime .
	@test_volume=$$(docker volume create); \
	  trap 'docker volume rm "$$test_volume" >/dev/null' EXIT; \
	  docker run --rm -v "$$test_volume:/home/dev" skytools-runtime ensure --only $(INTEGRATION_TOOLS) && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" skytools-runtime ensure --only $(INTEGRATION_TOOLS) && \
	  docker run --rm --network none -v "$$test_volume:/home/dev" -v "$(CURDIR)/scripts:/checks:ro" \
	    --entrypoint sh skytools-runtime /checks/check-status.sh --only $(INTEGRATION_TOOLS)

playwright-integration: smoke
	docker build --target runtime -t skytools-runtime .
	docker build -f scripts/playwright.Dockerfile -t skytools-playwright-test .
	@test_volume=$$(docker volume create); \
	  trap 'docker volume rm "$$test_volume" >/dev/null' EXIT; \
	  docker run --rm --init -v "$$test_volume:/home/dev" skytools-playwright-test ensure --only playwright-cli && \
	  docker run --rm --init --network none -v "$$test_volume:/home/dev" \
	    --entrypoint sh skytools-playwright-test /checks/check-playwright.sh

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
