package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gitee.com/li-yuyanglee/leelens-backend/internal/domain"
	"gitee.com/li-yuyanglee/leelens-backend/internal/eventbus"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service"
	"k8s.io/klog/v2"
)

type RepositoryHandler struct {
	repoBus     *eventbus.RepositoryEventBus
	taskBus     *eventbus.TaskEventBus
	service     *service.RepositoryService
	taskService *service.TaskService
	ragService  *service.RAGService
}

func NewRepositoryHandler(repoBus *eventbus.RepositoryEventBus, taskBus *eventbus.TaskEventBus, service *service.RepositoryService, taskService *service.TaskService, ragService *service.RAGService) *RepositoryHandler {
	return &RepositoryHandler{
		repoBus:     repoBus,
		taskBus:     taskBus,
		service:     service,
		taskService: taskService,
		ragService:  ragService,
	}
}

// Reindex 对仓库内所有最新文档重新生成向量（RAG 回填，用于历史文档/补建索引）。
// embedding 未启用时返回 409。
func (h *RepositoryHandler) Reindex(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if h.ragService == nil || !h.ragService.Enabled() {
		c.JSON(http.StatusConflict, gin.H{"error": "RAG 未启用：请先在配置中开启 embedding.enabled 并配置 embeddings 端点"})
		return
	}
	n, err := h.ragService.ReindexRepository(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"repository_id": id, "indexed_documents": n})
}

func (h *RepositoryHandler) Create(c *gin.Context) {
	var req service.CreateRepoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	repo, err := h.service.Create(req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidRepositoryURL):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrRepositoryAlreadyExists):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	ctx := context.Background()
	h.repoBus.Publish(ctx, eventbus.RepositoryEventAdded, eventbus.RepositoryEvent{
		Type:         eventbus.RepositoryEventAdded,
		RepositoryID: repo.ID,
	})

	c.JSON(http.StatusCreated, repo)
}

func (h *RepositoryHandler) List(c *gin.Context) {
	repos, err := h.service.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, repos)
}

func (h *RepositoryHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	repo, err := h.service.Get(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
		return
	}

	c.JSON(http.StatusOK, repo)
}

func (h *RepositoryHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.service.Delete(uint(id)); err != nil {
		switch {
		case errors.Is(err, service.ErrCannotDeleteRepoInvalidStatus):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *RepositoryHandler) RunAllTasks(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.service.RunAllTasks(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "tasks started"})
}

// UpgradeToDeep 把 light 模式仓库一键升级为 deep 模式。
//
// 设计：deep 与 light 是"独立两套流程"——deep 不复用 light 跑出的 task 结构。
// 升级时：
//  1. 校验 repo.generation_mode == "light"
//  2. 取消所有 active task 的 ctx（CancelAll）
//  3. 解绑所有文档：task_id=0、is_latest=false（保留在 documents 表里供
//     "历史版本"查看，与 task 表脱钩）
//  4. 硬删除 repo 下所有 task
//  5. 聚合 repo 状态（无 task → ready，让 RunAllTasks 通过 CanExecuteTasks 检查）
//  6. 切 repository.generation_mode = "deep"
//  7. 创建一个新 TocWriter task（sort_order=10）—— deep 的 toc_editor.yaml
//     会按 deep 风格重新决定 outline 与下游 task 集合
//  8. RunAllTasks 立即把新 toc task 入队
//
// 路由: POST /api/repositories/:id/upgrade-to-deep
func (h *RepositoryHandler) UpgradeToDeep(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	repoID := uint(id)

	repo, err := h.service.Get(repoID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
		return
	}
	if repo.GenerationMode != "light" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "仓库当前不是 light 模式，无法升级",
			"current_mode": repo.GenerationMode,
		})
		return
	}

	// 1) 先把还在跑的 task 取消，避免和后续删除竞争
	canceled, _ := h.taskService.CancelAll(repoID)

	// 2) 解绑所有文档（保留数据，断开与 task 的关联）+ 删除所有 task
	if err := h.taskService.DeleteAllByRepository(repoID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":    err.Error(),
			"canceled": canceled,
		})
		return
	}

	// 3) 重新聚合 repo 状态：task 全没了，summary 全 0 → 落到 ready
	if err := h.taskService.UpdateRepositoryStatus(repoID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", repoID, err)
	}

	// 4) 切 mode
	if err := h.service.SetGenerationMode(repoID, "deep"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":    err.Error(),
			"canceled": canceled,
		})
		return
	}

	// 5) 创建一个新的 TocWriter task，让 deep 的 toc_editor.yaml 重新决定 outline
	//    后续 toc 跑完会自动按 deep outline 创建对应的 DocWrite task 集合
	tocTask, err := h.taskService.CreateTocWriteTask(c.Request.Context(), repoID, "目录分析", 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":    "创建 toc task 失败: " + err.Error(),
			"canceled": canceled,
		})
		return
	}

	// 6) 入队
	if err := h.service.RunAllTasks(repoID); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message":     "升级到 deep 已完成，但入队失败，请手动点 run-all",
			"canceled":    canceled,
			"toc_task_id": tocTask.ID,
			"enqueue_err": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":     "upgrade to deep done; toc 重新生成中，完成后会按 deep outline 创建新 task 集合",
		"canceled":    canceled,
		"toc_task_id": tocTask.ID,
	})
}

