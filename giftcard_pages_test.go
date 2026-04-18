package main

import (
	"net/http/httptest"
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
		`class="redeem-assist"`,
		`actions redeem-actions{{if .BuyCardURL}}`,
		"redeem-actions-2",
		"redeem-actions-3",
		".redeem-actions .btn",
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

func TestAdminTokenFromRequestOnlyAcceptsHeaders(t *testing.T) {
	server := &HTTPServer{}

	req := httptest.NewRequest("GET", "/admin/cards?token=url-token", nil)
	req.Header.Set("Authorization", "Bearer header-token")

	if got := server.adminTokenFromRequest(req); got != "header-token" {
		t.Fatalf("应优先从请求头读取 AdminToken，实际: %q", got)
	}

	req = httptest.NewRequest("GET", "/admin/cards?token=url-token", nil)
	if got := server.adminTokenFromRequest(req); got != "" {
		t.Fatalf("URL token 不应再被后台接受，实际: %q", got)
	}
}

func TestAdminAuthPageMentionsHeaderOnlyLogin(t *testing.T) {
	html := renderAdminAuthPageHTML("请重新登录")
	requiredSnippets := []string{
		"Authorization: Bearer",
		"X-Admin-Token",
		"sessionStorage",
		"为了避免 token 继续出现在 URL",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("后台登录页缺少关键安全提示: %s", snippet)
		}
	}

	if strings.Contains(html, "?token=") {
		t.Fatalf("后台登录页不应再提示 URL token 登录")
	}
}
