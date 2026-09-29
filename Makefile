.PHONY: all build build_demo fmt vet test clean

# bin/ is the canonical build output. Tests and, later, the e2e driver consume
# the binaries from here rather than rebuilding via `go build`.
BIN_DIR := $(CURDIR)/bin
GL_BIN := $(BIN_DIR)/gl
STUB_BIN := $(BIN_DIR)/exec_env_stub
LOGDEMO_BIN := $(BIN_DIR)/logdemo
TASKDEMO_BIN := $(BIN_DIR)/taskdemo

all: fmt vet build

# `build` is phony so `go build` runs unconditionally — Go's own build cache
# decides what actually needs recompiling.
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(GL_BIN) ./cmd/gl
	go build -o $(STUB_BIN) ./cmd/exec_env_stub

# Demo binaries are not needed for tests; build them on demand.
build_demo:
	@mkdir -p $(BIN_DIR)
	go build -o $(LOGDEMO_BIN) ./cmd/logdemo
	go build -o $(TASKDEMO_BIN) ./cmd/taskdemo

fmt:
	go fmt ./...

vet:
	go vet ./...

# Tests consume the pre-built stub via GL_EXEC_ENV_STUB; they never invoke
# `go build` themselves. Tests needing the stub read that variable and skip
# with a clear message if it is unset. `-count=1` disables the result cache so
# every package is exercised end-to-end.
test: build
	GL_EXEC_ENV_STUB=$(STUB_BIN) GL_GL_BIN=$(GL_BIN) go test -count=1 ./...

clean:
	rm -rf $(BIN_DIR)
