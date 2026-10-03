FUNCTIONS := inspector trail
LAMBDA_ENV := GOOS=linux GOARCH=arm64 CGO_ENABLED=0

.PHONY: build test test-go test-dispatcher test-infra synth deploy destroy check-public hooks clean FORCE

build: $(foreach f,$(FUNCTIONS),build/$(f)/bootstrap)

build/%/bootstrap: FORCE
	$(LAMBDA_ENV) go build -tags lambda.norpc -trimpath -o $@ ./functions/$*

test: test-go test-dispatcher test-infra

test-go:
	go vet ./...
	go test ./...

functions/dispatcher/node_modules: functions/dispatcher/package-lock.json
	cd functions/dispatcher && npm ci
	touch $@

infra/node_modules: infra/package-lock.json
	cd infra && npm ci
	touch $@

test-dispatcher: functions/dispatcher/node_modules
	cd functions/dispatcher && npm run typecheck && npm test

test-infra: build functions/dispatcher/node_modules infra/node_modules
	cd infra && npm run typecheck && npm test

synth: build functions/dispatcher/node_modules infra/node_modules
	cd infra && cdk synth > /dev/null && echo "synth ok: infra/cdk.out"

# Deploys into the account of your current AWS credentials.
deploy: synth
	cd infra && cdk deploy --outputs-file cdk-outputs.json

destroy:
	cd infra && cdk destroy

check-public:
	scripts/check-public.sh

hooks:
	git config core.hooksPath .githooks

clean:
	rm -rf build infra/cdk.out
