package adkagents

import (
	"context"
	"time"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ExitConfig 退出条件配置
type ExitConfig struct {
	Type string `yaml:"type" json:"type"` // 退出类型，如 "tool_call"
}

// LoadResult 加载结果
type LoadResult struct {
	Agent  *AgentDefinition
	Error  error
	Action string // "created", "updated", "deleted", "failed"
}

// FileEvent 文件事件
type FileEvent struct {
	Type string // "create", "modify", "delete"
	Path string
}

// APIKeyService API Key 服务接口（避免循环导入）
type APIKeyService interface {
	MarkUnavailable(ctx context.Context, apiKeyID uint, resetTime time.Time) error
	RecordRequest(ctx context.Context, apiKeyID uint, success bool) error
}

// TaskUsageService 任务用量记录接口（避免循环导入）
type TaskUsageService interface {
	RecordUsage(ctx context.Context, taskID uint, apiKeyName string, usage *schema.TokenUsage) error
}

// ModelWithMetadata 带有元数据的模型包装器
//
// ChatModel 字段类型为 einoModel.BaseChatModel（仅要求 Generate/Stream），
// 这样既能承载旧接口 einoModel.ChatModel（含 BindTools），也能承载
// einoModel.ToolCallingChatModel（含 WithTools）—— 后者由 ProxyChatModel
// 在每次调用前通过 WithTools 绑定工具后使用。
type ModelWithMetadata struct {
	ChatModel  einoModel.BaseChatModel
	APIKeyName string
	APIKeyID   uint
	LLMModel   string
}

// Name 返回模型名称
func (m *ModelWithMetadata) Name() string {
	return m.APIKeyName
}

// ModelProvider 模型提供者接口
type ModelProvider interface {
	// GetModel 获取指定名称的模型，name 为空时返回默认模型
	GetModel(name string) (einoModel.BaseChatModel, error)
	// DefaultModel 获取默认模型
	DefaultModel() einoModel.BaseChatModel
	// GetModelPool 获取模型池
	GetModelPool(ctx context.Context, names []string) ([]*ModelWithMetadata, error)
	// MarkModelUnavailable 标记模型为不可用
	MarkModelUnavailable(ctx context.Context, modelName string, resetTime time.Time) error
	// GetNextModel 获取下一个可用模型
	GetNextModel(ctx context.Context, currentModelName string, poolNames []string) (*ModelWithMetadata, error)
}

// Now 返回当前时间（用于测试）
var Now = func() time.Time {
	return time.Now()
}
