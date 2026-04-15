package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	ResHash        string
	ResCookie      string
	ResDH          string
	TonAccount     string
	TonDevice      string
	WalletMnemonic string
	WalletVersion  string

	OpenType     string
	OpenUserName string
	OpenDuration int
	OpenStars    int

	ListenAddr     string
	HookToken      string
	AdminToken     string
	CardStorePath  string
	QueueWorkers   int
	RequestTimeout time.Duration
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	timeoutSeconds := 90
	if raw := strings.TrimSpace(os.Getenv("RequestTimeoutSeconds")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return Config{}, fmt.Errorf("RequestTimeoutSeconds 配置无效: %q", raw)
		}
		timeoutSeconds = value
	}

	duration, err := parseOptionalIntEnv("OpenDuration")
	if err != nil {
		return Config{}, err
	}

	stars, err := parseOptionalIntEnv("OpenStars")
	if err != nil {
		return Config{}, err
	}

	queueWorkers := 1
	if raw := strings.TrimSpace(os.Getenv("QueueWorkers")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return Config{}, fmt.Errorf("QueueWorkers 配置无效: %q", raw)
		}
		queueWorkers = value
	}

	return Config{
		ResHash:        strings.TrimSpace(os.Getenv("ResHash")),
		ResCookie:      strings.TrimSpace(os.Getenv("ResCookie")),
		ResDH:          strings.TrimSpace(os.Getenv("ResDH")),
		TonAccount:     strings.TrimSpace(os.Getenv("TonAccount")),
		TonDevice:      strings.TrimSpace(os.Getenv("TonDevice")),
		WalletMnemonic: strings.TrimSpace(os.Getenv("WalletMnemonic")),
		WalletVersion:  strings.TrimSpace(os.Getenv("WalletVersion")),
		OpenType:       strings.TrimSpace(strings.ToLower(os.Getenv("OpenType"))),
		OpenUserName:   strings.TrimSpace(os.Getenv("OpenUserName")),
		OpenDuration:   duration,
		OpenStars:      stars,
		ListenAddr:     strings.TrimSpace(os.Getenv("ListenAddr")),
		HookToken:      strings.TrimSpace(os.Getenv("HookToken")),
		AdminToken:     strings.TrimSpace(os.Getenv("AdminToken")),
		CardStorePath:  strings.TrimSpace(os.Getenv("CardStorePath")),
		QueueWorkers:   queueWorkers,
		RequestTimeout: time.Duration(timeoutSeconds) * time.Second,
	}, nil
}

func parseOptionalIntEnv(key string) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 配置无效: %q", key, raw)
	}
	return value, nil
}

func (c Config) LegacyRequest() (FulfillRequest, error) {
	productType := c.OpenType
	if productType == "" {
		if c.OpenStars > 0 {
			productType = string(ProductStars)
		} else {
			productType = string(ProductPremium)
		}
	}

	pt, err := ParseProductType(productType)
	if err != nil {
		return FulfillRequest{}, err
	}

	req := FulfillRequest{
		ProductType: pt,
		Username:    normalizeUsername(c.OpenUserName),
		ShowSender:  true,
		Source:      "legacy-env",
	}

	switch pt {
	case ProductPremium:
		req.DurationMonths = c.OpenDuration
	case ProductStars:
		req.Stars = c.OpenStars
	default:
		return FulfillRequest{}, fmt.Errorf("不支持的商品类型: %s", pt)
	}

	if err := req.Validate(); err != nil {
		return FulfillRequest{}, err
	}
	return req, nil
}

func (c Config) EffectiveAdminToken() string {
	if strings.TrimSpace(c.AdminToken) != "" {
		return strings.TrimSpace(c.AdminToken)
	}
	return strings.TrimSpace(c.HookToken)
}

func (c Config) EffectiveCardStorePath() string {
	if strings.TrimSpace(c.CardStorePath) != "" {
		return strings.TrimSpace(c.CardStorePath)
	}
	return "data/gift_cards.json"
}