func (h *RepositoryHandler) AnalyzeDirectory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx := context.Background()

	h.taskBus.Publish(ctx, eventbus.TaskEventTocWrite, eventbus.TaskEvent{
		Type:         eventbus.TaskEventTocWrite,
		RepositoryID: uint(id),
		Title:        "目录分析",
		SortOrder:    10,
		WriterName:   domain.TocWriter,
	})

	c.JSON(http.StatusOK, gin.H{
		"message": "directory analysis started",
	})

}

func (h *RepositoryHandler) AnalyzeDatabaseModel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx := context.Background()
	h.taskBus.Publish(ctx, eventbus.TaskEventDocWrite, eventbus.TaskEvent{
		Type:         eventbus.TaskEventDocWrite,
		RepositoryID: uint(id),
		Title:        "数据库模型分析",
		SortOrder:    20,
		WriterName:   domain.DBModelWriter,
	})

	c.JSON(http.StatusOK, gin.H{
		"message": "database model analysis started",
	})
}

// AnalyzeAPI 处理API接口分析的触发请求。
func (h *RepositoryHandler) AnalyzeAPI(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx := context.Background()
	h.taskBus.Publish(ctx, eventbus.TaskEventDocWrite, eventbus.TaskEvent{
		Type:         eventbus.TaskEventDocWrite,
		RepositoryID: uint(id),
		Title:        "API接口分析",
		SortOrder:    20,
		WriterName:   domain.APIWriter,
	})

	c.JSON(http.StatusOK, gin.H{
		"message": "api analysis started",
	})
}

// IncrementalAnalysis 处理增量分析的触发请求。
func (h *RepositoryHandler) IncrementalAnalysis(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx := context.Background()
	h.taskBus.Publish(ctx, eventbus.TaskEventIncrementalWrite, eventbus.TaskEvent{
		Type:         eventbus.TaskEventIncrementalWrite,
		RepositoryID: uint(id),
		Title:        "增量分析",
		SortOrder:    20,
		WriterName:   domain.IncrementalWriter,
	})

	c.JSON(http.StatusOK, gin.H{
		"message": "incremental analysis started",
	})
}

type AnalyzeProblemRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *RepositoryHandler) SetReady(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.service.SetReady(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "仓库状态已设置为就绪"})
}

// Clone 重新下载仓库（删除本地目录并重新克隆）
// 仅在非 cloning/analyzing 状态下允许触发
func (h *RepositoryHandler) Clone(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx := context.Background()
	if err := h.service.CloneRepository(ctx, uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "clone started"})
}

func (h *RepositoryHandler) PurgeLocal(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.service.PurgeLocalDir(uint(id)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "local directory purged"})
}

// GetIncrementalHistory 获取仓库的增量同步历史记录。
func (h *RepositoryHandler) GetIncrementalHistory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	// 解析可选的 limit 参数
	limit := 50
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	history, err := h.service.GetIncrementalHistory(uint(id), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, history)
}
