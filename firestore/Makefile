JAVA_HOME ?= /opt/homebrew/opt/openjdk@21
EMULATOR = PATH="$(JAVA_HOME)/bin:$$PATH" firebase emulators:exec --project demo-billing --only firestore

.PHONY: test test-emulator

test:
	go test ./...

test-emulator:
	$(EMULATOR) "go test -race -count=1 ./..."
