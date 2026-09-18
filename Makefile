GO_CACHE ?= /tmp/streamtool-go-cache
GO_MOD_CACHE ?= /tmp/streamtool-go-mod
GO_ENV = GOCACHE=$(GO_CACHE) GOMODCACHE=$(GO_MOD_CACHE)

.PHONY: check test race vet build up down logs integration e2e e2e-fast failure security soak soak-24h

check: test vet
	python3 tests/security/repository_hygiene.py
	python3 tests/selfhost/verify.py
	python3 tests/selfhost/public_address_test.py
	python3 tests/selfhost/installer_handoff_test.py
	python3 tests/selfhost/archive_test.py
	python3 tests/selfhost/updater_test.py
	python3 tests/selfhost/updater_bridge_test.py
	python3 tests/selfhost/installer_preflight_test.py
	python3 tests/selfhost/installer_job_test.py
	$(GO_ENV) python3 tests/selfhost/application_install_test.py
	python3 tests/selfhost/host_idle_test.py
	python3 tests/selfhost/web_replace_test.py
	python3 tests/selfhost/disk_reserve_test.py
	python3 tests/selfhost/security_scan_test.py
	python3 tests/selfhost/media_scan_test.py
	python3 tests/control/fixture_isolation_test.py
	$(GO_ENV) python3 tests/selfhost/release_prepare_test.py
	bash -n infra/selfhost/install.sh infra/selfhost/update.sh infra/selfhost/package.sh tests/selfhost/linux_host/provision.sh infra/release/scan.sh infra/ci/run.sh
	@test -z "$$(gofmt -l backend/cmd backend/internal)" || (echo "run gofmt on:"; gofmt -l backend/cmd backend/internal; exit 1)
	@docker compose config --quiet
	@docker compose -f compose.control.yml config --quiet
	@docker compose -f compose.cabinet.yml config --quiet

test:
	$(GO_ENV) go -C backend test ./...

race:
	$(GO_ENV) go -C backend test -race ./internal/...

vet:
	$(GO_ENV) go -C backend vet ./...

build:
	docker compose build api worker publisher

integration:
	./tests/integration/control_plane.sh

up:
	docker compose up --build -d

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f worker edge receiver

e2e:
	STREAM_SMOKE_SECONDS=300 ./tests/e2e/run.sh

e2e-fast:
	STREAM_SMOKE_SECONDS=5 ./tests/e2e/run.sh

soak:
	STREAM_SMOKE_SECONDS=3600 ./tests/e2e/run.sh

soak-24h:
	SOAK_HOURS=24 ./tests/load/soak.sh

failure:
	./tests/failure/run.sh

security:
	GOCACHE=$(GO_CACHE) GOMODCACHE=$(GO_MOD_CACHE) ./tests/security/scan.sh

.PHONY: security-images
security-images:
	bash tests/security/images.sh

.PHONY: security-network
security-network:
	bash tests/security/network.sh

.PHONY: control-up control-down control-e2e
.PHONY: cabinet-up cabinet-down
cabinet-up:
	python3 infra/local/cabinet-secrets.py
	docker compose -f compose.cabinet.yml up -d --build --remove-orphans

cabinet-down:
	docker compose -f compose.cabinet.yml down

control-up:
	./tests/control/certificates.sh
	docker compose -f compose.control.yml build api agent worker publisher
	docker compose -f compose.control.yml up -d api agent edge receiver

control-down:
	./tests/control/down.sh

control-e2e: control-up
	python3 ./tests/control/single_node.py

.PHONY: control-db-test
control-db-test:
	./tests/control/database.sh

.PHONY: users-test android-check
users-test:
	./tests/control/database.sh ./internal/application

android-check:
	cd apps/android && ./gradlew :app:assembleDebug :app:lintDebug :app:testDebugUnitTest

.PHONY: android-device-test
android-device-test:
	cd apps/android && ./gradlew :app:connectedDebugAndroidTest

.PHONY: control-video-e2e
control-video-e2e:
	./tests/control/fallback_video.sh

.PHONY: encoder-experiment
encoder-experiment:
	docker build --target fixture-publisher -t streamtool-control-publisher:latest backend
	docker build -t streamtool-encoder-experiment:local tests/encoder
	python3 tests/encoder/run.py --protocol srt
	python3 tests/encoder/run.py --protocol rtmp --pattern snow --source-preset ultrafast

.PHONY: selfhost-package
selfhost-package:
	bash infra/selfhost/package.sh $(VERSION)

.PHONY: control-multistream-e2e
control-multistream-e2e:
	CONTROL_TEST_MULTISTREAM_ONLY=1 $(MAKE) control-e2e
