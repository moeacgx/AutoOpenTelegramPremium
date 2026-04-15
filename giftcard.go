package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type GiftCardStatus string

const (
	GiftCardAvailable GiftCardStatus = "available"
	GiftCardRedeeming GiftCardStatus = "redeeming"
	GiftCardRedeemed  GiftCardStatus = "redeemed"
)

type GiftCard struct {
	Code           string         `json:"code"`
	ProductType    ProductType    `json:"type"`
	Stars          int            `json:"stars,omitempty"`
	DurationMonths int            `json:"duration,omitempty"`
	Status         GiftCardStatus `json:"status"`
	Note           string         `json:"note,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	RedeemedAt     *time.Time     `json:"redeemed_at,omitempty"`
	RedeemedBy     string         `json:"redeemed_by,omitempty"`
	OrderID        string         `json:"order_id,omitempty"`
	ReqID          string         `json:"req_id,omitempty"`
	TxHashBase64   string         `json:"tx_hash_base64,omitempty"`
	ExplorerURL    string         `json:"explorer_url,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
}

type GiftCardSpec struct {
	ProductType    ProductType
	Stars          int
	DurationMonths int
	Note           string
}

type GiftCardStore struct {
	mu   sync.Mutex
	path string
	data map[string]*GiftCard
}

func NewGiftCardStore(path string) (*GiftCardStore, error) {
	store := &GiftCardStore{
		path: path,
		data: make(map[string]*GiftCard),
	}

	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *GiftCardStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取卡密库失败: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}

	var cards []GiftCard
	if err := json.Unmarshal(raw, &cards); err != nil {
		return fmt.Errorf("解析卡密库失败: %w", err)
	}

	for _, card := range cards {
		copyCard := card
		s.data[normalizeCardCode(card.Code)] = &copyCard
	}
	return nil
}

func (s *GiftCardStore) saveLocked() error {
	cards := make([]GiftCard, 0, len(s.data))
	for _, card := range s.data {
		cards = append(cards, *card)
	}

	sort.Slice(cards, func(i, j int) bool {
		if cards[i].CreatedAt.Equal(cards[j].CreatedAt) {
			return cards[i].Code > cards[j].Code
		}
		return cards[i].CreatedAt.After(cards[j].CreatedAt)
	})

	payload, err := json.MarshalIndent(cards, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化卡密库失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("创建卡密目录失败: %w", err)
	}

	if err := os.WriteFile(s.path, payload, 0o600); err != nil {
		return fmt.Errorf("写入卡密库失败: %w", err)
	}
	return nil
}

func (s *GiftCardStore) List() []GiftCard {
	s.mu.Lock()
	defer s.mu.Unlock()

	cards := make([]GiftCard, 0, len(s.data))
	for _, card := range s.data {
		cards = append(cards, *card)
	}

	sort.Slice(cards, func(i, j int) bool {
		if cards[i].CreatedAt.Equal(cards[j].CreatedAt) {
			return cards[i].Code > cards[j].Code
		}
		return cards[i].CreatedAt.After(cards[j].CreatedAt)
	})
	return cards
}

func (s *GiftCardStore) Generate(spec GiftCardSpec, count int) ([]GiftCard, error) {
	if count <= 0 || count > 200 {
		return nil, fmt.Errorf("生成数量必须在 1 到 200 之间")
	}

	req, err := spec.toFulfillRequest("generator-preview")
	if err != nil {
		return nil, err
	}
	_ = req

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	created := make([]GiftCard, 0, count)
	for i := 0; i < count; i++ {
		displayCode, normalizedCode, err := s.newCodeLocked()
		if err != nil {
			return nil, err
		}

		card := GiftCard{
			Code:           displayCode,
			ProductType:    spec.ProductType,
			Stars:          spec.Stars,
			DurationMonths: spec.DurationMonths,
			Status:         GiftCardAvailable,
			Note:           strings.TrimSpace(spec.Note),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		s.data[normalizedCode] = &card
		created = append(created, card)
	}

	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *GiftCardStore) Reserve(code string) (GiftCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	card, ok := s.data[normalizeCardCode(code)]
	if !ok {
		return GiftCard{}, fmt.Errorf("卡密不存在")
	}

	switch card.Status {
	case GiftCardRedeemed:
		return GiftCard{}, fmt.Errorf("卡密已使用")
	case GiftCardRedeeming:
		return GiftCard{}, fmt.Errorf("卡密正在处理中，请稍后再试")
	}

	card.Status = GiftCardRedeeming
	card.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return GiftCard{}, err
	}
	return *card, nil
}

