package main

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBuildManualRequestPremium(t *testing.T) {
	fields := fieldBag{
		"type":     "premium",
		"username": "@demo_user",
		"duration": "3",
		"order_id": "M001",
		"dry_run":  "1",
	}

	req, err := buildManualRequest(fields, url.Values{})
	if err != nil {
		t.Fatalf("buildManualRequest 返回错误: %v", err)
	}

	if req.ProductType != ProductPremium {
		t.Fatalf("商品类型错误: %v", req.ProductType)
	}
	if req.Username != "demo_user" {
		t.Fatalf("用户名解析错误: %s", req.Username)
	}
	if req.DurationMonths != 3 {
		t.Fatalf("月数错误: %d", req.DurationMonths)
	}
	if !req.DryRun {
		t.Fatalf("dry_run 未正确解析")
	}
}

func TestApplyWalletFields(t *testing.T) {
	service := &FragmentService{
		tonAccount: `{"address":"0:test"}`,
		tonDevice:  `{"appName":"tonkeeper"}`,
	}
	values := url.Values{}

	service.applyWalletFields(values)

	if values.Get("transaction") != "1" {
		t.Fatalf("transaction 字段错误: %q", values.Get("transaction"))
	}
	if values.Get("account") == "" {
		t.Fatalf("account 字段为空")
	}
	if values.Get("device") == "" {
		t.Fatalf("device 字段为空")
	}
}

func TestBearerTokenFromHeader(t *testing.T) {
	if got := bearerTokenFromHeader("Bearer demo-token"); got != "demo-token" {
		t.Fatalf("Bearer token 解析错误: %q", got)
	}
	if got := bearerTokenFromHeader("Basic demo-token"); got != "" {
		t.Fatalf("非 Bearer 请求头不应被接受: %q", got)
	}
}

func TestClientIPFromRequestPrefersCloudflareHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "198.51.100.10:443"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.8, 198.51.100.10")

	if got := clientIPFromRequest(req); got != "203.0.113.9" {
		t.Fatalf("真实来源 IP 读取错误，期望优先取 CF-Connecting-IP，实际: %q", got)
	}
}
