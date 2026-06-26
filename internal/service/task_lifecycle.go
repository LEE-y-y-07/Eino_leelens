package service

import (
	"context"
	"fmt"
	"time"

	"gitee.com/li-yuyanglee/leelens-backend/internal/eventbus"
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service/orchestrator"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service/statemachine"
	"k8s.io/klog/v2"
)

// TaskLifecycleService 任务生命周期服务
type TaskLifecycleService struct {
	taskRepo         repository.TaskRepository
	repoRepo         repository.RepoRepository
	taskStateMachine *statemachine.TaskStateMachine
	repoAggregator   *statemachine.RepositoryStatusAggregator
	orchestrator     *orchestrator.Orchestrator
	bus              *eventbus.TaskEventBus
}

// NewTaskLifecycleService 创建新的任务生命周期服务
func NewTaskLifecycleService(taskRepo repository.TaskRepository, repoRepo repository.RepoRepository) *TaskLifecycleService {
	return &TaskLifecycleService{
		taskRepo:         taskRepo,
		repoRepo:         repoRepo,
		taskStateMachine: statemachine.NewTaskStateMachine(),
		repoAggregator:   statemachine.NewRepositoryStatusAggregator(),
	}
}

// SetOrchestrator 设置编排器
func (s *TaskLifecycleService) SetOrchestrator(o *orchestrator.Orchestrator) {
	s.orchestrator = o
}

// SetEventBus 设置事件总线
func (s *TaskLifecycleService) SetEventBus(bus *eventbus.TaskEventBus) {
	s.bus = bus
}

// SucceedTask 任务成功完成处理
// 状态迁移: running -> succeeded
func (s *TaskLifecycleService) SucceedTask(task *model.Task) error {
	klog.V(6).Infof("任务成功: taskID=%d", task.ID)

	oldStatus := statemachine.TaskStatus(task.Status)
	newStatus := statemachine.TaskStatusSucceeded

	if err := s.taskStateMachine.Transition(oldStatus, newStatus, task.ID); err != nil {
		klog.Errorf("任务状态迁移失败: taskID=%d, error=%v", task.ID, err)
		return err
	}

	completedAt := time.Now()
	task.Status = string(newStatus)
	task.CompletedAt = &completedAt
	if err := s.taskRepo.Save(task); err != nil {
		klog.Errorf("更新任务状态失败: taskID=%d, error=%v", task.ID, err)
		return err
	}

	duration := completedAt.Sub(*task.StartedAt)
	klog.V(6).Infof("任务执行完成: taskID=%d, duration=%v", task.ID, duration)

	if err := s.UpdateRepositoryStatus(task.RepositoryID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", task.RepositoryID, err)
	}

	if s.bus != nil {
		s.bus.Publish(context.Background(), eventbus.TaskEventWriteComplete, eventbus.TaskEvent{
			Type:         eventbus.TaskEventWriteComplete,
			RepositoryID: task.RepositoryID,
			Title:        task.Title,
			SortOrder:    task.SortOrder,
			RunAfter:     task.RunAfter,
			DocID:        task.DocID,
			WriterName:   task.WriterName,
			TaskID:       task.ID,
			TaskType:     task.TaskType,
		})
	}

	return nil
}

// FailTask 任务失败处理
// 状态迁移: running -> failed
func (s *TaskLifecycleService) FailTask(task *model.Task, errMsg string) error {
	klog.V(6).Infof("任务失败: taskID=%d, error=%s", task.ID, errMsg)

	oldStatus := statemachine.TaskStatus(task.Status)
	newStatus := statemachine.TaskStatusFailed

	if err := s.taskStateMachine.Transition(oldStatus, newStatus, task.ID); err != nil {
		klog.Errorf("任务状态迁移失败: taskID=%d, error=%v", task.ID, err)
		return err
	}

	task.Status = string(newStatus)
	task.ErrorMsg = errMsg
	if err := s.taskRepo.Save(task); err != nil {
		klog.Errorf("更新任务状态失败: taskID=%d, error=%v", task.ID, err)
		return err
	}

	if err := s.UpdateRepositoryStatus(task.RepositoryID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", task.RepositoryID, err)
	}

	if s.bus != nil {
		s.bus.Publish(context.Background(), eventbus.TaskEventWriteFailed, eventbus.TaskEvent{
			Type:         eventbus.TaskEventWriteFailed,
			RepositoryID: task.RepositoryID,
			Title:        task.Title,
			SortOrder:    task.SortOrder,
			RunAfter:     task.RunAfter,
			DocID:        task.DocID,
			WriterName:   task.WriterName,
			TaskID:       task.ID,
			TaskType:     task.TaskType,
		})
	}

	return nil
}

