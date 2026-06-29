package repository

import (
	"context"

	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gorm.io/gorm"
)

// DocumentVectorRepository 文档向量存取（RAG）。
type DocumentVectorRepository interface {
	DeleteByDocument(ctx context.Context, documentID uint, modelName string) error
	Create(ctx context.Context, vectors []*model.DocumentVector) error
	ListByRepository(ctx context.Context, repoID uint, modelName string) ([]*model.DocumentVector, error)
	CountByRepository(ctx context.Context, repoID uint, modelName string) (int64, error)
}

type documentVectorRepository struct {
	db *gorm.DB
}

func NewDocumentVectorRepository(db *gorm.DB) DocumentVectorRepository {
	return &documentVectorRepository{db: db}
}

// DeleteByDocument 删除某文档在指定模型下的所有 chunk 向量（重建前先清旧）。
func (r *documentVectorRepository) DeleteByDocument(ctx context.Context, documentID uint, modelName string) error {
	return r.db.WithContext(ctx).
		Where("document_id = ? AND model_name = ?", documentID, modelName).
		Delete(&model.DocumentVector{}).Error
}

// Create 批量写入向量。
func (r *documentVectorRepository) Create(ctx context.Context, vectors []*model.DocumentVector) error {
	if len(vectors) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(vectors, 50).Error
}

// ListByRepository 取某仓库在指定模型下的全部 chunk 向量（在 Go 内算余弦）。
func (r *documentVectorRepository) ListByRepository(ctx context.Context, repoID uint, modelName string) ([]*model.DocumentVector, error) {
	var vectors []*model.DocumentVector
	err := r.db.WithContext(ctx).
		Where("repository_id = ? AND model_name = ?", repoID, modelName).
		Find(&vectors).Error
	return vectors, err
}

// CountByRepository 统计某仓库在指定模型下的向量条数。
func (r *documentVectorRepository) CountByRepository(ctx context.Context, repoID uint, modelName string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&model.DocumentVector{}).
		Where("repository_id = ? AND model_name = ?", repoID, modelName).
		Count(&n).Error
	return n, err
}
