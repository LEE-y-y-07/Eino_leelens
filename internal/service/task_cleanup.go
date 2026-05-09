package service

import (
	"fmt"
	"sync"
	"time"

	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service/statemachine"
	"k8s.io/klog/v2"
)

// TaskCleanupService 任务清理服务
type TaskCleanupService struct {
	taskRepo         repository.TaskRepository
	taskStateMachine *statemachine.TaskStateMachine
	lifecycle        *TaskLifecycleService

	// 自动重试配置 ——
	// 卡住任务被清理时，若仍有重试预算则重置+重新入队；耗尽后才永久标记 failed。
	// 这样避免单次 LLM hang/网关短暂错误导致整篇文档生成永久失败。
	retryFn         func(taskID uint) error // 由 TaskService 在构造完成后注入；nil 时走原 fail 流程
	maxAutoRetries  int                     // 单个任务允许的最大自动重试次数
	cleanupAttempts sync.Map                // taskID -> int 当前已自动重试次数（进程内计数）
}

// NewTaskCleanupService 创建新的任务清理服务
func NewTaskCleanupService(taskRepo repository.TaskRepository, lifecycle *TaskLifecycleService) *TaskCleanupService {
	return &TaskCleanupService{
		taskRepo:         taskRepo,
		taskStateMachine: statemachine.NewTaskStateMachine(),
		lifecycle:        lifecycle,
		maxAutoRetries:   3,
	}
}

// SetRetryFn 注入自动重试回调（一般传入 TaskService.Retry）
func (s *TaskCleanupService) SetRetryFn(fn func(taskID uint) error) {
	s.retryFn = fn
}

// CleanupStuckTasks 清理卡住的任务（运行超过指定时间的任务）
// 状态迁移: running -> failed (超时) 或 running -> pending -> queued (自动重试)
func (s *TaskCleanupService) CleanupStuckTasks(timeout time.Duration) (int64, error) {
	klog.V(6).Infof("开始清理卡住的任务: timeout=%v", timeout)

	tasks, err := s.taskRepo.GetStuckTasks(timeout)
	if err != nil {
		klog.V(6).Infof("获取卡住任务失败: error=%v", err)
		return 0, err
	}

	var affected int64
	for _, task := range tasks {
		// 1) 先把任务标记为 failed（无论后续是否重试，都需要先稳态地从 running 出来）
		oldStatus := statemachine.TaskStatus(task.Status)
		newStatus := statemachine.TaskStatusFailed

		if err := s.taskStateMachine.Transition(oldStatus, newStatus, task.ID); err != nil {
			klog.Warningf("任务状态迁移失败: taskID=%d, error=%v", task.ID, err)
			continue
		}

		// 2) 决定是否自动重试 —— 在 retry 预算内的会被重新入队；预算耗尽后才永久 failed
		attemptIfaceVal, _ := s.cleanupAttempts.LoadOrStore(task.ID, 0)
		attempts, _ := attemptIfaceVal.(int)

		shouldRetry := s.retryFn != nil && attempts < s.maxAutoRetries
		if shouldRetry {
			task.Status = string(newStatus)
			task.ErrorMsg = fmt.Sprintf("任务超时（超过 %v）— 第 %d/%d 次自动重试中",
				timeout, attempts+1, s.maxAutoRetries)
		} else {
			task.Status = string(newStatus)
			task.ErrorMsg = fmt.Sprintf("任务超时（超过 %v），已达自动重试上限 %d 次，标记为最终失败",
				timeout, s.maxAutoRetries)
			s.cleanupAttempts.Delete(task.ID)
		}
		if err := s.taskRepo.Save(&task); err != nil {
			klog.Errorf("更新任务状态失败: taskID=%d, error=%v", task.ID, err)
			continue
		}
		affected++

		// 3) 在 retry 预算内：重置任务并重新入队（Reset 会把 failed -> pending，Enqueue -> queued）
		if shouldRetry {
			s.cleanupAttempts.Store(task.ID, attempts+1)
			klog.Warningf("卡住任务自动重试: taskID=%d attempt=%d/%d", task.ID, attempts+1, s.maxAutoRetries)
			if rerr := s.retryFn(task.ID); rerr != nil {
				klog.Errorf("自动重试失败: taskID=%d, error=%v", task.ID, rerr)
			}
		} else {
			klog.Warningf("卡住任务自动重试预算耗尽，最终失败: taskID=%d", task.ID)
		}

		_ = s.lifecycle.UpdateRepositoryStatus(task.RepositoryID)
	}

	klog.V(6).Infof("清理卡住任务完成: affected=%d", affected)
	return affected, nil
}

// CleanupQueuedTasksOnStartup 清理启动时遗留的排队任务
func (s *TaskCleanupService) CleanupQueuedTasksOnStartup() (int64, error) {
	klog.V(6).Info("开始清理启动时遗留的排队任务")

	tasks, err := s.taskRepo.GetByStatus(string(statemachine.TaskStatusQueued))
	if err != nil {
		klog.V(6).Infof("获取排队任务失败: error=%v", err)
		return 0, err
	}

	var affected int64
	updatedRepoIDs := make(map[uint]struct{})
	for _, task := range tasks {
		currentStatus := statemachine.TaskStatus(task.Status)
		if err := s.taskStateMachine.Transition(currentStatus, statemachine.TaskStatusCanceled, task.ID); err != nil {
			klog.Warningf("任务状态迁移失败（%s -> canceled）: taskID=%d, error=%v", currentStatus, task.ID, err)
			continue
		}
		currentStatus = statemachine.TaskStatusCanceled
		if err := s.taskStateMachine.Transition(currentStatus, statemachine.TaskStatusPending, task.ID); err != nil {
			klog.Warningf("任务状态迁移失败（%s -> pending）: taskID=%d, error=%v", currentStatus, task.ID, err)
			continue
		}

		task.Status = string(statemachine.TaskStatusPending)
		task.ErrorMsg = ""
		task.StartedAt = nil
		task.CompletedAt = nil
		if err := s.taskRepo.Save(&task); err != nil {
			klog.Errorf("更新任务状态失败: taskID=%d, error=%v", task.ID, err)
			continue
		}

		affected++
		updatedRepoIDs[task.RepositoryID] = struct{}{}
		klog.V(6).Infof("启动时清理排队任务完成: taskID=%d", task.ID)
	}

	for repoID := range updatedRepoIDs {
		_ = s.lifecycle.UpdateRepositoryStatus(repoID)
	}

	klog.V(6).Infof("启动时清理排队任务完成: affected=%d", affected)
	return affected, nil
}

// GetStuckTasks 获取卡住的任务列表
func (s *TaskCleanupService) GetStuckTasks(timeout time.Duration) ([]model.Task, error) {
	return s.taskRepo.GetStuckTasks(timeout)
}
