package main

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

func TestIsRetryableTonNodeError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "651 cannot load block",
			err:  assertError("failed to get account state: lite server error, code 651: cannot load block"),
			want: true,
		},
		{
			name: "out of sync",
			err:  assertError("block is not in db (possibly out of sync: shard_client_seqno=1 ls_seqno=2)"),
			want: true,
		},
		{
			name: "normal error",
			err:  assertError("wallet seed invalid"),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableTonNodeError(tc.err); got != tc.want {
				t.Fatalf("isRetryableTonNodeError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTonPayerTransferIgnoresConfirmedBalanceReadFailure(t *testing.T) {
	t.Parallel()

	preBlock := testBlock(100)
	latestBlock := testBlock(101)
	confirmedBlock := testBlock(102)

	apiCalls := 0
	balanceCalls := 0

	payer := &TonPayer{
		retryAttempts: 2,
		retryDelay:    time.Nanosecond,
		runtimeFactory: func(context.Context) (*tonRuntime, error) {
			return &tonRuntime{
				api: &mockTonAPI{
					currentMasterchainInfoFunc: func(context.Context) (*ton.BlockIDExt, error) {
						apiCalls++
						if apiCalls == 1 {
							return preBlock, nil
						}
						return latestBlock, nil
					},
				},
				wallet: &mockTonWallet{
					getBalanceFunc: func(_ context.Context, block *ton.BlockIDExt) (tlb.Coins, error) {
						balanceCalls++
						switch balanceCalls {
						case 1:
							if block.SeqNo != preBlock.SeqNo {
								t.Fatalf("首次余额查询应使用预检查区块，实际 %d", block.SeqNo)
							}
							return tlb.MustFromTON("10"), nil
						default:
							return tlb.Coins{}, assertError("lite server error, code 651: cannot load block")
						}
					},
					sendManyWaitTransactionFunc: func(context.Context, []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error) {
						return successfulTestTransaction(), confirmedBlock, nil
					},
				},
			}, nil
		},
	}

	resp, err := payer.Transfer(context.Background(), testRawPayment("1"))
	if err != nil {
		t.Fatalf("Transfer() 返回错误: %v", err)
	}
	if resp.TxHashBase64 == "" {
		t.Fatalf("交易成功后应返回 tx hash")
	}
	if resp.WalletBalance != "" {
		t.Fatalf("确认后余额读取失败时不应把整单判失败，也不应填入错误余额: %q", resp.WalletBalance)
	}
}

func TestTonPayerTransferRetriesRetryableSendError(t *testing.T) {
	t.Parallel()

	preBlock := testBlock(200)
	confirmedBlock := testBlock(201)
	sendCalls := 0

	payer := &TonPayer{
		retryAttempts: 2,
		retryDelay:    time.Nanosecond,
		runtimeFactory: func(context.Context) (*tonRuntime, error) {
			return &tonRuntime{
				api: &mockTonAPI{
					currentMasterchainInfoFunc: func(context.Context) (*ton.BlockIDExt, error) {
						return preBlock, nil
					},
				},
				wallet: &mockTonWallet{
					getBalanceFunc: func(_ context.Context, block *ton.BlockIDExt) (tlb.Coins, error) {
						if block.SeqNo != preBlock.SeqNo && block.SeqNo != confirmedBlock.SeqNo {
							t.Fatalf("余额查询区块异常: %d", block.SeqNo)
						}
						return tlb.MustFromTON("10"), nil
					},
					sendManyWaitTransactionFunc: func(context.Context, []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error) {
						sendCalls++
						if sendCalls == 1 {
							return nil, nil, assertError("failed to get account state: lite server error, code 651: cannot load block")
						}
						return successfulTestTransaction(), confirmedBlock, nil
					},
				},
			}, nil
		},
	}

	resp, err := payer.Transfer(context.Background(), testRawPayment("1"))
	if err != nil {
		t.Fatalf("Transfer() 返回错误: %v", err)
	}
	if sendCalls != 2 {
		t.Fatalf("可重试发送错误应自动重试一次，实际调用 %d 次", sendCalls)
	}
	if resp.WalletBalance == "" {
		t.Fatalf("重试成功后应返回确认后余额")
	}
}

type mockTonAPI struct {
	currentMasterchainInfoFunc func(context.Context) (*ton.BlockIDExt, error)
}

func (m *mockTonAPI) CurrentMasterchainInfo(ctx context.Context) (*ton.BlockIDExt, error) {
	return m.currentMasterchainInfoFunc(ctx)
}

type mockTonWallet struct {
	getBalanceFunc              func(context.Context, *ton.BlockIDExt) (tlb.Coins, error)
	sendManyWaitTransactionFunc func(context.Context, []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error)
}

func (m *mockTonWallet) GetBalance(ctx context.Context, block *ton.BlockIDExt) (tlb.Coins, error) {
	return m.getBalanceFunc(ctx, block)
}

func (m *mockTonWallet) SendManyWaitTransaction(ctx context.Context, messages []*wallet.Message) (*tlb.Transaction, *ton.BlockIDExt, error) {
	return m.sendManyWaitTransactionFunc(ctx, messages)
}

func successfulTestTransaction() *tlb.Transaction {
	vm := tlb.ComputePhaseVM{
		Success: true,
	}
	vm.Details.ExitCode = 0

	return &tlb.Transaction{
		Hash:        []byte("test-hash"),
		OutMsgCount: 1,
		Description: tlb.TransactionDescriptionOrdinary{
			ComputePhase: tlb.ComputePhase{
				Phase: vm,
			},
			ActionPhase: &tlb.ActionPhase{
				Success:    true,
				ResultCode: 0,
			},
		},
	}
}

func testRawPayment(amountTON string) RawPayment {
	return RawPayment{
		AmountTON:     amountTON,
		PayloadBase64: base64.StdEncoding.EncodeToString(cell.BeginCell().EndCell().ToBOC()),
	}
}

func testBlock(seqNo uint32) *ton.BlockIDExt {
	return &ton.BlockIDExt{
		Workchain: 0,
		Shard:     1,
		SeqNo:     seqNo,
		RootHash:  []byte{1, 2, 3, byte(seqNo)},
		FileHash:  []byte{4, 5, 6, byte(seqNo)},
	}
}

func assertError(message string) error {
	return &testError{message: message}
}

type testError struct {
	message string
}

func (e *testError) Error() string {
	return e.message
}
