package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"k8s.io/klog/v2"

	"gitee.com/li-yuyanglee/leelens-backend/config"
	"gitee.com/li-yuyanglee/leelens-backend/internal/assets"
	"gitee.com/li-yuyanglee/leelens-backend/internal/domain/writers"
	"gitee.com/li-yuyanglee/leelens-backend/internal/eventbus"
	"gitee.com/li-yuyanglee/leelens-backend/internal/handler"
	"gitee.com/li-yuyanglee/leelens-backend/internal/mcp"
	"gitee.com/li-yuyanglee/leelens-backend/internal/pkg/adkagents"
	"github.com/mark3labs/mcp-go/server"
	"github.com/gin-gonic/gin"
	"gitee.com/li-yuyanglee/leelens-backend/internal/pkg/database"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"gitee.com/li-yuyanglee/leelens-backend/internal/router"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service/orchestrator"
	syncservice "gitee.com/li-yuyanglee/leelens-backend/internal/service/sync"
	"gitee.com/li-yuyanglee/leelens-backend/internal/subscriber"
)

func main() {
	// 初始化 klog
	klog.InitFlags(nil)
	flag.Parse()
	defer klog.Flush()

	klog.V(6).Info("服务启动中...")

	cfg := config.GetConfig()

	if err := os.MkdirAll(cfg.Data.Dir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	if err := os.MkdirAll(cfg.Data.RepoDir, 0755); err != nil {
		log.Fatalf("Failed to create repo directory: %v", err)
	}

	// 释放内嵌的默认 agents 文件（如果不存在）
	if err := assets.ExtractAgents(cfg.Agent.Dir); err != nil {
		log.Fatalf("Failed to extract embedded agents: %v", err)
	}

	// 创建 skills 目录（如果不存在）
	if err := os.MkdirAll(cfg.Skill.Dir, 0755); err != nil {
		log.Fatalf("Failed to create skills directory: %v", err)
	}

	// 初始化数据库
	db, err := database.InitDB(cfg)

	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 初始化 Repository
	repoRepo := repository.NewRepoRepository(db)
	taskRepo := repository.NewTaskRepository(db)
	docRepo := repository.NewDocumentRepository(db)
	ratingRepo := repository.NewDocumentRatingRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	hintRepo := repository.NewHintRepository(db)
	taskUsageRepo := repository.NewTaskUsageRepository(db)
	syncTargetRepo := repository.NewSyncTargetRepository(db)
	syncEventRepo := repository.NewSyncEventRepository(db)
	incrementalHistoryRepo := repository.NewIncrementalUpdateHistoryRepository(db)
	userRequestRepo := repository.NewUserRequestRepository(db)
	agentVersionRepo := repository.NewAgentVersionRepository(db)
	chatSessionRepo := repository.NewChatSessionRepository(db)
	chatMessageRepo := repository.NewChatMessageRepository(db)
	chatToolCallRepo := repository.NewChatToolCallRepository(db)
	docVectorRepo := repository.NewDocumentVectorRepository(db)

	// 初始化 Service
	docService := service.NewDocumentService(cfg, docRepo, repoRepo, ratingRepo, nil)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo)
	taskUsageService := service.NewTaskUsageService(taskUsageRepo)
	userRequestService := service.NewUserRequestService(userRequestRepo, repoRepo)
	agentService := service.NewAgentService(agentVersionRepo, cfg.Agent.Dir)
	chatService := service.NewChatService(chatSessionRepo, chatMessageRepo, chatToolCallRepo)
	ragService := service.NewRAGService(cfg, docRepo, docVectorRepo, apiKeyRepo)

	//初始化系列Writer
	titleRewriter, err := writers.NewTitleRewriter(cfg, docRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize title rewriter service: %v", err)
	}
	docRewriter, err := writers.NewDocRewriter(cfg, docRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize doc rewriter service: %v", err)
	}

	userRequestWriter, err := writers.NewUserRequestWriter(cfg, hintRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize user request writer service: %v", err)
	}
	defaultWriter, err := writers.NewDefaultWriter(cfg, hintRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize document generator service: %v", err)
	}
	dbModelWriter, err := writers.NewDBModelWriter(cfg, hintRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize db model writer service: %v", err)
	}
	apiWriter, err := writers.NewAPIWriter(cfg, hintRepo, taskRepo, repoRepo)
	if err != nil {
		log.Fatalf("Failed to initialize api analyzer service: %v", err)
	}

	// 初始化目录分析服务
	tocWriter, err := writers.NewTocWriter(cfg, docRepo, repoRepo, taskRepo, hintRepo)
	if err != nil {
		log.Fatalf("Failed to initialize directory analyzer service: %v", err)
	}

	incrementalWriter, err := writers.NewIncrementalWriter(cfg, repoRepo, taskRepo, hintRepo, docRepo, incrementalHistoryRepo)
	if err != nil {
		log.Fatalf("Failed to initialize incremental writer service: %v", err)
	}
	//初始化系列Writer结束

	taskService := service.NewTaskService(cfg, taskRepo, repoRepo, docService)
	taskService.AddWriters(userRequestWriter)
	taskService.AddWriters(defaultWriter)
	taskService.AddWriters(dbModelWriter)
	taskService.AddWriters(apiWriter)
	taskService.AddWriters(titleRewriter)
	taskService.AddWriters(docRewriter)
	taskService.AddWriters(tocWriter)
	taskService.AddWriters(incrementalWriter)
	tocWriter.SetTaskService(taskService)
	incrementalWriter.SetTaskService(taskService)

	// 初始化全局任务编排器
	// maxWorkers=4：与 LLM 网关并发能力匹配，让多篇 doc 并行生成。
	// 强模型(sonnet/gpt/DeepSeek-V4)能轻松扛 4 并发,中端国产模型(qwen-turbo/豆包/GLM-air)
	// 在 light 模式下也能扛 —— light 模式 prompt 限制了工具调用次数,实际 LLM 调用量适中。
	taskExecutor := &taskExecutorAdapter{taskService: taskService}
	orchestrator.InitGlobalOrchestrator(4, taskExecutor)
	taskService.SetOrchestrator(orchestrator.GetGlobalOrchestrator())
	defer orchestrator.ShutdownGlobalOrchestrator()

	// 初始化任务事件总线
	taskEventBus := eventbus.NewTaskEventBus()
	subscriber.NewTaskEventSubscriber(taskService).Register(taskEventBus)
	taskService.SetEventBus(taskEventBus)

	// 初始化活跃度事件总线
	activityEventBus := eventbus.NewActivityEventBus()
	subscriber.NewActivityEventSubscriber(repoRepo, cfg).Register(activityEventBus)

	// 初始化活跃度调度器
	activityScheduler := service.NewActivityScheduler(cfg, repoRepo, taskEventBus)
	activityScheduler.Start(context.Background())
	defer activityScheduler.Stop()

	// 初始化 ActivityHandler
	activityHandler := handler.NewActivityHandler(cfg)

	// 初始化 RepositoryService (依赖全局编排器，需要在 orchestrator 初始化之后)
	repoService := service.NewRepositoryService(cfg, repoRepo, taskRepo, docRepo, hintRepo, incrementalHistoryRepo)
	//注册RepoEventBus
	repoEventBus := eventbus.NewRepositoryEventBus()
	subscriber.NewRepositoryEventSubscriber(taskEventBus, taskService, repoService).Register(repoEventBus)
	incrementalWriter.SetRepositoryEventBus(repoEventBus)

	// 初始化文档事件总线
	docEventBus := eventbus.NewDocEventBus()
	subscriber.NewDocEventSubscriber(taskEventBus, syncEventRepo).Register(docEventBus)
	subscriber.NewEmbeddingEventSubscriber(ragService).Register(docEventBus)
	// 激活文档保存/更新事件投递（此前 docService 的 bus 一直是 nil，事件从未真正发出，
	// 导致"触发向量生成"形同虚设）。RAG 向量化订阅者依赖此处。
	docService.SetEventBus(docEventBus)

	// 初始化 Handler
	repoHandler := handler.NewRepositoryHandler(repoEventBus, taskEventBus, repoService, taskService, ragService)
	taskHandler := handler.NewTaskHandler(taskService)
	docHandler := handler.NewDocumentHandler(docEventBus, docService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	syncService := syncservice.New(repoRepo, taskRepo, docRepo, taskUsageRepo, syncTargetRepo, syncEventRepo)
	syncService.SetDocEventBus(docEventBus)
	syncHandler := handler.NewSyncHandler(syncService)
	userRequestHandler := handler.NewUserRequestHandler(userRequestService, taskEventBus, taskService)

	agentHandler := handler.NewAgentHandler(agentService)

	// 初始化 OpenAPIHandler（AI 友好 API 端点）
	// 提供 /.well-known/openapi.yaml 端点，供 AI 工具使用
	openAPIHandler := handler.NewOpenAPIHandler(".well-known/openapi.yaml")

	// 初始化 EnhancedModelProvider 并设置到 Manager
	manager, err := adkagents.GetOrCreateInstanceWithDocRepo(cfg, docRepo)
	if err != nil {
		log.Fatalf("Failed to get manager: %v", err)
	}
	enhancedModelProvider, err := adkagents.NewEnhancedModelProvider(cfg, apiKeyRepo, apiKeyService, taskUsageService)
	if err != nil {
		log.Fatalf("Failed to create enhanced model provider: %v", err)
	}
	manager.SetEnhancedModelProvider(enhancedModelProvider)

	// 创建 AgentFactory（必须在 Manager 设置 EnhancedModelProvider 之后）
	agentFactory, err := adkagents.NewAgentFactory(cfg)
	if err != nil {
		log.Fatalf("Failed to create agent factory: %v", err)
	}

	// 创建 ChatHandler，传入 AgentFactory、RepositoryService 和 DocumentService
	chatHandler := handler.NewChatHandler(chatService, repoService, docService, agentFactory, apiKeyRepo, ragService)
	// 启动ChatHub
	go chatHandler.GetHub().Run()

	// 创建 MCP Server，为 AI 编程工具提供文档查询接口
	mcpServer := mcp.NewMCPServer(repoService, docService)
	klog.V(6).Info("MCP Server 已初始化")

	// 创建 Streamable HTTP Server 实例
	// Streamable HTTP 是 MCP 协议的传输层实现，支持同步 HTTP 响应和流式事件
	streamableServer := server.NewStreamableHTTPServer(mcpServer.GetServer())

	// 启动时清理卡住的任务（超过 10 分钟的运行中任务）
	cleanupStuckTasks(taskService)
	taskService.StartPendingTaskScheduler(context.Background(), 10*time.Second)

	// 定期清理卡住任务（运行时也需要兜底，否则单次 LLM hang 会让任务永远卡 running）
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		// 20min 阈值：给 sonnet/glm 这类带大上下文的慢模型足够推理时间，
		// 同时配合 LLM HTTP 层的 ResponseHeaderTimeout=60s（catch 网关半挂死）+
		// cleanup 自动重试预算 3 次，构成兜底链：网关挂 -> HTTP 60s 失败 -> 任务报错 ->
		// 若任务真挂(20min 无终止) -> cleanup 强制 fail -> 自动重试 -> 最多 3 次后才永久 failed
		for range ticker.C {
			if affected, err := taskService.CleanupStuckTasks(20 * time.Minute); err != nil {
				klog.Warningf("周期清理卡住任务失败: %v", err)
			} else if affected > 0 {
				klog.V(6).Infof("周期清理：处理了 %d 个卡住任务", affected)
			}
		}
	}()

	// 设置路由
	r := router.Setup(cfg, repoHandler, taskHandler, docHandler, apiKeyHandler, syncHandler, userRequestHandler, openAPIHandler, activityHandler, agentHandler, chatHandler)

	// 添加 MCP Streamable HTTP 端点
	// Streamable HTTP 使用单个端点处理 GET（流式事件）和 POST（JSON-RPC 请求）
	r.Any("/mcp/streamable", func(c *gin.Context) {
		streamableServer.ServeHTTP(c.Writer, c.Request)
	})
	klog.V(6).Info("MCP 端点已注册: /mcp/streamable")

	//eino callbacks注册
	callbacks := adkagents.NewEinoCallbacks(true, 8)
	callbacks.AppendGlobalHandlers(callbacks.Handler())

	log.Printf("Server starting on port %s...", cfg.Server.Port)
	if err := r.Run(":" + cfg.Server.Port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// cleanupStuckTasks 清理启动前卡住的任务
func cleanupStuckTasks(taskService *service.TaskService) {
	timeout := 10 * time.Minute

	affected, err := taskService.CleanupStuckTasks(timeout)
	if err != nil {
		klog.V(6).Infof("清理卡住任务失败: %v", err)
		return
	}

	if affected > 0 {
		klog.V(6).Infof("启动时清理了 %d 个卡住的任务", affected)
	}

	queuedAffected, err := taskService.CleanupQueuedTasksOnStartup()
	if err != nil {
		klog.V(6).Infof("清理启动遗留排队任务失败: %v", err)
		return
	}

	if queuedAffected > 0 {
		klog.V(6).Infof("启动时清理了 %d 个遗留排队任务", queuedAffected)
	}
}