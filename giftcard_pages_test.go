package main

import (
	"strings"
	"testing"
)

func TestGiftCardTemplateIncludesMobileSafeDetailWrapping(t *testing.T) {
	requiredSnippets := []string{
		".detail-card span",
		"overflow-wrap: anywhere;",
		".code {",
		"max-width: 100%;",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(giftCardPageTemplate, snippet) {
			t.Fatalf("兑换详情页缺少移动端防溢出样式片段: %s", snippet)
		}
	}
}

func TestGiftCardTemplateIncludesBuyCardButtonAndUpdatedUsernameExample(t *testing.T) {
	requiredSnippets := []string{
		"购买卡密",
		`target="_blank"`,
		`rel="noreferrer noopener"`,
		`placeholder="例如 liuyifei 或 @liuyifei"`,
		"Telegram 用户名（不是昵称）",
		"不是昵称/显示名",
		`id="recipient-preview"`,
		"/api/redeem/recipient?username=",
		"已找到 Telegram 用户",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(giftCardPageTemplate, snippet) {
			t.Fatalf("兑换页缺少新的购买入口或用户名示例: %s", snippet)
		}
	}
}
