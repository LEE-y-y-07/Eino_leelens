package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"gitee.com/li-yuyanglee/leelens-backend/config"
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/pkg/embedding"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"k8s.io/klog/v2"
)

// RetrievedChunk 一次检索命中的文档片段。
type RetrievedChunk struct {
	DocID   uint
	Content string
	Score   float32
}

// RAGService 负责文档向量化与语义检索（L3 语义记忆）。
// embedding.enabled 关闭或 embedder 初始化失败时，所有方法均为安全空操作。
type RAGService struct {
	cfg      config.EmbeddingConfig
	docRepo  repository.DocumentRepository
	vecRepo  repository.DocumentVectorRepository
	embedder embedding.Embedder
}

func NewRAGService(cfg *config.Config, docRepo repository.DocumentRepository, vecRepo repository.DocumentVectorRepository, apiKeyRepo repository.APIKeyRepository) *RAGService {
	s := &RAGService{cfg: cfg.Embedding, docRepo: docRepo, vecRepo: vecRepo}
	if cfg.Embedding.Enabled {
		emb, err := embedding.NewEmbedder(cfg.Embedding, apiKeyRepo)
		if err != nil {
			klog.Warningf("[RAG] 初始化 embedder 失败，RAG 将不可用: %v", err)
		} else {
			s.embedder = emb
			klog.V(6).Infof("[RAG] 已启用，provider=%s model=%s dim=%d", cfg.Embedding.Provider, emb.ModelName(), emb.Dimension())
		}
	}
	return s
}

// Enabled 仅当配置开启且 embedder 就绪时返回 true。
func (s *RAGService) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.embedder != nil
}

func (s *RAGService) batchSize() int {
	if s.cfg.BatchSize > 0 {
		return s.cfg.BatchSize
	}
	return 16
}

// IndexDocument 对单篇文档分块、embed 并落库（先删旧 chunk 再写新的）。
func (s *RAGService) IndexDocument(ctx context.Context, docID, repoID uint, content string) error {
	if !s.Enabled() {
		return nil
	}
	modelName := s.embedder.ModelName()
	if err := s.vecRepo.DeleteByDocument(ctx, docID, modelName); err != nil {
		return err
	}
	chunks := embedding.Chunk(content, 1200)
	if len(chunks) == 0 {
		return nil
	}
	var rows []*model.DocumentVector
	bs := s.batchSize()
	for start := 0; start < len(chunks); start += bs {
		end := start + bs
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]
		vecs, err := s.embedder.Embed(ctx, batch)
		if err != nil {
			return err
		}
		for i, v := range vecs {
			if len(v) == 0 {
				continue
			}
			sum := sha256.Sum256([]byte(batch[i]))
			rows = append(rows, &model.DocumentVector{
				DocumentID:   docID,
				RepositoryID: repoID,
				ChunkIndex:   start + i,
				ModelName:    modelName,
				Dimension:    len(v),
				Embedding:    embedding.Float32ToBytes(v),
				Content:      batch[i],
				ContentHash:  hex.EncodeToString(sum[:]),
			})
		}
	}
	return s.vecRepo.Create(ctx, rows)
}

// ReindexRepository 对仓库内所有最新文档重新向量化，返回处理的文档数。
func (s *RAGService) ReindexRepository(ctx context.Context, repoID uint) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	docs, err := s.docRepo.GetByRepository(repoID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range docs {
		d := docs[i]
		if err := s.IndexDocument(ctx, d.ID, d.RepositoryID, d.Content); err != nil {
			klog.Warningf("[RAG] 文档向量化失败 docID=%d: %v", d.ID, err)
			continue
		}
		n++
	}
	return n, nil
}

// Retrieve 对 query 做语义检索，返回 top-k 片段（去重到每文档最高分）。
func (s *RAGService) Retrieve(ctx context.Context, repoID uint, query string, k int) ([]RetrievedChunk, error) {
	if !s.Enabled() || query == "" {
		return nil, nil
	}
	if k <= 0 {
		k = 4
	}
	qv, err := s.embedder.Embed(ctx, []string{query})
	if err != nil || len(qv) == 0 || len(qv[0]) == 0 {
		return nil, err
	}
	rows, err := s.vecRepo.ListByRepository(ctx, repoID, s.embedder.ModelName())
	if err != nil {
		return nil, err
	}
	all := make([]RetrievedChunk, 0, len(rows))
	for _, r := range rows {
		vec := embedding.BytesToFloat32(r.Embedding)
		all = append(all, RetrievedChunk{
			DocID:   r.DocumentID,
			Content: r.Content,
			Score:   embedding.Cosine(qv[0], vec),
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })

	// 去重到每文档最高分
	seen := make(map[uint]bool)
	var out []RetrievedChunk
	for _, c := range all {
		if seen[c.DocID] {
			continue
		}
		seen[c.DocID] = true
		out = append(out, c)
		if len(out) >= k {
			break
		}
	}
	return out, nil
}
