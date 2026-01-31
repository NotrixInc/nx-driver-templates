BIN_DIR=bin
BIN_NAME=driver

.PHONY: build
build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BIN_NAME) ./cmd/driver

.PHONY: package
package: build
	tar -czf driver-package.tgz manifest.json config.schema.json endpoints.json variables.schema.json capabilities.json events.json -C $(BIN_DIR) $(BIN_NAME)

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) driver-package.tgz
