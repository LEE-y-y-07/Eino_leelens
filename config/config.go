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
}

type ServerConfig struct {
	Port string `yaml:"port"`
	Mode string `yaml:"mode"` // debug, release
	// CORSAllowOrigins 允许的跨域来源白名单。默认仅放行本地前端 dev 地址。
	// 生产部署应在 config.yaml 或 CORS_ALLOW_ORIGINS 环境变量中显式指定前端域名。
	// 注意：若设为 ["*"]（通配所有来源），将按 CORS 规范自动禁用 AllowCredentials，
	// 避免 "*" + 携带凭据" 这一典型误配。
	CORSAllowOrigins []string `yaml:"cors_allow_origins"`
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
