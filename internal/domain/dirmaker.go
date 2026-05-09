package domain

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// generationResult 表示 Agent 输出的任务生成结果（仅包内使用）。
type DirMakerGenerationResult struct {
	Dirs            []*DirMakerDirSpec `json:"dirs" yaml:"dirs"`
	AnalysisSummary FlexibleString     `json:"analysis_summary" yaml:"analysis_summary"`
}

// FlexibleString 兼容 LLM 输出 analysis_summary 时既可能写成
//   - 标量字符串（用 `|` 字面块）
//   - YAML 映射（`key: value` 嵌套结构）
//
// 两种情况都按字符串使用 —— 映射会被重新序列化成多行 YAML。
// 这避免了"line N: cannot unmarshal !!map into string"在 deepseek/qwen 等模型上的反复失败。
type FlexibleString string

// UnmarshalYAML 自定义解码：标量直接拿值，映射则 marshal 回字符串
func (f *FlexibleString) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*f = ""
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		*f = FlexibleString(node.Value)
		return nil
	default:
		// 映射 / 序列 —— 回序列化成多行 YAML 字符串
		out, err := yaml.Marshal(node)
		if err != nil {
			return err
		}
		*f = FlexibleString(strings.TrimRight(string(out), "\n"))
		return nil
	}
}

// String 兼容现有调用点（fmt 打印、传给业务等）
func (f FlexibleString) String() string { return string(f) }

// taskSpec 表示 Agent 生成的单个任务定义（仅包内使用）。
// Type 字段不局限于预定义值，Agent 可根据项目特征自由定义。
type DirMakerDirSpec struct {
	Title     string             `json:"title" yaml:"title"`           // 目录标题，如 "安全分析"
	SortOrder int                `json:"sort_order" yaml:"sort_order"` // 排序顺序
	Hint      []DirMakerHintSpec `json:"hint" yaml:"hint"`
	Outline   string             `json:"outline" yaml:"outline"`
	DocID     uint               `json:"doc_id" yaml:"doc_id"` // 关联的文档ID 保存到数据库后才有
}

type DirMakerHintSpec struct {
	Aspect string `json:"aspect" yaml:"aspect"`
	Source string `json:"source" yaml:"source"`
	Detail string `json:"detail" yaml:"detail"`
}

// incrementalGenerationResult 表示 Agent 输出的任务生成结果（仅包内使用）。
type IncrementalGenerationResult struct {
	UpdateDirs []*IncrementalDirSpec `json:"update_dirs" yaml:"update_dirs"`
	AddDirs    []*DirMakerDirSpec    `json:"add_dirs" yaml:"add_dirs"`
}

// taskSpec 表示 Agent 生成的单个任务定义（仅包内使用）。
// Type 字段不局限于预定义值，Agent 可根据项目特征自由定义。
type IncrementalDirSpec struct {
	Title   string `json:"title" yaml:"title"` // 目录标题，如 "安全分析"
	Content string `json:"content" yaml:"content"`
	Replace string `json:"replace" yaml:"replace"`
	DocID   uint   `json:"doc_id" yaml:"doc_id"` // 关联的文档ID 保存到数据库后才有
}
