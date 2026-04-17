package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FragmentService struct {
	client     *http.Client
	apiURL     string
	cookie     string
	tonAccount string
	tonDevice  string
	mu         sync.Mutex
	dh         string
}

type RawPayment struct {
	AmountTON     string
	PayloadBase64 string
}

type RecipientPreview struct {
	Found       bool   `json:"found"`
	Username    string `json:"username"`
	Recipient   string `json:"recipient,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	PhotoURL    string `json:"photo_url,omitempty"`
}

func NewFragmentService(cfg Config) (*FragmentService, error) {
	if cfg.ResHash == "" {
		return nil, fmt.Errorf("缺少 ResHash 配置")
	}
	if cfg.ResCookie == "" {
		return nil, fmt.Errorf("缺少 ResCookie 配置")
	}
	if (cfg.TonAccount == "") != (cfg.TonDevice == "") {
		return nil, fmt.Errorf("TonAccount 与 TonDevice 必须同时配置或同时留空")
	}

	return &FragmentService{
		client:     &http.Client{Timeout: cfg.RequestTimeout},
		apiURL:     fmt.Sprintf("https://fragment.com/api?hash=%s", cfg.ResHash),
		cookie:     cfg.ResCookie,
		tonAccount: cfg.TonAccount,
		tonDevice:  cfg.TonDevice,
		dh:         cfg.ResDH,
	}, nil
}

func (s *FragmentService) SearchRecipient(ctx context.Context, req FulfillRequest) (string, error) {
	found, err := s.searchRecipient(ctx, req)
	if err != nil {
		return "", err
	}

	recipient, ok := found["recipient"].(string)
	if !ok || strings.TrimSpace(recipient) == "" {
		return "", fmt.Errorf("Fragment 返回缺少 recipient")
	}
	return recipient, nil
}

func (s *FragmentService) PreviewRecipient(ctx context.Context, username string) (RecipientPreview, error) {
	normalized := normalizeUsername(username)
	if normalized == "" {
		return RecipientPreview{}, fmt.Errorf("Telegram 用户名不能为空")
	}

	found, err := s.searchRecipient(ctx, FulfillRequest{
		ProductType: ProductStars,
		Username:    normalized,
	})
	if err != nil {
		return RecipientPreview{}, err
	}

	return recipientPreviewFromFound(normalized, found), nil
}

func (s *FragmentService) searchRecipient(ctx context.Context, req FulfillRequest) (map[string]interface{}, error) {
	data := url.Values{}
	data.Set("query", req.Username)

	switch req.ProductType {
	case ProductPremium:
		data.Set("months", strconv.Itoa(req.DurationMonths))
		data.Set("method", "searchPremiumGiftRecipient")
	case ProductStars:
		data.Set("method", "searchStarsRecipient")
	default:
		return nil, fmt.Errorf("未知商品类型: %s", req.ProductType)
	}

	result, err := s.postAPI(ctx, data, "")
	if err != nil {
		return nil, err
	}

	okValue, ok := result["ok"].(bool)
	if !ok || !okValue {
		return nil, fmt.Errorf("Fragment 搜索用户失败")
	}

	found, ok := result["found"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Fragment 返回缺少 found 字段")
	}

	return found, nil
}

func (s *FragmentService) BootstrapSession(ctx context.Context, productType ProductType) error {
	data := url.Values{}

	switch productType {
	case ProductPremium:
		data.Set("method", "updatePremiumState")
	case ProductStars:
		data.Set("method", "updateStarsBuyState")
	default:
		return fmt.Errorf("未知商品类型: %s", productType)
	}
	s.applySessionFields(data)

	result, err := s.postAPI(ctx, data, "")
	if err != nil {
		return err
	}
	if _, exists := result["error"]; exists {
		return fmt.Errorf("Fragment 会话初始化失败: %v", result["error"])
	}
	return nil
}

func (s *FragmentService) InitOrder(ctx context.Context, req FulfillRequest, recipient string) (string, error) {
	data := url.Values{}
	data.Set("recipient", recipient)

	switch req.ProductType {
	case ProductPremium:
		data.Set("months", strconv.Itoa(req.DurationMonths))
		data.Set("method", "initGiftPremiumRequest")
	case ProductStars:
		data.Set("quantity", strconv.Itoa(req.Stars))
		data.Set("method", "initBuyStarsRequest")
	default:
		return "", fmt.Errorf("未知商品类型: %s", req.ProductType)
	}

	result, err := s.postAPI(ctx, data, s.refererForOrder(req, recipient))
	if err != nil {
		return "", err
	}

	reqID, ok := result["req_id"].(string)
	if !ok || strings.TrimSpace(reqID) == "" {
		return "", fmt.Errorf("Fragment 返回缺少 req_id")
	}
	return reqID, nil
}

func (s *FragmentService) ConfirmOrder(ctx context.Context, req FulfillRequest, reqID string, recipient string) (time.Time, *RawPayment, error) {
	data := url.Values{}
	data.Set("id", reqID)
	if req.ShowSender {
		data.Set("show_sender", "1")
	} else {
		data.Set("show_sender", "0")
	}
	s.applyWalletFields(data)

	switch req.ProductType {
	case ProductPremium:
		data.Set("method", "getGiftPremiumLink")
	case ProductStars:
		data.Set("method", "getBuyStarsLink")
	default:
		return time.Time{}, nil, fmt.Errorf("未知商品类型: %s", req.ProductType)
	}

	result, err := s.postAPI(ctx, data, s.refererForOrder(req, recipient))
	if err != nil {
		return time.Time{}, nil, err
	}

	okValue, ok := result["ok"].(bool)
	if !ok || !okValue {
		if errText := fragmentError(result); errText != "" {
			return time.Time{}, nil, fmt.Errorf("Fragment 订单确认失败: %s", errText)
		}
		return time.Time{}, nil, fmt.Errorf("Fragment 订单确认失败")
	}

	if payment, expireAt, ok := parseTransactionPayment(result); ok {
		return expireAt, &payment, nil
	}

	expireAfter, ok := result["expire_after"].(float64)
	if !ok {
		return time.Time{}, nil, fmt.Errorf("Fragment 返回缺少 expire_after 或 transaction")
	}
	return time.Now().Add(time.Duration(expireAfter) * time.Second), nil, nil
}

func (s *FragmentService) applyWalletFields(data url.Values) {
	if s.tonAccount == "" || s.tonDevice == "" {
		return
	}

	// Fragment 的 TonConnect v2 流程会校验钱包运行态字段。
	data.Set("account", s.tonAccount)
	data.Set("device", s.tonDevice)
	data.Set("transaction", "1")
}

func fragmentError(result map[string]interface{}) string {
	value, ok := result["error"]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

func recipientPreviewFromFound(username string, found map[string]interface{}) RecipientPreview {
	return RecipientPreview{
		Found:       true,
		Username:    normalizeUsername(firstStringValue(found, "username", "user", "slug")),
		Recipient:   strings.TrimSpace(firstStringValue(found, "recipient")),
		DisplayName: strings.TrimSpace(firstStringValue(found, "name", "title", "display_name", "displayName", "label")),
		PhotoURL:    strings.TrimSpace(firstStringValue(found, "photo", "photo_url", "photoURL", "avatar", "avatar_url")),
	}.withFallbackUsername(username)
}

func (p RecipientPreview) withFallbackUsername(username string) RecipientPreview {
	if strings.TrimSpace(p.Username) == "" {
		p.Username = normalizeUsername(username)
	}
	return p
}

func firstStringValue(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		if typed, ok := value.(string); ok {
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func (s *FragmentService) GetRawRequest(ctx context.Context, reqID string) (RawPayment, error) {
	requestURL := fmt.Sprintf("https://fragment.com/tonkeeper/rawRequest?id=%s", url.QueryEscape(reqID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return RawPayment{}, err
	}
	req.Header.Set("Cookie", s.cookie)

	resp, err := s.client.Do(req)
	if err != nil {
		return RawPayment{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RawPayment{}, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return RawPayment{}, fmt.Errorf("rawRequest 解析失败: %w", err)
	}

	bodyObj, ok := result["body"].(map[string]interface{})
	if !ok {
		return RawPayment{}, fmt.Errorf("rawRequest 缺少 body 字段")
	}

	params, ok := bodyObj["params"].(map[string]interface{})
	if !ok {
		return RawPayment{}, fmt.Errorf("rawRequest 缺少 params 字段")
	}

	messages, ok := params["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return RawPayment{}, fmt.Errorf("rawRequest 缺少 messages")
	}

	firstMessage, ok := messages[0].(map[string]interface{})
	if !ok {
		return RawPayment{}, fmt.Errorf("rawRequest messages[0] 格式无效")
	}

	amountFloat, ok := firstMessage["amount"].(float64)
	if !ok {
		return RawPayment{}, fmt.Errorf("rawRequest amount 字段无效")
	}

	payload, ok := firstMessage["payload"].(string)
	if !ok || strings.TrimSpace(payload) == "" {
		return RawPayment{}, fmt.Errorf("rawRequest payload 字段无效")
	}

	return RawPayment{
		AmountTON:     formatTONAmount(amountFloat / 1e9),
		PayloadBase64: payload,
	}, nil
}

func (s *FragmentService) postAPI(ctx context.Context, data url.Values, referer string) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", s.cookie)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Origin", "https://fragment.com")
	req.Header.Set("Pragma", "no-cache")
	if referer == "" {
		referer = "https://fragment.com/stars/buy"
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("Sec-CH-UA", `"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("Fragment 响应解析失败: %w", err)
	}
	s.updateSessionDH(result)
	return result, nil
}

