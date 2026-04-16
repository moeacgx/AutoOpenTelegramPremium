package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

const fragmentWalletAddress = "EQBAjaOyi2wGWlk-EDkSabqqnF-MrrwMadnwqrurKpkla9nE"

const (
	defaultTonRetryAttempts = 3
	defaultTonRetryDelay    = 1500 * time.Millisecond
)

type tonAPI interface {
	CurrentMasterchainInfo(ctx context.Context) (*ton.BlockIDExt, error)
}

type tonWalletClient interface {
	GetBalance(ctx context.Context, block *ton.BlockIDExt) (tlb.Coins, error)
	SendManyWaitTransaction(ctx context.Context, messages []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error)
}

type tonRuntime struct {
	api    tonAPI
	wallet tonWalletClient
}

type TonPayer struct {
	mnemonic       []string
	walletVersion  wallet.VersionConfig
	runtimeFactory func(context.Context) (*tonRuntime, error)
	retryAttempts  int
	retryDelay     time.Duration
}

type PaymentResult struct {
	TxHashBase64  string
	TxHashURLSafe string
	ExplorerURL   string
	WalletBalance string
}

func NewTonPayer(cfg Config) (*TonPayer, error) {
	words := strings.Fields(cfg.WalletMnemonic)
	if len(words) == 0 {
		return nil, fmt.Errorf("缺少 WalletMnemonic 配置")
	}

	version, err := parseWalletVersion(cfg.WalletVersion)
	if err != nil {
		return nil, err
	}

	return &TonPayer{
		mnemonic:      words,
		walletVersion: version,
		runtimeFactory: func(ctx context.Context) (*tonRuntime, error) {
			client := liteclient.NewConnectionPool()
			if err := client.AddConnectionsFromConfigUrl(ctx, "https://ton-blockchain.github.io/global.config.json"); err != nil {
				return nil, fmt.Errorf("加载 TON 节点失败: %w", err)
			}

			api := ton.NewAPIClient(client)
			w, err := wallet.FromSeed(api, words, version)
			if err != nil {
				return nil, fmt.Errorf("钱包初始化失败: %w", err)
			}

			return &tonRuntime{
				api:    api,
				wallet: w,
			}, nil
		},
		retryAttempts: defaultTonRetryAttempts,
		retryDelay:    defaultTonRetryDelay,
	}, nil
}

func (p *TonPayer) Transfer(ctx context.Context, payment RawPayment) (PaymentResult, error) {
	runtime, err := p.newRuntime(ctx)
	if err != nil {
		return PaymentResult{}, err
	}

	balance, err := p.loadCurrentBalance(ctx, runtime)
	if err != nil {
		return PaymentResult{}, err
	}

	amount := tlb.MustFromTON(payment.AmountTON)
	if balance.NanoTON().Cmp(amount.NanoTON()) < 0 {
		return PaymentResult{}, fmt.Errorf("钱包余额不足，当前 %s TON，需要 %s TON", balance.TON(), payment.AmountTON)
	}

	bodyCell, err := decodePayloadCell(payment.PayloadBase64)
	if err != nil {
		return PaymentResult{}, fmt.Errorf("解析 Fragment payload 失败: %w", err)
	}

	destination := address.MustParseAddr(fragmentWalletAddress)
	messages := []*wallet.Message{
		{
			// Wallet V5 外部签名消息要求带 IgnoreErrors 位，否则合约会以 137 失败。
			Mode: wallet.PayGasSeparately | wallet.IgnoreErrors,
			InternalMessage: &tlb.InternalMessage{
				Bounce:  false,
				DstAddr: destination,
				Amount:  amount,
				Body:    bodyCell,
			},
		},
	}

	tx, confirmedBlock, err := p.sendManyWaitTransaction(ctx, runtime, messages)
	if err != nil {
		return PaymentResult{}, fmt.Errorf("发送交易失败: %w", err)
	}
	if err := ensureTransactionSucceeded(tx); err != nil {
		return PaymentResult{}, err
	}

	confirmedBalance, err := p.loadConfirmedBalance(ctx, runtime, confirmedBlock)
	if err != nil {
		// 余额只用于回显，不能因为节点短暂不同步把已确认的链上成功单打成失败，
		// 否则卡密会被重新放回可兑换状态，存在二次扣款风险。
		log.Printf("TON 交易已确认，忽略确认后余额读取失败: %v", err)
	}

	hashBase64 := base64.StdEncoding.EncodeToString(tx.Hash)
	hashURLSafe := base64.URLEncoding.EncodeToString(tx.Hash)

	return PaymentResult{
		TxHashBase64:  hashBase64,
		TxHashURLSafe: hashURLSafe,
		ExplorerURL:   "https://tonscan.org/tx/" + hashURLSafe,
		WalletBalance: confirmedBalance,
	}, nil
}

func (p *TonPayer) newRuntime(ctx context.Context) (*tonRuntime, error) {
	if p.runtimeFactory == nil {
		return nil, fmt.Errorf("TON 运行时未初始化")
	}
	return p.runtimeFactory(ctx)
}

func (p *TonPayer) loadCurrentBalance(ctx context.Context, runtime *tonRuntime) (tlb.Coins, error) {
	var balance tlb.Coins

	var block *ton.BlockIDExt
	if err := p.retryTransientTonError(ctx, func() error {
		currentBlock, err := runtime.api.CurrentMasterchainInfo(ctx)
		if err != nil {
			return err
		}
		block = currentBlock
		return nil
	}); err != nil {
		return tlb.Coins{}, fmt.Errorf("获取主链信息失败: %w", err)
	}

	if err := p.retryTransientTonError(ctx, func() error {
		currentBalance, err := runtime.wallet.GetBalance(ctx, block)
		if err != nil {
			return err
		}
		balance = currentBalance
		return nil
	}); err != nil {
		return tlb.Coins{}, fmt.Errorf("获取钱包余额失败: %w", err)
	}

	return balance, nil
}