// Reset 重置任务
// 状态迁移: failed/succeeded/canceled -> pending
func (s *TaskLifecycleService) Reset(taskID uint) error {
	klog.V(6).Infof("重置任务: taskID=%d", taskID)

	task, err := s.taskRepo.Get(taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %w", err)
	}

	oldStatus := statemachine.TaskStatus(task.Status)
	newStatus := statemachine.TaskStatusPending

	if err := s.taskStateMachine.Transition(oldStatus, newStatus, taskID); err != nil {
		return fmt.Errorf("任务状态迁移失败: %w", err)
	}

	task.Status = string(newStatus)
	task.ErrorMsg = ""
	task.StartedAt = nil
	task.CompletedAt = nil
	if err := s.taskRepo.Save(task); err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}

	klog.V(6).Infof("任务已重置: taskID=%d", taskID)
	if err := s.UpdateRepositoryStatus(task.RepositoryID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", task.RepositoryID, err)
	}

	return nil
}

// ForceReset 强制重置任务，无论当前状态
// 状态迁移: 任意状态 -> pending（除了running）
func (s *TaskLifecycleService) ForceReset(taskID uint) error {
	klog.V(6).Infof("强制重置任务: taskID=%d", taskID)

	task, err := s.taskRepo.Get(taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %w", err)
	}

	klog.V(6).Infof("任务当前状态: taskID=%d, status=%s, startedAt=%v",
		taskID, task.Status, task.StartedAt)

	oldStatus := statemachine.TaskStatus(task.Status)
	newStatus := statemachine.TaskStatusPending

	currentStatus := oldStatus
	if currentStatus == statemachine.TaskStatusRunning || currentStatus == statemachine.TaskStatusQueued {
		if err := s.taskStateMachine.Transition(currentStatus, statemachine.TaskStatusCanceled, taskID); err != nil {
			klog.Warningf("任务状态迁移失败（%s -> canceled）: taskID=%d, error=%v，继续强制重置", currentStatus, taskID, err)
		} else {
			currentStatus = statemachine.TaskStatusCanceled
		}
	}

	if currentStatus != newStatus {
		if err := s.taskStateMachine.Transition(currentStatus, newStatus, taskID); err != nil {
			klog.Warningf("任务状态迁移失败（%s -> %s）: taskID=%d, error=%v，继续强制重置", currentStatus, newStatus, taskID, err)
		}
	}

	task.Status = string(newStatus)
	task.ErrorMsg = ""
	task.StartedAt = nil
	task.CompletedAt = nil

	klog.V(6).Infof("任务已强制重置: taskID=%d", taskID)
	if err := s.taskRepo.Save(task); err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}

	if err := s.UpdateRepositoryStatus(task.RepositoryID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", task.RepositoryID, err)
	}

	return nil
}

// Cancel 取消任务
// 支持取消 Running 和 Queued 状态的任务
func (s *TaskLifecycleService) Cancel(taskID uint) error {
	klog.V(6).Infof("取消任务: taskID=%d", taskID)

	task, err := s.taskRepo.Get(taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %w", err)
	}

	oldStatus := statemachine.TaskStatus(task.Status)
	newStatus := statemachine.TaskStatusCanceled

	if oldStatus == newStatus {
		return nil
	}

	if oldStatus == statemachine.TaskStatusRunning {
		if s.orchestrator != nil && s.orchestrator.CancelTask(taskID) {
			klog.V(6).Infof("已触发运行中任务的取消: taskID=%d", taskID)
		} else {
			klog.Warningf("尝试取消运行中任务，但编排器中未找到: taskID=%d", taskID)
		}
	}

	if err := s.taskStateMachine.Transition(oldStatus, newStatus, taskID); err != nil {
		return fmt.Errorf("任务状态迁移失败: %w", err)
	}

	now := time.Now()
	task.Status = string(newStatus)
	task.CompletedAt = &now
	task.ErrorMsg = "用户手动取消"

	if err := s.taskRepo.Save(task); err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}

	klog.V(6).Infof("任务已取消: taskID=%d", taskID)
	if err := s.UpdateRepositoryStatus(task.RepositoryID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", task.RepositoryID, err)
	}

	return nil
}

