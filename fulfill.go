package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ProductType string

const (
	ProductPremium ProductType = "premium"
	ProductStars   ProductType = "stars"
)

type FulfillRequest struct {
	ProductType    ProductType `json:"type"`
	Username       string      `json:"username"`
	DurationMonths int         `json:"duration,omitempty"`
	Stars          int         `json:"stars,omitempty"`
	OrderID        string      `json:"order_id,omitempty"`
	ShowSender     bool        `json:"show_sender"`
	DryRun         bool        `json:"dry_run,omitempty"`
	Force          bool        `json:"force,omitempty"`
	Source         string      `json:"source,omitempty"`
}

type FulfillResponse struct {
	OK             bool        `json:"ok"`
	Duplicate      bool        `json:"duplicate,omitempty"`
	ProductType    ProductType `json:"type"`
	Username       string      `json:"username"`
	OrderID        string      `json:"order_id,omitempty"`
	ReqID          string      `json:"req_id"`
	AmountTON      string      `json:"amount_ton"`
	ExpireAt       time.Time   `json:"expire_at"`
	TxHashBase64   string      `json:"tx_hash_base64"`
	TxHashURLSafe  string      `json:"tx_hash_url_safe"`
	ExplorerURL    string      `json:"explorer_url"`
	WalletBalance  string      `json:"wallet_balance"`
	DurationMonths int         `json:"duration,omitempty"`
	Stars          int         `json:"stars,omitempty"`
	DryRun         bool        `json:"dry_run,omitempty"`
}

type orderState struct {
	Response   FulfillResponse
	Err        string
	Processing bool
}

type OrderRegistry struct {
	mu     sync.Mutex
	states map[string]orderState
}

type App struct {
	fragment *FragmentService
	payer    *TonPayer
	orders   *OrderRegistry
	queue    *FulfillQueue
}

func NewApp(cfg Config) (*App, error) {
	fragment, err := NewFragmentService(cfg)
	if err != nil {
		return nil, err
	}

	payer, err := NewTonPayer(cfg)
	if err != nil {
		return nil, err
	}

	app := &App{
		fragment: fragment,
		payer:    payer,
		orders: &OrderRegistry{
			states: make(map[string]orderState),
		},
	}
	app.queue = NewFulfillQueue(cfg.QueueWorkers, cfg.RequestTimeout, app.fulfill)
	return app, nil
}

func ParseProductType(raw string) (ProductType, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ProductPremium):
		return ProductPremium, nil
	case string(ProductStars):
		return ProductStars, nil
	default:
		return "", fmt.Errorf("不支持的商品类型: %q", raw)
	}
}

func (r FulfillRequest) Validate() error {
	if r.ProductType != ProductPremium && r.ProductType != ProductStars {
		return fmt.Errorf("商品类型无效")
	}
	if r.Username == "" {
		return fmt.Errorf("Telegram 用户名不能为空")
	}

	switch r.ProductType {
	case ProductPremium:
		switch r.DurationMonths {
		case 3, 6, 12:
		default:
			return fmt.Errorf("Premium 月数只支持 3、6、12")
		}
	case ProductStars:
		if r.Stars < 50 || r.Stars > 1000000 {
			return fmt.Errorf("Stars 数量必须在 50 到 1000000 之间")
		}
	}

	return nil
}

func (r *OrderRegistry) Begin(orderID string, force bool) (*FulfillResponse, error) {
	if orderID == "" {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, exists := r.states[orderID]
	if exists && !force {
		switch {
		case state.Processing:
			return nil, fmt.Errorf("订单 %s 正在处理中", orderID)
		case state.Response.OK:
			resp := state.Response
			resp.Duplicate = true
			return &resp, nil
		case state.Err != "":
			return nil, fmt.Errorf("订单 %s 已处理失败，如需重试请传 force=1", orderID)
		}
	}

	r.states[orderID] = orderState{Processing: true}
	return nil, nil
}

func (r *OrderRegistry) FinishSuccess(orderID string, resp FulfillResponse) {
	if orderID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[orderID] = orderState{Response: resp}
}

func (r *OrderRegistry) FinishFailure(orderID string, err error) {
	if orderID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[orderID] = orderState{Err: err.Error()}
}

func (r *OrderRegistry) Clear(orderID string) {
	if orderID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, orderID)
}

func (a *App) Fulfill(ctx context.Context, req FulfillRequest) (FulfillResponse, error) {
	if err := req.Validate(); err != nil {
		return FulfillResponse{}, err
	}

	if req.DryRun {
		return a.fulfill(ctx, req)
	}

	_, done, err := a.EnqueueFulfill(req, FulfillTaskMeta{
		OrderID: req.OrderID,
	}, queueCallbacks{})
	if err != nil {
		return FulfillResponse{}, err
	}

	select {
	case result := <-done:
		if result.err != nil {
			return FulfillResponse{}, result.err
		}
		return result.resp, nil
	case <-ctx.Done():
		return FulfillResponse{}, ctx.Err()
	}
}

