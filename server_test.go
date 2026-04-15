package main

import (
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

func TestBuildVFakaRequestStars(t *testing.T) {
	fields := fieldBag{
		"order_no":       "VF001",
		"quantity":       "2",
		"query_password": "@target_user",
	}
	query := url.Values{
		"type":          []string{"stars"},
		"stars":         []string{"500"},
		"username_from": []string{"query_password"},
	}

	req, err := buildVFakaRequest(fields, query)
	if err != nil {
		t.Fatalf("buildVFakaRequest 返回错误: %v", err)
	}

	if req.Username != "target_user" {
		t.Fatalf("用户名解析错误: %s", req.Username)
	}
	if req.Stars != 1000 {
		t.Fatalf("Stars 计算错误: %d", req.Stars)
	}
}

func TestBuildVFakaRequestStarsFromOrderQuantity(t *testing.T) {
	fields := fieldBag{
		"order_no":       "VF003",
		"quantity":       "350",
		"query_password": "@dynamic_user",
	}
	query := url.Values{
		"type":          []string{"stars"},
		"username_from": []string{"query_password"},
	}

	req, err := buildVFakaRequest(fields, query)
	if err != nil {
		t.Fatalf("buildVFakaRequest 返回错误: %v", err)
	}

	if req.Username != "dynamic_user" {
		t.Fatalf("用户名解析错误: %s", req.Username)
	}
	if req.Stars != 350 {
		t.Fatalf("动态 Stars 计算错误: %d", req.Stars)
	}
}

func TestBuildVFakaRequestPremiumFromEmail(t *testing.T) {
	fields := fieldBag{
		"order_no": "VF002",
		"email":    "buyer_name@example.com",
	}
	query := url.Values{
		"type":          []string{"premium"},
		"duration":      []string{"6"},
		"username_from": []string{"email"},
	}

	req, err := buildVFakaRequest(fields, query)
	if err != nil {
		t.Fatalf("buildVFakaRequest 返回错误: %v", err)
	}

	if req.Username != "buyer_name" {
		t.Fatalf("邮箱用户名提取错误: %s", req.Username)
	}
	if req.DurationMonths != 6 {
		t.Fatalf("月数错误: %d", req.DurationMonths)
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