// CancelAll 批量取消仓库下所有 queued + running 状态的任务
// 返回实际取消的任务数量。pending 任务因状态机限制不会被取消，
// 但 scheduler 每 10s 会把它们推进到 queued，之后用户再点一次即可逮到。
func (s *TaskLifecycleService) CancelAll(repoID uint) (int, error) {
	tasks, err := s.taskRepo.GetByRepository(repoID)
	if err != nil {
		return 0, fmt.Errorf("获取任务列表失败: %w", err)
	}

	var canceled int
	var lastErr error
	for _, t := range tasks {
		status := statemachine.TaskStatus(t.Status)
		if status != statemachine.TaskStatusQueued && status != statemachine.TaskStatusRunning {
			continue
		}
		if err := s.Cancel(t.ID); err != nil {
			klog.Warningf("批量取消-单个任务失败: repoID=%d taskID=%d err=%v", repoID, t.ID, err)
			lastErr = err
			continue
		}
		canceled++
	}
	klog.V(6).Infof("批量取消完成: repoID=%d canceled=%d lastErr=%v", repoID, canceled, lastErr)
	return canceled, lastErr
}

// PauseAll 批量暂停仓库下所有 pending/queued/running 任务。
// 流程：
//  1. 在 orchestrator 内 in-memory 标记 repo 为 paused（scheduler 后续不再推 pending）
//  2. 对每个 active task：先把 DB status 写成 paused（状态机迁移）
//  3. 对其中 running 的，再调用 orchestrator.CancelTask 取消其 ctx
//     —— Run() 内已加 ctx.Err()!=nil 时跳过 FailTask 的保护，
//     不会把 paused 覆盖回 failed
//
// paused 不是终止态：后续可通过 ResumeAll 回到 queued 重新入队。
func (s *TaskLifecycleService) PauseAll(repoID uint) (int, error) {
	if s.orchestrator != nil {
		s.orchestrator.PauseRepo(repoID)
	}

	tasks, err := s.taskRepo.GetByRepository(repoID)
	if err != nil {
		return 0, fmt.Errorf("获取任务列表失败: %w", err)
	}

	var paused int
	var lastErr error
	for i := range tasks {
		t := &tasks[i]
		oldStatus := statemachine.TaskStatus(t.Status)
		if oldStatus != statemachine.TaskStatusPending &&
			oldStatus != statemachine.TaskStatusQueued &&
			oldStatus != statemachine.TaskStatusRunning {
			continue
		}

		newStatus := statemachine.TaskStatusPaused
		if err := s.taskStateMachine.Transition(oldStatus, newStatus, t.ID); err != nil {
			klog.Warningf("批量暂停-状态迁移失败: repoID=%d taskID=%d %s->%s err=%v",
				repoID, t.ID, oldStatus, newStatus, err)
			lastErr = err
			continue
		}

		t.Status = string(newStatus)
		if err := s.taskRepo.Save(t); err != nil {
			klog.Warningf("批量暂停-保存失败: repoID=%d taskID=%d err=%v", repoID, t.ID, err)
			lastErr = err
			continue
		}

		// running 任务的 ctx 取消放在 DB 写完之后，确保 Run() 看到 ctx.Err() 时
		// 即使触发了 FailTask 检查也已经命中"跳过"分支
		if oldStatus == statemachine.TaskStatusRunning && s.orchestrator != nil {
			if !s.orchestrator.CancelTask(t.ID) {
				klog.Warningf("批量暂停-未在编排器中找到 running 任务: taskID=%d", t.ID)
			}
		}
		paused++
	}

	if err := s.UpdateRepositoryStatus(repoID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", repoID, err)
	}
	klog.V(6).Infof("批量暂停完成: repoID=%d paused=%d lastErr=%v", repoID, paused, lastErr)
	return paused, lastErr
}

