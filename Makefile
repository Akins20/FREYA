BINARY := freya
PKG    := ./cmd/freya

# Build output goes to ./bin, which is gitignored.
BIN_DIR := bin

.PHONY: build run test vet fmt clean install tidy check

build: ## Compile the binary
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -o $(BIN_DIR)/$(BINARY) $(PKG)
	@echo "built $(BIN_DIR)/$(BINARY)"

run: build ## Build and start the REPL
	@./$(BIN_DIR)/$(BINARY)

offline: build ## Start the REPL with the no-key mock provider
	@./$(BIN_DIR)/$(BINARY) -provider mock

test: ## Run all tests
	go test ./...

vet: ## Static analysis
	go vet ./...

fmt: ## Format all Go source
	go fmt ./...

tidy: ## Tidy the module graph
	go mod tidy

check: fmt vet test build ## Everything, in order

install: build ## Install to ~/.local/bin
	@mkdir -p $(HOME)/.local/bin
	cp $(BIN_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "installed to $(HOME)/.local/bin/$(BINARY)"

clean: ## Remove build output
	rm -rf $(BIN_DIR)
