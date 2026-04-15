package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeBuyCardURL(t *testing.T) {
	value, err := normalizeBuyCardURL(" https://shop.example.com/buy?item=card ")
	if err != nil {
		t.Fatalf("有效购买地址不应报错: %v", err)
	}
	if value != "https://shop.example.com/buy?item=card" {
		t.Fatalf("购买地址清洗结果错误: %s", value)
	}

	if _, err := normalizeBuyCardURL("javascript:alert(1)"); err == nil {
		t.Fatalf("非法协议应被拒绝")
	}
}

func TestSiteSettingsStorePersistsBuyCardURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site_settings.json")

	store, err := NewSiteSettingsStore(path)
	if err != nil {
		t.Fatalf("初始化设置存储失败: %v", err)
	}

	saved, err := store.SetBuyCardURL("https://shop.example.com/gift-cards")
	if err != nil {
		t.Fatalf("保存购买地址失败: %v", err)
	}
	if saved.BuyCardURL != "https://shop.example.com/gift-cards" {
		t.Fatalf("保存后的购买地址错误: %s", saved.BuyCardURL)
	}

	loaded, err := NewSiteSettingsStore(path)
	if err != nil {
		t.Fatalf("重新加载设置存储失败: %v", err)
	}
	if loaded.Get().BuyCardURL != "https://shop.example.com/gift-cards" {
		t.Fatalf("重新加载后的购买地址错误: %s", loaded.Get().BuyCardURL)
	}
}