// ResumeAll 恢复仓库下所有 paused 任务：清除 orchestrator 暂停标记 +
// 把 DB status=paused 的任务批量改回 queued 并重新入队。
// 实际入队由 caller 通过 TaskService.Enqueue 完成（状态机里 paused -> queued 已合法）。
func (s *TaskLifecycleService) ResumeAll(repoID uint, enqueueFn func(taskID uint) error) (int, error) {
	if s.orchestrator != nil {
		s.orchestrator.ResumeRepo(repoID)
	}

	tasks, err := s.taskRepo.GetByRepository(repoID)
	if err != nil {
		return 0, fmt.Errorf("获取任务列表失败: %w", err)
	}

	var resumed int
	var lastErr error
	for _, t := range tasks {
		if statemachine.TaskStatus(t.Status) != statemachine.TaskStatusPaused {
			continue
		}
		if err := enqueueFn(t.ID); err != nil {
			klog.Warningf("批量恢复-单个任务入队失败: repoID=%d taskID=%d err=%v", repoID, t.ID, err)
			lastErr = err
			continue
		}
		resumed++
	}

	if err := s.UpdateRepositoryStatus(repoID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", repoID, err)
	}
	klog.V(6).Infof("批量恢复完成: repoID=%d resumed=%d lastErr=%v", repoID, resumed, lastErr)
	return resumed, lastErr
}

// Delete 删除任务（删除单个任务）
// 注意：删除任务也会删除关联的文档
func (s *TaskLifecycleService) Delete(taskID uint, docService *DocumentService) error {
	klog.V(6).Infof("删除任务: taskID=%d", taskID)

	task, err := s.taskRepo.Get(taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %w", err)
	}

	repoID := task.RepositoryID

	currentStatus := statemachine.TaskStatus(task.Status)
	if currentStatus == statemachine.TaskStatusRunning || currentStatus == statemachine.TaskStatusQueued {
		return fmt.Errorf("运行中或已入队的任务不允许删除: current=%s", currentStatus)
	}

	if err := docService.DeleteByTaskID(taskID); err != nil {
		return fmt.Errorf("删除关联文档失败: %w", err)
	}

	if err := s.taskRepo.Delete(taskID); err != nil {
		return fmt.Errorf("删除任务失败: %w", err)
	}

	klog.V(6).Infof("任务已删除: taskID=%d", taskID)
	if err := s.UpdateRepositoryStatus(repoID); err != nil {
		klog.Warningf("更新仓库状态失败: repoID=%d, error=%v", repoID, err)
	}

	return nil
}

// UpdateRepositoryStatus 更新仓库状态（使用状态机聚合器）
func (s *TaskLifecycleService) UpdateRepositoryStatus(repoID uint) error {
	repo, err := s.repoRepo.GetBasic(repoID)
	if err != nil {
		return fmt.Errorf("获取仓库失败: %w", err)
	}

	tasks, err := s.taskRepo.GetByRepository(repoID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %w", err)
	}

	summary := s.buildTaskSummary(tasks)

	currentStatus := statemachine.RepositoryStatus(repo.Status)
	newStatus, err := s.repoAggregator.AggregateStatus(currentStatus, summary, repoID)
	if err != nil {
		klog.Warningf("仓库状态聚合失败: repoID=%d, error=%v", repoID, err)
		return err
	}

	if newStatus == currentStatus {
		return nil
	}

	if err := s.repoAggregator.StateMachine.ValidateTransition(currentStatus, newStatus); err != nil {
		klog.Errorf("仓库状态迁移失败: repoID=%d, error=%v", repoID, err)
		return err
	}

	repo.Status = string(newStatus)
	if err := s.repoRepo.Save(repo); err != nil {
		return fmt.Errorf("更新仓库状态失败: %w", err)
	}

	klog.V(6).Infof("仓库状态已更新: repoID=%d, %s -> %s", repoID, currentStatus, newStatus)

	return nil
}

// buildTaskSummary 构建任务状态汇总
func (s *TaskLifecycleService) buildTaskSummary(tasks []model.Task) *statemachine.TaskStatusSummary {
	summary := &statemachine.TaskStatusSummary{
		Total: len(tasks),
	}

	for _, t := range tasks {
		status := statemachine.TaskStatus(t.Status)
		switch status {
		case statemachine.TaskStatusPending:
			summary.Pending++
		case statemachine.TaskStatusQueued:
			summary.Queued++
		case statemachine.TaskStatusRunning:
			summary.Running++
		case statemachine.TaskStatusSucceeded:
			summary.Succeeded++
		case statemachine.TaskStatusFailed:
			summary.Failed++
		case statemachine.TaskStatusCanceled:
			summary.Canceled++
		case statemachine.TaskStatusPaused:
			summary.Paused++
		}
	}

	return summary
}
