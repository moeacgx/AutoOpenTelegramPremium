package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type SiteSettings struct {
	BuyCardURL string    `json:"buy_card_url,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

type SiteSettingsStore struct {
	mu   sync.Mutex
	path string
	data SiteSettings
}

func NewSiteSettingsStore(path string) (*SiteSettingsStore, error) {
	store := &SiteSettingsStore{path: path}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SiteSettingsStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取页面设置失败: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}

	var data SiteSettings
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("解析页面设置失败: %w", err)
	}

	cleaned, err := normalizeBuyCardURL(data.BuyCardURL)
	if err != nil {
		return err
	}
	data.BuyCardURL = cleaned
	s.data = data
	return nil
}

func (s *SiteSettingsStore) Get() SiteSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

func (s *SiteSettingsStore) SetBuyCardURL(raw string) (SiteSettings, error) {
	cleaned, err := normalizeBuyCardURL(raw)
	if err != nil {
		return SiteSettings{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.BuyCardURL = cleaned
	s.data.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return SiteSettings{}, err
	}
	return s.data, nil
}

func (s *SiteSettingsStore) saveLocked() error {
	payload, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化页面设置失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("创建页面设置目录失败: %w", err)
	}
	if err := os.WriteFile(s.path, payload, 0o600); err != nil {
		return fmt.Errorf("写入页面设置失败: %w", err)
	}
	return nil
}

func normalizeBuyCardURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("购买卡密地址必须是完整的 http 或 https 链接")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("购买卡密地址必须是完整的 http 或 https 链接")
	}
}
