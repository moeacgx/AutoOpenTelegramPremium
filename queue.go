package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type FulfillTaskStatus string

const (
	TaskQueued     FulfillTaskStatus = "queued"
	TaskProcessing FulfillTaskStatus = "processing"
	TaskSucceeded  FulfillTaskStatus = "succeeded"
	TaskFailed     FulfillTaskStatus = "failed"
)

type FulfillTask struct {
	ID             string            `json:"id"`
	OrderID        string            `json:"order_id,omitempty"`
	CardCode       string            `json:"card_code,omitempty"`
	ProductType    ProductType       `json:"type"`
	Username       string            `json:"username"`
	DurationMonths int               `json:"duration,omitempty"`
	Stars          int               `json:"stars,omitempty"`
	Source         string            `json:"source,omitempty"`
	Status         FulfillTaskStatus `json:"status"`
	Error          string            `json:"error,omitempty"`
	Response       *FulfillResponse  `json:"response,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	Position       int               `json:"position,omitempty"`
}

type FulfillTaskMeta struct {
	ID       string
	OrderID  string
	CardCode string
}

type QueueStats struct {
	Workers    int `json:"workers"`
	Queued     int `json:"queued"`
	Processing int `json:"processing"`
	Tracked    int `json:"tracked"`
}

type queueResult struct {
	resp FulfillResponse
	err  error
}

type queueCallbacks struct {
	onSuccess func(FulfillTask)
	onFailure func(FulfillTask)
}

type queueEntry struct {
	id        string
	req       FulfillRequest
	task      *FulfillTask
	done      chan queueResult
	callbacks queueCallbacks
}

type FulfillQueue struct {
	mu         sync.Mutex
	workerCount int
	timeout    time.Duration
	executor   func(context.Context, FulfillRequest) (FulfillResponse, error)
	tasks      chan *queueEntry
	items      map[string]*FulfillTask
	pending    []string
	processing map[string]struct{}
}

func NewFulfillQueue(workerCount int, timeout time.Duration, executor func(context.Context, FulfillRequest) (FulfillResponse, error)) *FulfillQueue {
	if workerCount <= 0 {
		workerCount = 1
	}

	q := &FulfillQueue{
		workerCount: workerCount,
		timeout:     timeout,
		executor:    executor,
		tasks:       make(chan *queueEntry, 1024),
		items:       make(map[string]*FulfillTask),
		pending:     make([]string, 0, 64),
		processing:  make(map[string]struct{}),
	}

	for i := 0; i < workerCount; i++ {
		go q.runWorker()
	}
	return q
}

func (q *FulfillQueue) Enqueue(req FulfillRequest, meta FulfillTaskMeta, callbacks queueCallbacks) (FulfillTask, <-chan queueResult, error) {
	now := time.Now().UTC()
	taskID := strings.TrimSpace(meta.ID)
	if taskID == "" {
		taskID = generateTaskID("task")
	}

	task := &FulfillTask{
		ID:             taskID,
		OrderID:        firstNonEmpty(strings.TrimSpace(meta.OrderID), strings.TrimSpace(req.OrderID)),
		CardCode:       strings.TrimSpace(meta.CardCode),
		ProductType:    req.ProductType,
		Username:       req.Username,
		DurationMonths: req.DurationMonths,
		Stars:          req.Stars,
		Source:         req.Source,
		Status:         TaskQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	entry := &queueEntry{
		id:        taskID,
		req:       req,
		task:      task,
		done:      make(chan queueResult, 1),
		callbacks: callbacks,
	}

	q.mu.Lock()
	if _, exists := q.items[taskID]; exists {
		q.mu.Unlock()
		return FulfillTask{}, nil, fmt.Errorf("任务 %s 已存在", taskID)
	}
	q.items[taskID] = task
	q.pending = append(q.pending, taskID)
	snapshot := q.snapshotLocked(taskID)
	q.mu.Unlock()

	select {
	case q.tasks <- entry:
		return snapshot, entry.done, nil
	default:
		q.mu.Lock()
		delete(q.items, taskID)
		q.removePendingLocked(taskID)
		q.mu.Unlock()
		return FulfillTask{}, nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
}

func (q *FulfillQueue) Get(taskID string) (FulfillTask, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	task, ok := q.items[strings.TrimSpace(taskID)]
	if !ok {
		return FulfillTask{}, false
	}
	return q.cloneLocked(task), true
}

func (q *FulfillQueue) List(taskIDs []string) ([]FulfillTask, QueueStats) {
	q.mu.Lock()
	defer q.mu.Unlock()

	items := make([]FulfillTask, 0, len(taskIDs))
	for _, rawID := range taskIDs {
		task, ok := q.items[strings.TrimSpace(rawID)]
		if !ok {
			continue
		}
		items = append(items, q.cloneLocked(task))
	}
	return items, q.statsLocked()
}

func (q *FulfillQueue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.statsLocked()
}

func (q *FulfillQueue) runWorker() {
	for entry := range q.tasks {
		q.markProcessing(entry.id)

		ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
		resp, err := q.executor(ctx, entry.req)
		cancel()

		snapshot := q.finish(entry.id, resp, err)
		if err != nil {
			if entry.callbacks.onFailure != nil {
				entry.callbacks.onFailure(snapshot)
			}
			entry.done <- queueResult{err: err}
		} else {
			if entry.callbacks.onSuccess != nil {
				entry.callbacks.onSuccess(snapshot)
			}
			entry.done <- queueResult{resp: resp}
		}
		close(entry.done)
	}
}

func (q *FulfillQueue) markProcessing(taskID string) {
	now := time.Now().UTC()

	q.mu.Lock()
	defer q.mu.Unlock()

	q.removePendingLocked(taskID)
	q.processing[taskID] = struct{}{}

	task, ok := q.items[taskID]
	if !ok {
		return
	}
	task.Status = TaskProcessing
	task.UpdatedAt = now
	task.StartedAt = &now
	task.Position = 0
}

func (q *FulfillQueue) finish(taskID string, resp FulfillResponse, err error) FulfillTask {
	now := time.Now().UTC()

	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.processing, taskID)

	task, ok := q.items[taskID]
	if !ok {
		fallback := FulfillTask{
			ID:         taskID,
			Status:     TaskFailed,
			Error:      "任务不存在",
			CreatedAt:  now,
			UpdatedAt:  now,
			FinishedAt: &now,
		}
		return fallback
	}

	task.UpdatedAt = now
	task.FinishedAt = &now
	task.Position = 0
	if err != nil {
		task.Status = TaskFailed
		task.Error = err.Error()
		task.Response = nil
	} else {
		respCopy := resp
		task.Status = TaskSucceeded
		task.Error = ""
		task.Response = &respCopy
	}

	return q.cloneLocked(task)
}

func (q *FulfillQueue) snapshotLocked(taskID string) FulfillTask {
	task, ok := q.items[taskID]
	if !ok {
		return FulfillTask{}
	}
	return q.cloneLocked(task)
}

func (q *FulfillQueue) cloneLocked(task *FulfillTask) FulfillTask {
	cloned := *task
	cloned.Position = q.positionLocked(task.ID)
	if task.Response != nil {
		respCopy := *task.Response
		cloned.Response = &respCopy
	}
	return cloned
}

func (q *FulfillQueue) positionLocked(taskID string) int {
	for index, pendingID := range q.pending {
		if pendingID == taskID {
			return index + 1
		}
	}
	return 0
}

func (q *FulfillQueue) removePendingLocked(taskID string) {
	filtered := q.pending[:0]
	for _, pendingID := range q.pending {
		if pendingID != taskID {
			filtered = append(filtered, pendingID)
		}
	}
	q.pending = filtered
}

func (q *FulfillQueue) statsLocked() QueueStats {
	return QueueStats{
		Workers:    q.workerCount,
		Queued:     len(q.pending),
		Processing: len(q.processing),
		Tracked:    len(q.items),
	}
}

func generateTaskID(prefix string) string {
	prefix = strings.Trim(strings.ToLower(prefix), "-_ ")
	if prefix == "" {
		prefix = "task"
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().UTC().UnixNano(), hex.EncodeToString(suffix))
}
