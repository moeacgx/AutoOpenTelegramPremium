package main

import (
	"path/filepath"
	"testing"
)

func TestGiftCardStoreLifecycle(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "gift_cards.json")
	store, err := NewGiftCardStore(storePath)
	if err != nil {
		t.Fatalf("初始化卡密库失败: %v", err)
	}

	created, err := store.Generate(GiftCardSpec{
		ProductType: ProductStars,
		Stars:       500,
		Note:        "测试批次",
	}, 2)
	if err != nil {
		t.Fatalf("生成卡密失败: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("卡密数量错误: %d", len(created))
	}

	locked, err := store.Reserve(created[0].Code, "redeem-task-1")
	if err != nil {
		t.Fatalf("锁定卡密失败: %v", err)
	}
	if locked.Status != GiftCardRedeeming {
		t.Fatalf("卡密状态错误: %s", locked.Status)
	}
	if locked.TaskID != "redeem-task-1" {
		t.Fatalf("任务 ID 记录错误: %s", locked.TaskID)
	}

	if _, err := store.Reserve(created[0].Code, "redeem-task-2"); err == nil {
		t.Fatalf("重复锁定同一卡密时应该失败")
	}

	resp := FulfillResponse{
		OK:           true,
		ProductType:  ProductStars,
		Username:     "ciyuancat",
		OrderID:      "giftcard-test",
		ReqID:        "REQ-001",
		TxHashBase64: "HASH-001",
		ExplorerURL:  "https://example.com/tx/HASH-001",
		Stars:        500,
	}
	if err := store.MarkRedeemed(created[0].Code, "@ciyuancat", resp); err != nil {
		t.Fatalf("标记兑换成功失败: %v", err)
	}

	reloaded, err := NewGiftCardStore(storePath)
	if err != nil {
		t.Fatalf("重新加载卡密库失败: %v", err)
	}
	cards := reloaded.List()
	if len(cards) != 2 {
		t.Fatalf("重新加载后的卡密数量错误: %d", len(cards))
	}

	var redeemed *GiftCard
	for i := range cards {
		if normalizeCardCode(cards[i].Code) == normalizeCardCode(created[0].Code) {
			redeemed = &cards[i]
			break
		}
	}
	if redeemed == nil {
		t.Fatalf("未找到已兑换卡密")
	}
	if redeemed.Status != GiftCardRedeemed {
		t.Fatalf("兑换后状态错误: %s", redeemed.Status)
	}
	if redeemed.RedeemedBy != "ciyuancat" {
		t.Fatalf("兑换用户名记录错误: %s", redeemed.RedeemedBy)
	}
}

func TestBuildFulfillRequestFromGiftCard(t *testing.T) {
	req, err := buildFulfillRequestFromGiftCard(GiftCard{
		Code:        "TGX-ABCD-EFGH-JKLM",
		TaskID:      "redeem-001",
		ProductType: ProductStars,
		Stars:       350,
	}, "@demo_user")
	if err != nil {
		t.Fatalf("构建兑换请求失败: %v", err)
	}

	if req.Username != "demo_user" {
		t.Fatalf("用户名标准化失败: %s", req.Username)
	}
	if req.Stars != 350 {
		t.Fatalf("Stars 数量错误: %d", req.Stars)
	}
	if req.OrderID != "redeem-001" {
		t.Fatalf("订单号错误: %s", req.OrderID)
	}
}

func TestGiftCardStoreDeleteCodes(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "gift_cards.json")
	store, err := NewGiftCardStore(storePath)
	if err != nil {
		t.Fatalf("初始化卡密库失败: %v", err)
	}

	created, err := store.Generate(GiftCardSpec{
		ProductType: ProductStars,
		Stars:       50,
	}, 3)
	if err != nil {
		t.Fatalf("生成卡密失败: %v", err)
	}

	deleted, err := store.DeleteCodes([]string{created[0].Code, created[1].Code})
	if err != nil {
		t.Fatalf("删除卡密失败: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("删除数量错误: %d", deleted)
	}

	left := store.List()
	if len(left) != 1 {
		t.Fatalf("删除后的剩余数量错误: %d", len(left))
	}
	if normalizeCardCode(left[0].Code) != normalizeCardCode(created[2].Code) {
		t.Fatalf("剩余卡密不符合预期: %s", left[0].Code)
	}
}
