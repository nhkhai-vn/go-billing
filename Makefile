MODULES = . stripe postgres firestore
TOOL = go tool -modfile=$(CURDIR)/tools/go.mod

.PHONY: format lint test test-emulator

format:
	for m in $(MODULES); do (cd $$m && $(TOOL) golangci-lint fmt) || exit 1; done

lint:
	cd tools && go mod tidy -diff
	for m in $(MODULES); do (cd $$m && go mod tidy -diff && $(TOOL) golangci-lint run ./... && $(TOOL) govulncheck ./...) || exit 1; done

# The postgres tests run when BILLING_POSTGRES_URL is set; the firestore tests need test-emulator.
test:
	for m in $(MODULES); do (cd $$m && go test -race -cover ./...) || exit 1; done

test-emulator:
	$(MAKE) -C firestore test-emulator
