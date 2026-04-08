# Makefile for netbench_go
GO_PATH = ../../go/bin/go
BUILD_PATH = ./build

# Go sources
netbench_go_server_srcs = ./server/netbench_server.go ./server/synthetic_worker.go
netbench_go_client_srcs = ./client/netbench_client.go

# Build targets
netbench_go_server_tgt = $(BUILD_PATH)/netbench_server
netbench_go_client_tgt = $(BUILD_PATH)/netbench_client

# Probably this make file needs proper dependency tracking

all: $(netbench_go_server_tgt) $(netbench_go_client_tgt)

$(netbench_go_server_tgt): $(netbench_go_server_srcs)
	mkdir -p $(BUILD_PATH)
	$(GO_PATH) build -o $@ $<

$(netbench_go_client_tgt): $(netbench_go_client_srcs)
	mkdir -p $(BUILD_PATH)
	$(GO_PATH) build -o $@ $<

clean:
	rm -rf $(BUILD_PATH)
