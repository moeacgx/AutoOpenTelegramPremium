package main

import "testing"

func TestRecipientPreviewFromFound(t *testing.T) {
	preview := recipientPreviewFromFound("target_user", map[string]interface{}{
		"recipient":    "123456",
		"display_name": "目标用户",
		"photo_url":    "https://example.com/avatar.jpg",
	})

	if !preview.Found {
		t.Fatalf("预览结果应该标记为已找到")
	}
	if preview.Username != "target_user" {
		t.Fatalf("用户名兜底错误: %s", preview.Username)
	}
	if preview.Recipient != "123456" {
		t.Fatalf("recipient 解析错误: %s", preview.Recipient)
	}
	if preview.DisplayName != "目标用户" {
		t.Fatalf("显示名解析错误: %s", preview.DisplayName)
	}
	if preview.PhotoURL == "" {
		t.Fatalf("头像地址应被解析")
	}
}
