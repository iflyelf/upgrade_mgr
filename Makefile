VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# legacy 构建额外上报变体，服务端自升级时据此选择同样兼容旧内核的二进制。
LEGACY_LDFLAGS := $(LDFLAGS) -X main.variant=legacy

.PHONY: all build build-agent test vet fmt tidy cross agent-legacy test-agent-legacy agent-windows test-agent-windows clean run

all: build

## build: 编译控制台当前平台二进制
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/upgrade-mgr ./cmd/server

## build-agent: 编译 Agent 当前平台二进制
build-agent:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/upgrade-agent ./cmd/agent

## test: 运行单元测试（集成测试需 DATABASE_URL / CONSUL_ADDRESS）
test:
	go test ./...

## vet: 静态检查
vet:
	go vet ./...

## fmt: 格式化
fmt:
	gofmt -w .

## tidy: 整理依赖
tidy:
	go mod tidy

## cross: 交叉编译控制台与 Agent 多架构到 dist/
cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-mgr-linux-amd64 ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-mgr-linux-arm64 ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-mgr-linux-arm ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-agent-linux-arm64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-agent-linux-arm ./cmd/agent

## agent-legacy: 用 Go 1.23（最后一个支持内核 2.6.32 的版本）编译 Agent，兼容 CentOS 6
## Agent 只依赖标准库，借助独立 modfile 避开主 go.mod 的 go 1.26 要求
LEGACY_GO ?= go1.23.12

## agent-windows: 用 Go 1.20（最后支持 Windows Server 2008 R2 的版本）编译 Windows Agent
## 单套构建同时覆盖 Server 2008 R2 ~ Windows 11；唯一依赖 golang.org/x/sys（SCM）
WINDOWS_GO ?= go1.20.14
agent-windows:
	mkdir -p dist
	GOTOOLCHAIN=$(WINDOWS_GO) CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -modfile=build/agent-windows.mod -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-agent-windows-amd64.exe ./cmd/agent
	GOTOOLCHAIN=$(WINDOWS_GO) CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -modfile=build/agent-windows.mod -trimpath -ldflags "$(LDFLAGS)" -o dist/upgrade-agent-windows-arm64.exe ./cmd/agent

## test-agent-windows: 用 Go 1.20 交叉编译校验 Windows Agent（无法在 Linux 上运行 Windows 单测）
test-agent-windows:
	GOTOOLCHAIN=$(WINDOWS_GO) CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -modfile=build/agent-windows.mod ./cmd/agent
	GOTOOLCHAIN=$(WINDOWS_GO) CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet -modfile=build/agent-windows.mod ./cmd/agent ./internal/agent/...
agent-legacy:
	mkdir -p dist
	GOTOOLCHAIN=$(LEGACY_GO) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -modfile=build/agent-legacy.mod -trimpath -ldflags "$(LEGACY_LDFLAGS)" -o dist/upgrade-agent-linux-amd64-legacy ./cmd/agent
	GOTOOLCHAIN=$(LEGACY_GO) CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -modfile=build/agent-legacy.mod -trimpath -ldflags "$(LEGACY_LDFLAGS)" -o dist/upgrade-agent-linux-386-legacy ./cmd/agent
	GOTOOLCHAIN=$(LEGACY_GO) CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -modfile=build/agent-legacy.mod -trimpath -ldflags "$(LEGACY_LDFLAGS)" -o dist/upgrade-agent-linux-arm64-legacy ./cmd/agent

## test-agent-legacy: 用 Go 1.23 运行 Agent 相关测试，防止引入 1.24+ 才有的 API
test-agent-legacy:
	GOTOOLCHAIN=$(LEGACY_GO) go test -modfile=build/agent-legacy.mod ./cmd/agent ./internal/agent/... ./internal/protocol ./internal/pkg/signer ./internal/pkg/runner

## clean: 清理产物
clean:
	rm -rf bin dist

## run: 前台运行控制台
run: build
	./bin/upgrade-mgr -c deploy/config/config.yaml