func (p *TonPayer) sendManyWaitTransaction(ctx context.Context, runtime *tonRuntime, messages []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error) {
	var (
		tx    *tlb.Transaction
		block *ton.BlockIDExt
	)

	if err := p.retryTransientTonError(ctx, func() error {
		currentTx, currentBlock, err := runtime.wallet.SendManyWaitTransaction(ctx, messages)
		if err != nil {
			return err
		}
		tx = currentTx
		block = currentBlock
		return nil
	}); err != nil {
		return nil, nil, err
	}

	return tx, block, nil
}

func (p *TonPayer) loadConfirmedBalance(ctx context.Context, runtime *tonRuntime, confirmedBlock *ton.BlockIDExt) (string, error) {
	var balance tlb.Coins

	if confirmedBlock != nil {
		if err := p.retryTransientTonError(ctx, func() error {
			currentBalance, err := runtime.wallet.GetBalance(ctx, confirmedBlock)
			if err != nil {
				return err
			}
			balance = currentBalance
			return nil
		}); err == nil {
			return balance.TON(), nil
		} else if !isRetryableTonNodeError(err) {
			return "", fmt.Errorf("获取确认后余额失败: %w", err)
		}
	}

	if err := p.retryTransientTonError(ctx, func() error {
		block, err := runtime.api.CurrentMasterchainInfo(ctx)
		if err != nil {
			return err
		}

		currentBalance, err := runtime.wallet.GetBalance(ctx, block)
		if err != nil {
			return err
		}
		balance = currentBalance
		return nil
	}); err != nil {
		return "", fmt.Errorf("获取确认后余额失败: %w", err)
	}

	return balance.TON(), nil
}

func (p *TonPayer) retryTransientTonError(ctx context.Context, fn func() error) error {
	attempts := p.retryAttempts
	if attempts <= 0 {
		attempts = defaultTonRetryAttempts
	}

	delay := p.retryDelay
	if delay <= 0 {
		delay = defaultTonRetryDelay
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err
		if !isRetryableTonNodeError(err) || attempt == attempts {
			return err
		}

		log.Printf("TON 节点短暂不同步，第 %d/%d 次重试: %v", attempt, attempts, err)
		if err := sleepWithContext(ctx, delay); err != nil {
			return lastErr
		}
	}

	return lastErr
}

func isRetryableTonNodeError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	retryableFragments := []string{
		"lite server error, code 651",
		"cannot load block",
		"not in db",
		"out of sync",
		"shard_client_seqno",
	}

	for _, fragment := range retryableFragments {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseWalletVersion(raw string) (wallet.VersionConfig, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "v4r2", "v4":
		return wallet.V4R2, nil
	case "v4r1":
		return wallet.V4R1, nil
	case "v3r2", "v3":
		return wallet.V3R2, nil
	case "v3r1":
		return wallet.V3R1, nil
	case "v5r1final", "v5final", "v5":
		return wallet.ConfigV5R1Final{NetworkGlobalID: wallet.MainnetGlobalID, Workchain: 0}, nil
	case "v5r1beta", "v5beta":
		return wallet.ConfigV5R1Beta{NetworkGlobalID: wallet.MainnetGlobalID, Workchain: 0}, nil
	default:
		return nil, fmt.Errorf("WalletVersion 不支持: %s", raw)
	}
}

func ensureTransactionSucceeded(tx *tlb.Transaction) error {
	if tx == nil {
		return fmt.Errorf("交易结果为空")
	}

	switch desc := tx.Description.(type) {
	case tlb.TransactionDescriptionOrdinary:
		return validateOrdinaryTransaction(desc, tx.OutMsgCount)
	case *tlb.TransactionDescriptionOrdinary:
		return validateOrdinaryTransaction(*desc, tx.OutMsgCount)
	default:
		return nil
	}
}

func validateOrdinaryTransaction(desc tlb.TransactionDescriptionOrdinary, outMsgCount uint16) error {
	if desc.Aborted {
		return fmt.Errorf("链上交易已中止")
	}

	if vm, ok := desc.ComputePhase.Phase.(tlb.ComputePhaseVM); ok {
		if !vm.Success || vm.Details.ExitCode != 0 {
			return fmt.Errorf("链上交易执行失败，exit_code=%d", vm.Details.ExitCode)
		}
	}
	if desc.ActionPhase != nil && (!desc.ActionPhase.Success || desc.ActionPhase.ResultCode != 0) {
		return fmt.Errorf("链上动作执行失败，result_code=%d", desc.ActionPhase.ResultCode)
	}
	if outMsgCount == 0 {
		return fmt.Errorf("链上交易未创建转账消息")
	}
	return nil
}

func decodePayloadCell(encoded string) (*cell.Cell, error) {
	data, err := decodeBase64Flex(encoded)
	if err != nil {
		return nil, err
	}

	root, err := cell.FromBOC(data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("payload 中没有可用 cell")
	}
	return root, nil
}

func decodeBase64Flex(encoded string) ([]byte, error) {
	candidates := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	}

	padded := encoded
	if remainder := len(encoded) % 4; remainder != 0 {
		padded += strings.Repeat("=", 4-remainder)
	}

	for _, decoder := range candidates {
		if data, err := decoder(encoded); err == nil {
			return data, nil
		}
		if padded != encoded {
			if data, err := decoder(padded); err == nil {
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("无法解码 base64 payload")
}
