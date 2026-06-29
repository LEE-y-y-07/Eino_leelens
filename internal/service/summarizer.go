package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"gitee.com/li-yuyanglee/leelens-backend/internal/pkg/adkagents"
)

// Summarizer 通过无工具的 summarizer agent 调用一次 LLM 做文本/对话摘要。
// L1 历史压缩、L2 仓库级跨会话记忆、会话标题生成三处共用同一入口。
type Summarizer struct {
	factory *adkagents.AgentFactory
}

func NewSummarizer(factory *adkagents.AgentFactory) *Summarizer {
	return &Summarizer{factory: factory}
}

// Summarize 让 summarizer agent 按 instruction 对 text 产出摘要/标题。
// 出错或为空时返回错误，调用方应自行回退（如截断）。
func (s *Summarizer) Summarize(ctx context.Context, instruction, text string) (string, error) {
	if s == nil || s.factory == nil || s.factory.Manager == nil {
		return "", fmt.Errorf("summarizer 未初始化")
	}
	// 每次创建全新 agent 实例：ADK agent 运行后会被冻结，不可复用。
	agent, err := s.factory.Manager.CreateAgent("summarizer")
	if err != nil {
		return "", fmt.Errorf("创建 summarizer agent 失败: %w", err)
	}
	prompt := instruction
	if strings.TrimSpace(text) != "" {
		prompt = instruction + "\n\n---\n\n" + text
	}
	out, err := adkagents.RunAgentToLastContent(ctx, agent, []adk.Message{
		{Role: schema.User, Content: prompt},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
