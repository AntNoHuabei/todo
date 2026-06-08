package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	Model ModelConfig `json:"model"`
	WeCom WeComConfig `json:"wecom"`
	Local LocalConfig `json:"local"`
}

type ModelConfig struct {
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
}

type WeComConfig struct {
	BotID        string `json:"bot_id"`
	Secret       string `json:"secret"`
	WebSocketURL string `json:"websocket_url"`
	HomeChatID   string `json:"home_chat_id"`
}

type LocalConfig struct {
	Timezone string `json:"timezone"`
	DataDir  string `json:"data_dir"`
	LogLevel string `json:"log_level"`
}

func Default() Config {
	return Config{
		Model: ModelConfig{
			BaseURL:     "https://api.openai.com/v1",
			Model:       "gpt-4o-mini",
			Temperature: 0.2,
		},
		WeCom: WeComConfig{
			WebSocketURL: "wss://openws.work.weixin.qq.com",
		},
		Local: LocalConfig{
			Timezone: time.Local.String(),
			DataDir:  DefaultDataDir(),
			LogLevel: "info",
		},
	}
}

func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "data"
	}
	return filepath.Join(home, ".todo-assistant")
}

func LoadOrDefault(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if len(b) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	cfg.fillDefaults()
	return cfg, nil
}

func Save(path string, cfg Config) error {
	cfg.fillDefaults()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) fillDefaults() {
	def := Default()
	if c.Model.BaseURL == "" {
		c.Model.BaseURL = def.Model.BaseURL
	}
	if c.Model.Model == "" {
		c.Model.Model = def.Model.Model
	}
	if c.Model.Temperature == 0 {
		c.Model.Temperature = def.Model.Temperature
	}
	if c.WeCom.WebSocketURL == "" {
		c.WeCom.WebSocketURL = def.WeCom.WebSocketURL
	}
	if c.Local.Timezone == "" {
		c.Local.Timezone = def.Local.Timezone
	}
	if c.Local.DataDir == "" {
		c.Local.DataDir = def.Local.DataDir
	}
	if c.Local.LogLevel == "" {
		c.Local.LogLevel = def.Local.LogLevel
	}
}

func (c Config) Location() (*time.Location, error) {
	if c.Local.Timezone == "" || c.Local.Timezone == "Local" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Local.Timezone)
}