func (a *App) EnqueueFulfill(req FulfillRequest, meta FulfillTaskMeta, callbacks queueCallbacks) (FulfillTask, <-chan queueResult, error) {
	if err := req.Validate(); err != nil {
		return FulfillTask{}, nil, err
	}

	if req.DryRun {
		resp, err := a.fulfill(context.Background(), req)
		done := make(chan queueResult, 1)
		if err != nil {
			done <- queueResult{err: err}
			close(done)
			return FulfillTask{}, done, err
		}

		now := time.Now().UTC()
		taskID := firstNonEmpty(strings.TrimSpace(meta.ID), strings.TrimSpace(req.OrderID), generateTaskID("task"))
		respCopy := resp
		task := FulfillTask{
			ID:             taskID,
			OrderID:        firstNonEmpty(strings.TrimSpace(meta.OrderID), strings.TrimSpace(req.OrderID)),
			CardCode:       strings.TrimSpace(meta.CardCode),
			ProductType:    req.ProductType,
			Username:       req.Username,
			DurationMonths: req.DurationMonths,
			Stars:          req.Stars,
			Source:         req.Source,
			Status:         TaskSucceeded,
			Response:       &respCopy,
			CreatedAt:      now,
			UpdatedAt:      now,
			StartedAt:      &now,
			FinishedAt:     &now,
		}
		done <- queueResult{resp: resp}
		close(done)
		return task, done, nil
	}

	if existing, err := a.orders.Begin(req.OrderID, req.Force); err != nil {
		return FulfillTask{}, nil, err
	} else if existing != nil {
		done := make(chan queueResult, 1)
		done <- queueResult{resp: *existing}
		close(done)
		return FulfillTask{
			ID:             firstNonEmpty(strings.TrimSpace(meta.ID), strings.TrimSpace(req.OrderID)),
			OrderID:        firstNonEmpty(strings.TrimSpace(meta.OrderID), strings.TrimSpace(req.OrderID)),
			CardCode:       strings.TrimSpace(meta.CardCode),
			ProductType:    req.ProductType,
			Username:       req.Username,
			DurationMonths: req.DurationMonths,
			Stars:          req.Stars,
			Source:         req.Source,
			Status:         TaskSucceeded,
			Response:       existing,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}, done, nil
	}

	wrapped := queueCallbacks{
		onSuccess: func(task FulfillTask) {
			if task.Response != nil {
				a.orders.FinishSuccess(req.OrderID, *task.Response)
			}
			if callbacks.onSuccess != nil {
				callbacks.onSuccess(task)
			}
		},
		onFailure: func(task FulfillTask) {
			a.orders.FinishFailure(req.OrderID, errors.New(task.Error))
			if callbacks.onFailure != nil {
				callbacks.onFailure(task)
			}
		},
	}

	task, done, err := a.queue.Enqueue(req, meta, wrapped)
	if err != nil {
		a.orders.Clear(req.OrderID)
		return FulfillTask{}, nil, err
	}
	return task, done, nil
}

func (a *App) GetTask(taskID string) (FulfillTask, bool) {
	return a.queue.Get(taskID)
}

func (a *App) ListTasks(taskIDs []string) ([]FulfillTask, QueueStats) {
	return a.queue.List(taskIDs)
}

func (a *App) QueueStats() QueueStats {
	return a.queue.Stats()
}

func (a *App) fulfill(ctx context.Context, req FulfillRequest) (FulfillResponse, error) {
	if err := a.fragment.BootstrapSession(ctx, req.ProductType); err != nil {
		return FulfillResponse{}, err
	}

	recipient, err := a.fragment.SearchRecipient(ctx, req)
	if err != nil {
		return FulfillResponse{}, err
	}

	reqID, err := a.fragment.InitOrder(ctx, req, recipient)
	if err != nil {
		return FulfillResponse{}, err
	}

	expireAt, rawPayment, err := a.fragment.ConfirmOrder(ctx, req, reqID, recipient)
	if err != nil {
		return FulfillResponse{}, err
	}

	if rawPayment == nil {
		payment, err := a.fragment.GetRawRequest(ctx, reqID)
		if err != nil {
			return FulfillResponse{}, err
		}
		rawPayment = &payment
	}

	resp := FulfillResponse{
		OK:             true,
		ProductType:    req.ProductType,
		Username:       req.Username,
		OrderID:        req.OrderID,
		ReqID:          reqID,
		AmountTON:      rawPayment.AmountTON,
		ExpireAt:       expireAt,
		DurationMonths: req.DurationMonths,
		Stars:          req.Stars,
		DryRun:         req.DryRun,
	}

	if req.DryRun {
		return resp, nil
	}

	payResult, err := a.payer.Transfer(ctx, *rawPayment)
	if err != nil {
		return FulfillResponse{}, err
	}
	resp.TxHashBase64 = payResult.TxHashBase64
	resp.TxHashURLSafe = payResult.TxHashURLSafe
	resp.ExplorerURL = payResult.ExplorerURL
	resp.WalletBalance = payResult.WalletBalance
	return resp, nil
}

func normalizeUsername(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "@")
	return strings.TrimSpace(value)
}
