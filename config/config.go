package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Data     DataConfig     `yaml:"data"`
	Agent    AgentConfig    `yaml:"agent"`
	Skill    SkillConfig    `yaml:"skill"`
	Activity ActivityConfig `yaml:"activity"`
	Chat      ChatConfig      `yaml:"chat"`
	Memory    MemoryConfig    `yaml:"memory"`
	Embedding EmbeddingConfig `yaml:"embedding"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
	Mode string `yaml:"mode"` // debug, release
	// CORSAllowOrigins 允许的跨域来源白名单。默认仅放行本地前端 dev 地址。
	// 生产部署应在 config.yaml 或 CORS_ALLOW_ORIGINS 环境变量中显式指定前端域名。
	// 注意：若设为 ["*"]（通配所有来源），将按 CORS 规范自动禁用 AllowCredentials，
	// 避免 "*" + 携带凭据" 这一典型误配。
	CORSAllowOrigins []string `yaml:"cors_allow_origins"`
	// AuthToken 共享访问令牌。非空时所有 API/MCP 请求需带 Authorization: Bearer <token>
	// （WebSocket/MCP 也接受 ?token=）。留空 = 关闭鉴权（默认，便于自托管/本地开发）。
	AuthToken string `yaml:"auth_token"`
}

type DatabaseConfig struct {
	Type string `yaml:"type"` // sqlite, mysql
	DSN  string `yaml:"dsn"`
}

type DataConfig struct {
	Dir     string `yaml:"dir"`
	RepoDir string `yaml:"repo_dir"`
}
type AgentConfig struct {
	Dir            string
	ReloadInterval time.Duration
}
type SkillConfig struct {
	Dir string
}

type ActivityConfig struct {
	Enabled         bool          `yaml:"enabled"`          // 是否启用活跃度功能
	DefaultInterval time.Duration `yaml:"default_interval"` // 默认更新间隔
	DecreaseUnit    time.Duration `yaml:"decrease_unit"`    // 每个活跃点调减的时间
	CheckInterval   time.Duration `yaml:"check_interval"`   // 定时检查间隔
	ResetHour       int           `yaml:"reset_hour"`       // 每日重置小时（0-23）
}

// ChatConfig 对话相关配置
type ChatConfig struct {
	// HistoryTokenBudget 装配历史对话进上下文时的 token 预算（粗略估算）。
	// 超出预算的更早消息会被摘要压缩。<=0 时使用默认值 6000。
	HistoryTokenBudget int `yaml:"history_token_budget"`
}

// MemoryConfig L2 跨会话记忆配置
type MemoryConfig struct {
	Enabled  bool `yaml:"enabled"`   // 是否注入本仓库其它会话的摘要作为跨会话记忆
	MaxItems int  `yaml:"max_items"` // 最多注入的历史会话摘要条数，<=0 时用默认 3
}

// EmbeddingConfig 文档向量化 / RAG 配置。默认关闭，配好 embeddings 端点后再开启。
type EmbeddingConfig struct {
	Enabled    bool   `yaml:"enabled"`      // 是否启用向量化与 RAG 检索
	Provider   string `yaml:"provider"`     // openai（OpenAI 兼容 /embeddings）| mock（占位向量，仅验证链路）
	APIKeyName string `yaml:"api_key_name"` // 使用 api_keys 中的哪条（空=最高优先级）
	Model      string `yaml:"model"`        // embedding 模型名（空时用所选 api_key 的 model）
	Dimension  int    `yaml:"dimension"`    // 向量维度
	BatchSize  int    `yaml:"batch_size"`   // 单次 embeddings 请求的文本条数上限
	TopK       int    `yaml:"top_k"`        // chat 检索注入的片段数
}

var (
	cfg  *Config
	once sync.Once
)

func GetConfig() *Config {
	once.Do(func() {
		cfg = loadConfig()
	})
	return cfg
}

func loadConfig() *Config {
	config := &Config{
		Server: ServerConfig{
			Port:             "8080",
			Mode:             "debug",
			CORSAllowOrigins: []string{"http://localhost:3001"},
		},
		Database: DatabaseConfig{
			Type: "sqlite",
			DSN:  "./data/app.db",
		},
		Data: DataConfig{
			Dir:     "./data",
			RepoDir: "./data/repos",
		},
		Agent: AgentConfig{
			Dir:            "./agents",
			ReloadInterval: 5 * time.Second,
		},
		Skill: SkillConfig{
			Dir: "./skills",
		},
		Activity: ActivityConfig{
			Enabled:         true,
			DefaultInterval: 7 * 24 * time.Hour, // 7天
			DecreaseUnit:    1 * time.Hour,      // 每个活跃点调减1小时
			CheckInterval:   1 * time.Hour,      // 每小时检查一次
			ResetHour:       0,                  // 每天凌晨0点重置
		},
		Chat: ChatConfig{
			HistoryTokenBudget: 6000,
		},
		Memory: MemoryConfig{
			Enabled:  true,
			MaxItems: 3,
		},
		Embedding: EmbeddingConfig{
			Enabled:   false,
			Provider:  "openai",
			Dimension: 1536,
			BatchSize: 16,
			TopK:      4,
		},
	}

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}

	data, err := os.ReadFile(configPath)
	if err == nil {
		yaml.Unmarshal(data, config)
	}

	// 数据库环境变量
	if dbType := os.Getenv("DB_TYPE"); dbType != "" {
		config.Database.Type = dbType
	}
	if dbDSN := os.Getenv("DB_DSN"); dbDSN != "" {
		config.Database.DSN = dbDSN
	}

	// CORS 来源环境变量（逗号分隔）
	if origins := os.Getenv("CORS_ALLOW_ORIGINS"); origins != "" {
		parts := strings.Split(origins, ",")
		cleaned := make([]string, 0, len(parts))
		for _, p := range parts {
			if v := strings.TrimSpace(p); v != "" {
				cleaned = append(cleaned, v)
			}
		}
		if len(cleaned) > 0 {
			config.Server.CORSAllowOrigins = cleaned
		}
	}

	// 鉴权令牌环境变量
	if token := os.Getenv("AUTH_TOKEN"); token != "" {
		config.Server.AuthToken = token
	}

	// 数据目录环境变量
	if dataDir := os.Getenv("DATA_DIR"); dataDir != "" {
		config.Data.Dir = dataDir
	}
	if repoDir := os.Getenv("REPO_DIR"); repoDir != "" {
		config.Data.RepoDir = repoDir
	}

	if config.Data.RepoDir == "" {
		config.Data.RepoDir = filepath.Join(config.Data.Dir, "repos")
	}

	if agentDir := os.Getenv("AGENT_DIR"); agentDir != "" {
		config.Agent.Dir = agentDir
	}
	if skillDir := os.Getenv("SKILL_DIR"); skillDir != "" {
		config.Skill.Dir = skillDir
	}

	return config
}

func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func UpdateConfig(newCfg *Config) {
	cfg = newCfg
}
