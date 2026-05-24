package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"gorm.io/gorm"
)

// TestReadDocToolInvokableRun 验证 read_doc 工具按ID读取文档内容
func TestReadDocToolInvokableRun(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db error: %v", err)
	}
	if err := db.AutoMigrate(&model.Document{}); err != nil {
		t.Fatalf("migrate error: %v", err)
	}

	docRepo := repository.NewDocumentRepository(db)
	doc := &model.Document{
		RepositoryID: 1,
		TaskID:       1,
		Title:        "示例文档",
		Filename:     "demo.md",
		Content:      "这里是全文内容",
	}
	if err := docRepo.Create(doc); err != nil {
		t.Fatalf("create doc error: %v", err)
	}

	readTool := NewReadDocTool(docRepo)

	// 用例 1：数字形式（标准）
	argsJSON, _ := json.Marshal(struct {
		DocID uint `json:"doc_id"`
	}{DocID: doc.ID})
	result, err := readTool.InvokableRun(context.Background(), string(argsJSON))
	if err != nil {
		t.Fatalf("InvokableRun(number) error: %v", err)
	}
	if result != doc.Content {
		t.Fatalf("unexpected content (number): %s", result)
	}

	// 用例 2：字符串形式（DeepSeek 等模型实际行为）
	strArgs := fmt.Sprintf(`{"doc_id":"%d"}`, doc.ID)
	result, err = readTool.InvokableRun(context.Background(), strArgs)
	if err != nil {
		t.Fatalf("InvokableRun(string) error: %v", err)
	}
	if result != doc.Content {
		t.Fatalf("unexpected content (string): %s", result)
	}
}
