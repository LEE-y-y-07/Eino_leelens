package writers

import (
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"k8s.io/klog/v2"
)

func safe(s string) string {
	if s == "" {
		return "(无)"
	}
	return s
}

// pickAgent 根据生成模式选择 agent 名称 ——
// mode=="light" 时返回 base+"_light" 版本（轻度思考，更少 LLM 调用，适配弱/免费模型），
// 其他情况返回 base 原始名称（深度思考，全功能 agent，质量优先）。
// 调用方需保证存在对应的 _light 版本 yaml 文件，否则 agent 加载会失败。
func pickAgent(base, mode string) string {
	if mode == "light" {
		return base + "_light"
	}
	return base
}

// lookupRepoModeByTaskID 通过 taskID 反查仓库的 generation_mode 字段
// 返回值: "deep" | "light"，找不到时返回 "deep"
func lookupRepoModeByTaskID(taskRepo repository.TaskRepository, repoRepo repository.RepoRepository, taskID uint) string {
	if taskRepo == nil || repoRepo == nil || taskID == 0 {
		return "deep"
	}
	task, err := taskRepo.Get(taskID)
	if err != nil || task == nil {
		klog.V(6).Infof("[lookupRepoMode] taskRepo.Get(%d) failed: %v", taskID, err)
		return "deep"
	}
	repo, err := repoRepo.GetBasic(task.RepositoryID)
	if err != nil || repo == nil {
		klog.V(6).Infof("[lookupRepoMode] repoRepo.GetBasic(%d) failed: %v", task.RepositoryID, err)
		return "deep"
	}
	if repo.GenerationMode == "light" {
		return "light"
	}
	return "deep"
}