func (s *GiftCardStore) MarkAvailable(code string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	card, ok := s.data[normalizeCardCode(code)]
	if !ok {
		return fmt.Errorf("卡密不存在")
	}

	card.Status = GiftCardAvailable
	card.LastError = strings.TrimSpace(errMsg)
	card.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *GiftCardStore) MarkRedeemed(code string, username string, resp FulfillResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	card, ok := s.data[normalizeCardCode(code)]
	if !ok {
		return fmt.Errorf("卡密不存在")
	}

	now := time.Now().UTC()
	card.Status = GiftCardRedeemed
	card.RedeemedAt = &now
	card.RedeemedBy = normalizeUsername(username)
	card.OrderID = resp.OrderID
	card.ReqID = resp.ReqID
	card.TxHashBase64 = resp.TxHashBase64
	card.ExplorerURL = resp.ExplorerURL
	card.LastError = ""
	card.UpdatedAt = now
	return s.saveLocked()
}

func (s *GiftCardStore) DeleteCodes(codes []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizedCodes := make([]string, 0, len(codes))
	seen := make(map[string]struct{})
	for _, code := range codes {
		normalized := normalizeCardCode(code)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		normalizedCodes = append(normalizedCodes, normalized)
	}

	if len(normalizedCodes) == 0 {
		return 0, fmt.Errorf("请至少选择一个卡密")
	}

	for _, code := range normalizedCodes {
		card, ok := s.data[code]
		if !ok {
			return 0, fmt.Errorf("卡密不存在: %s", code)
		}
		if card.Status == GiftCardRedeeming {
			return 0, fmt.Errorf("卡密正在处理中，不能删除: %s", card.Code)
		}
	}

	deleted := 0
	for _, code := range normalizedCodes {
		if _, ok := s.data[code]; ok {
			delete(s.data, code)
			deleted++
		}
	}

	if deleted == 0 {
		return 0, nil
	}

	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *GiftCardStore) newCodeLocked() (string, string, error) {
	for i := 0; i < 10; i++ {
		code, err := randomGiftCardCode()
		if err != nil {
			return "", "", err
		}
		normalized := normalizeCardCode(code)
		if _, exists := s.data[normalized]; !exists {
			return code, normalized, nil
		}
	}
	return "", "", fmt.Errorf("生成卡密失败，请重试")
}

func (g GiftCardSpec) toFulfillRequest(username string) (FulfillRequest, error) {
	req := FulfillRequest{
		ProductType: g.ProductType,
		Username:    normalizeUsername(username),
		ShowSender:  true,
	}

	switch g.ProductType {
	case ProductStars:
		req.Stars = g.Stars
	case ProductPremium:
		req.DurationMonths = g.DurationMonths
	default:
		return FulfillRequest{}, fmt.Errorf("不支持的商品类型")
	}

	if err := req.Validate(); err != nil {
		return FulfillRequest{}, err
	}
	return req, nil
}

func buildFulfillRequestFromGiftCard(card GiftCard, username string) (FulfillRequest, error) {
	req, err := GiftCardSpec{
		ProductType:    card.ProductType,
		Stars:          card.Stars,
		DurationMonths: card.DurationMonths,
	}.toFulfillRequest(username)
	if err != nil {
		return FulfillRequest{}, err
	}

	req.OrderID = "giftcard-" + normalizeCardCode(card.Code)
	req.Source = "giftcard-site"
	return req, nil
}

func normalizeCardCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

func randomGiftCardCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buffer := make([]byte, 12)
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("随机数生成失败: %w", err)
	}

	for i, value := range random {
		buffer[i] = alphabet[int(value)%len(alphabet)]
	}

	return fmt.Sprintf("TGX-%s-%s-%s",
		string(buffer[0:4]),
		string(buffer[4:8]),
		string(buffer[8:12]),
	), nil
}
