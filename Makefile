.PHONY: all build build_demo fmt vet test e2e doc serve-doc stop-doc clean

# bin/ is the canonical build output. Tests and, later, the e2e driver consume
# the binaries from here rather than rebuilding via `go build`.
BIN_DIR := $(CURDIR)/bin
GL_BIN := $(BIN_DIR)/gl
STUB_BIN := $(BIN_DIR)/exec_env_stub
LOGDEMO_BIN := $(BIN_DIR)/logdemo
TASKDEMO_BIN := $(BIN_DIR)/taskdemo

DOC_DIR := $(CURDIR)/doc
DOC_BUILD := $(DOC_DIR)/book
DOC_PID := $(DOC_DIR)/.serve.pid
DOC_LOG := $(DOC_DIR)/.serve.log

# mdbook-mermaid is typically installed via `cargo install` into ~/.cargo/bin,
# which is not always on the interactive PATH. Make it discoverable so the
# mermaid preprocessor runs and diagrams render.
DOC_PATH := $(HOME)/.cargo/bin:$(PATH)

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

# End-to-end driver: consumes whatever is in bin/. The dependency on `build`
# guarantees freshness; the script itself never rebuilds. It prepares a staging
# conf-dir (import + lockfiles) and builds a rootfs entirely from source.
e2e: build
	GL_EXEC_ENV_STUB=$(STUB_BIN) GL_GL_BIN=$(GL_BIN) tests/full_build_test.sh

doc:
	PATH="$(DOC_PATH)" mdbook build $(DOC_DIR)

# `make serve-doc` launches `mdbook serve` detached, recording the PID. A
# second invocation while a previous server is alive is a no-op; use
# `make stop-doc` to terminate.
serve-doc:
	@if [ -f $(DOC_PID) ] && kill -0 $$(cat $(DOC_PID)) 2>/dev/null; then \
		echo "mdbook serve already running (pid $$(cat $(DOC_PID))); see $(DOC_LOG)"; \
	else \
		rm -f $(DOC_PID); \
		PATH="$(DOC_PATH)" nohup mdbook serve $(DOC_DIR) >$(DOC_LOG) 2>&1 & echo $$! >$(DOC_PID); \
		echo "mdbook serve started (pid $$(cat $(DOC_PID))); log: $(DOC_LOG)"; \
	fi

stop-doc:
	@if [ ! -f $(DOC_PID) ]; then \
		echo "no $(DOC_PID); nothing to stop"; \
	else \
		PID=$$(cat $(DOC_PID)); \
		if kill -0 $$PID 2>/dev/null; then \
			kill -TERM $$PID && echo "sent SIGTERM to mdbook serve (pid $$PID)"; \
		else \
			echo "pid $$PID from $(DOC_PID) is not alive"; \
		fi; \
		rm -f $(DOC_PID); \
	fi

clean:
	rm -rf $(BIN_DIR) $(DOC_BUILD) $(DOC_PID) $(DOC_LOG)