func (s *FragmentService) refererForOrder(req FulfillRequest, recipient string) string {
	switch req.ProductType {
	case ProductStars:
		values := url.Values{}
		values.Set("recipient", recipient)
		values.Set("quantity", strconv.Itoa(req.Stars))
		return "https://fragment.com/stars/buy?" + values.Encode()
	default:
		return ""
	}
}

func (s *FragmentService) applySessionFields(data url.Values) {
	if data.Get("mode") == "" {
		data.Set("mode", "new")
	}
	if data.Get("lv") == "" {
		data.Set("lv", "false")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dh != "" && data.Get("dh") == "" {
		data.Set("dh", s.dh)
	}
}

func (s *FragmentService) updateSessionDH(result map[string]interface{}) {
	value, ok := result["dh"]
	if !ok {
		return
	}

	var next string
	switch typed := value.(type) {
	case string:
		next = strings.TrimSpace(typed)
	case float64:
		next = strconv.FormatInt(int64(typed), 10)
	}
	if next == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.dh = next
}

func formatTONAmount(amount float64) string {
	formatted := strconv.FormatFloat(amount, 'f', 9, 64)
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")
	if formatted == "" {
		return "0"
	}
	return formatted
}

func parseTransactionPayment(result map[string]interface{}) (RawPayment, time.Time, bool) {
	transaction, ok := result["transaction"].(map[string]interface{})
	if !ok {
		return RawPayment{}, time.Time{}, false
	}

	messages, ok := transaction["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return RawPayment{}, time.Time{}, false
	}

	firstMessage, ok := messages[0].(map[string]interface{})
	if !ok {
		return RawPayment{}, time.Time{}, false
	}

	amountNano, ok := firstMessage["amount"].(string)
	if !ok || strings.TrimSpace(amountNano) == "" {
		return RawPayment{}, time.Time{}, false
	}

	payload, ok := firstMessage["payload"].(string)
	if !ok || strings.TrimSpace(payload) == "" {
		return RawPayment{}, time.Time{}, false
	}

	expireAt := time.Now().Add(5 * time.Minute)
	if validUntil, ok := transaction["validUntil"].(float64); ok && validUntil > 0 {
		expireAt = time.Unix(int64(validUntil), 0)
	}

	return RawPayment{
		AmountTON:     formatNanoTONString(amountNano),
		PayloadBase64: payload,
	}, expireAt, true
}

func formatNanoTONString(amountNano string) string {
	nano := new(big.Int)
	if _, ok := nano.SetString(strings.TrimSpace(amountNano), 10); !ok {
		return amountNano
	}

	denom := big.NewInt(1_000_000_000)
	whole := new(big.Int).Quo(nano, denom)
	frac := new(big.Int).Mod(nano, denom)
	if frac.Sign() == 0 {
		return whole.String()
	}

	fracText := fmt.Sprintf("%09s", frac.String())
	fracText = strings.TrimRight(fracText, "0")
	return whole.String() + "." + fracText
}
