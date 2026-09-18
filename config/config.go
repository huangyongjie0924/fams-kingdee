package config

import (
	"fmt"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Addr        string `yaml:"addr"`
		TLSAddr     string `yaml:"tls_addr"` // HTTPS 监听地址，留空则不启用
		CertFile    string `yaml:"cert_file"`
		KeyFile     string `yaml:"key_file"`
		UploadDir   string `yaml:"upload_dir"`
		MaxUploadMB int64  `yaml:"max_upload_mb"`
	} `yaml:"server"`

	MySQL struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Database string `yaml:"database"`
	} `yaml:"mysql"`

	Auth struct {
		JWTSecret         string `yaml:"jwt_secret"`
		TokenHours        int    `yaml:"token_hours"`
		InitAdminUser     string `yaml:"init_admin_user"`
		InitAdminPassword string `yaml:"init_admin_password"`
	} `yaml:"auth"`

	CodeRule struct {
		Prefix   string `yaml:"prefix"`
		SeqWidth int    `yaml:"seq_width"`
	} `yaml:"code_rule"`

	Kingdee struct {
		BaseURL             string `yaml:"base_url"`
		ClientID            string `yaml:"client_id"`
		ClientSecret        string `yaml:"client_secret"`
		Username            string `yaml:"username"`
		AccountID           string `yaml:"account_id"`
		RequestTimeout      int    `yaml:"request_timeout"`
		PageSize            int    `yaml:"page_size"`
		MaxRetries          int    `yaml:"max_retries"`
		QueryPath           string `yaml:"query_path"`
		PersonnelQueryPath  string `yaml:"personnel_query_path"`
		DepartmentQueryPath string `yaml:"department_query_path"`
	} `yaml:"kingdee"`

	Yunzhijia struct {
		BaseURL string `yaml:"base_url"`
		AppID   string `yaml:"app_id"`
		Secret  string `yaml:"secret"`
	} `yaml:"yunzhijia"`

	Sync struct {
		Enable          bool `yaml:"enable"`
		IntervalMinutes int  `yaml:"interval_minutes"`
		DryRun          bool `yaml:"dry_run"`
		AllowFullSync   bool `yaml:"allow_full_sync"`
	} `yaml:"sync"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.UploadDir == "" {
		c.Server.UploadDir = "uploads"
	}
	if c.Server.MaxUploadMB <= 0 {
		c.Server.MaxUploadMB = 10
	}
	if c.Auth.TokenHours <= 0 {
		c.Auth.TokenHours = 12
	}
	if c.CodeRule.SeqWidth <= 0 {
		c.CodeRule.SeqWidth = 5
	}
	if c.Kingdee.RequestTimeout <= 0 {
		c.Kingdee.RequestTimeout = 30
	}
	if c.Kingdee.PageSize <= 0 {
		c.Kingdee.PageSize = 200
	}
	if c.Kingdee.PageSize > 1000 {
		c.Kingdee.PageSize = 1000
	}
	if c.Kingdee.MaxRetries < 0 {
		c.Kingdee.MaxRetries = 3
	}
	if c.Kingdee.QueryPath == "" {
		c.Kingdee.QueryPath = "/v2/gcgs/fa/fa_asset_card/Select_AssetCard"
	}
	if c.Kingdee.PersonnelQueryPath == "" {
		c.Kingdee.PersonnelQueryPath = "/v2/gcgs/base/bos_user/query-personnel"
	}
	if c.Kingdee.DepartmentQueryPath == "" {
		c.Kingdee.DepartmentQueryPath = "/v2/gcgs/base/bos_adminorg/query-department"
	}
	if c.Sync.IntervalMinutes <= 0 {
		c.Sync.IntervalMinutes = 60
	}
	// 云之家基础地址不设默认值：各企业用自己的私有化域名，写死只会让
	// 「配置漏填」表现为运行时连不上。留空时仍可用 YZJ_BASE_URL 注入。

	// 敏感字段允许通过环境变量覆盖，避免写入配置文件
	overrideIfSet("KINGDEE_BASE_URL", &c.Kingdee.BaseURL)
	overrideIfSet("KINGDEE_CLIENT_ID", &c.Kingdee.ClientID)
	overrideIfSet("KINGDEE_CLIENT_SECRET", &c.Kingdee.ClientSecret)
	overrideIfSet("KINGDEE_USERNAME", &c.Kingdee.Username)
	overrideIfSet("KINGDEE_ACCOUNT_ID", &c.Kingdee.AccountID)
	overrideIfSet("YZJ_BASE_URL", &c.Yunzhijia.BaseURL)
	overrideIfSet("YZJ_APP_ID", &c.Yunzhijia.AppID)
	overrideIfSet("YZJ_SECRET", &c.Yunzhijia.Secret)

	if c.Kingdee.BaseURL == "" || c.Kingdee.ClientID == "" || c.Kingdee.ClientSecret == "" ||
		c.Kingdee.Username == "" || c.Kingdee.AccountID == "" {
		// 未配置金蝶时关闭同步，避免启动失败
		c.Sync.Enable = false
	}
	if c.Auth.JWTSecret == "" {
		return nil, fmt.Errorf("auth.jwt_secret must be set")
	}
	return &c, nil
}

func overrideIfSet(key string, target *string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

// DSN 固定用 +08:00 数值偏移，不用命名时区：MySQL 实例可能缺时区表。
func (c *Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local&time_zone=%s",
		c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database,
		url.QueryEscape("'+08:00'"))
}
