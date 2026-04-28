# LeeLens Backend — Go + Gin + Eino
# 独立后端项目，前端位于 ../leelens-frontend

.PHONY: setup build build-linux build-all run dev air clean init-config stop

PLATFORMS_LINUX := linux/amd64 linux/arm64
PLATFORMS_ALL := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

# 安装依赖与开发工具
setup:
	go mod tidy
	@command -v air >/dev/null 2>&1 || { echo "Installing air..."; go install github.com/air-verse/air@latest; }

# 初始化配置
init-config:
	@if [ ! -f config.yaml ]; then \
		cp config.yaml.example config.yaml; \
		echo "Created config.yaml — please fill in your LLM API key"; \
	else \
		echo "config.yaml already exists"; \
	fi

# 为当前平台构建
build: prepare-embed-agents
	@mkdir -p bin
	@GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) \
		CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/leelens ./cmd/server/
	@$(MAKE) cleanup-embed-agents

# Linux 跨平台构建
build-linux: prepare-embed-agents
	@mkdir -p bin
	@for platform in $(PLATFORMS_LINUX); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/}; \
		echo "Building $$GOOS/$$GOARCH..."; \
		GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/leelens-$$GOOS-$$GOARCH ./cmd/server/; \
	done
	@$(MAKE) cleanup-embed-agents

# 全平台构建
build-all: prepare-embed-agents
	@mkdir -p bin
	@for platform in $(PLATFORMS_ALL); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/}; \
		OUT="bin/leelens-$$GOOS-$$GOARCH"; \
		if [ "$$GOOS" = "windows" ]; then OUT="$$OUT.exe"; fi; \
		echo "Building $$GOOS/$$GOARCH..."; \
		GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 go build -ldflags "-s -w" -o $$OUT ./cmd/server/; \
	done
	@$(MAKE) cleanup-embed-agents

# 将 agents YAML 嵌入二进制（二进制首次运行时自释放到 ./agents）
prepare-embed-agents:
	@mkdir -p internal/assets/agents
	@rm -rf internal/assets/agents/*.yaml
	@cp agents/*.yaml internal/assets/agents/

cleanup-embed-agents:
	@rm -f internal/assets/agents/*.yaml

# 运行（生产模式）
run:
	./bin/leelens -v 6

# 开发模式：air 热重载（Windows 下若 rm -rf 不兼容，改用 dev）
air:
	air

# 开发模式：go run（Windows 兼容）
dev:
	go run ./cmd/server/ -v 6

# 清理
clean:
	rm -rf bin tmp internal/assets/agents/*.yaml

# 杀掉 :8080 上的进程
stop:
	@lsof -ti:8080 2>/dev/null | xargs kill -9 2>/dev/null || true
