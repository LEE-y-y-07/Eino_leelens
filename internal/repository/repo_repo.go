package repository

import (
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gorm.io/gorm"
)

type repoRepository struct {
	db *gorm.DB
}

func NewRepoRepository(db *gorm.DB) RepoRepository {
	return &repoRepository{db: db}
}

func (r *repoRepository) Create(repo *model.Repository) error {
	return r.db.Create(repo).Error
}

func (r *repoRepository) List() ([]model.Repository, error) {
	var repos []model.Repository
	err := r.db.Order("created_at desc").Find(&repos).Error
	return repos, err
}

func (r *repoRepository) Get(id uint) (*model.Repository, error) {
	var repo model.Repository
	err := r.db.Preload("Tasks").Preload("Documents", "is_latest = ?", true).First(&repo, id).Error
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (r *repoRepository) GetBasic(id uint) (*model.Repository, error) {
	var repo model.Repository
	err := r.db.First(&repo, id).Error
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (r *repoRepository) Save(repo *model.Repository) error {
	return r.db.Save(repo).Error
}

func (r *repoRepository) Delete(id uint) error {
	return r.db.Delete(&model.Repository{}, id).Error
}

// DeleteCascade 在单个事务里删除仓库及其关联的文档、任务、文档向量，
// 避免中途某步失败留下孤儿数据（替代原先三次独立无事务删除）。
func (r *repoRepository) DeleteCascade(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ?", id).Delete(&model.DocumentVector{}).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ?", id).Delete(&model.Document{}).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ?", id).Delete(&model.Task{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Repository{}, id).Error
	})
}
