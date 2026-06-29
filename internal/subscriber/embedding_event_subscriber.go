package subscriber

import (
	"context"

	"gitee.com/li-yuyanglee/leelens-backend/internal/eventbus"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service"
	"k8s.io/klog/v2"
)

// EmbeddingEventSubscriber 订阅文档保存/更新事件，异步生成向量（L3 RAG）。
type EmbeddingEventSubscriber struct {
	rag *service.RAGService
}

func NewEmbeddingEventSubscriber(rag *service.RAGService) *EmbeddingEventSubscriber {
	return &EmbeddingEventSubscriber{rag: rag}
}

func (s *EmbeddingEventSubscriber) Register(bus *eventbus.DocEventBus) {
	if bus == nil || s.rag == nil {
		return
	}
	bus.Subscribe(eventbus.DocEventSaved, s.handle)
	bus.Subscribe(eventbus.DocEventUpdated, s.handle)
}

// handle 异步向量化，绝不阻塞文档保存主流程；失败仅告警。
func (s *EmbeddingEventSubscriber) handle(_ context.Context, event eventbus.DocEvent) error {
	if !s.rag.Enabled() {
		return nil
	}
	docID, repoID, content := event.DocID, event.RepositoryID, event.Content
	go func() {
		if err := s.rag.IndexDocument(context.Background(), docID, repoID, content); err != nil {
			klog.Warningf("[Embedding] 文档向量化失败 docID=%d: %v", docID, err)
		}
	}()
	return nil
}
