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
