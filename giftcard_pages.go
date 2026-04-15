package main

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"
)

type redeemPageData struct {
	Error      string
	Success    string
	Code       string
	Username   string
	BuyCardURL string
}

type redeemDetailPageData struct {
	TaskID string
}

type adminPageData struct {
	Error      string
	Success    string
	AdminToken string
	BuyCardURL string
	FormType   string
	FormStars  int
	FormMonths int
	FormCount  int
	FormNote   string
	Cards      []GiftCard
	Generated  []GiftCard
}

var giftCardTemplates = template.Must(template.New("giftcard-pages").Funcs(template.FuncMap{
	"eq": func(left any, right any) bool {
		return fmt.Sprint(left) == fmt.Sprint(right)
	},
	"formatTime": func(value time.Time) string {
		if value.IsZero() {
			return "-"
		}
		return value.Local().Format("2006-01-02 15:04:05")
	},
	"formatTimePtr": func(value *time.Time) string {
		if value == nil || value.IsZero() {
			return "-"
		}
		return value.Local().Format("2006-01-02 15:04:05")
	},
	"productLabel": func(productType ProductType) string {
		switch productType {
		case ProductPremium:
			return "会员"
		case ProductStars:
			return "星星"
		default:
			return string(productType)
		}
	},
	"cardValue": func(card GiftCard) string {
		return formatGiftCardValue(card.ProductType, card.Stars, card.DurationMonths)
	},
	"statusLabel": func(status GiftCardStatus) string {
		switch status {
		case GiftCardRedeemed:
			return "已兑换"
		case GiftCardRedeeming:
			return "处理中"
		default:
			return "未使用"
		}
	},
	"statusClass": func(status GiftCardStatus) string {
		switch status {
		case GiftCardRedeemed:
			return "badge badge-ok"
		case GiftCardRedeeming:
			return "badge badge-warn"
		default:
			return "badge"
		}
	},
	"taskStatusLabel": func(status FulfillTaskStatus) string {
		switch status {
		case TaskProcessing:
			return "处理中"
		case TaskSucceeded:
			return "已完成"
		case TaskFailed:
			return "失败"
		default:
			return "排队中"
		}
	},
	"taskStatusClass": func(status FulfillTaskStatus) string {
		switch status {
		case TaskProcessing:
			return "badge badge-warn"
		case TaskSucceeded:
			return "badge badge-ok"
		case TaskFailed:
			return "badge badge-danger"
		default:
			return "badge"
		}
	},
}).Parse(giftCardPageTemplate))

func (s *HTTPServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/redeem", http.StatusFound)
}

func (s *HTTPServer) handleRedeemPage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.renderRedeemPage(w, redeemPageData{})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.renderRedeemPage(w, redeemPageData{Error: "表单解析失败，请重试"})
			return
		}

		code := strings.TrimSpace(r.FormValue("code"))
		username := normalizeUsername(r.FormValue("username"))
		page := redeemPageData{
			Code:     code,
			Username: username,
		}

		if code == "" {
			page.Error = "卡密不能为空"
			s.renderRedeemPage(w, page)
			return
		}
		if username == "" {
			page.Error = "Telegram 用户名不能为空"
			s.renderRedeemPage(w, page)
			return
		}

		task, err := s.submitRedeemTask(code, username)
		if err != nil {
			page.Error = err.Error()
			s.renderRedeemPage(w, page)
			return
		}
		http.Redirect(w, r, "/redeem/detail?id="+task.ID, http.StatusFound)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "只支持 GET 和 POST", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleRedeemDetailPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "只支持 GET", http.StatusMethodNotAllowed)
		return
	}

	taskID := strings.TrimSpace(r.URL.Query().Get("id"))
	if taskID == "" {
		http.Error(w, "缺少任务 ID", http.StatusBadRequest)
		return
	}

	s.renderRedeemDetailPage(w, redeemDetailPageData{TaskID: taskID})
}

func (s *HTTPServer) handleRedeemSubmitAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}

	fields, err := parseRequestFields(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, err := s.submitRedeemTask(
		firstNonEmpty(fields.Get("code"), fields.Get("card_code")),
		firstNonEmpty(fields.Get("username"), fields.Get("telegram_username"), fields.Get("recipient")),
	)
	if err != nil {
		writeError(w, redeemErrorStatus(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"task":       task,
		"detail_url": "/redeem/detail?id=" + task.ID,
	})
}

func (s *HTTPServer) handleRedeemTasksAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}

	rawIDs := strings.TrimSpace(r.URL.Query().Get("ids"))
	if rawIDs == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":    true,
			"tasks": []FulfillTask{},
			"stats": s.app.QueueStats(),
		})
		return
	}

	parts := strings.Split(rawIDs, ",")
	taskIDs := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			taskIDs = append(taskIDs, trimmed)
		}
	}

	tasks, stats := s.loadTaskSnapshots(taskIDs)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"tasks": tasks,
		"stats": stats,
	})
}

func (s *HTTPServer) handleRedeemTaskAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}

	taskID := strings.TrimSpace(r.URL.Query().Get("id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "缺少任务 ID")
		return
	}

	task, ok := s.loadTaskSnapshot(taskID)
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"task":  task,
		"stats": s.app.QueueStats(),
	})
}

func (s *HTTPServer) submitRedeemTask(code string, username string) (FulfillTask, error) {
	code = strings.TrimSpace(code)
	username = normalizeUsername(username)

	if code == "" {
		return FulfillTask{}, fmt.Errorf("卡密不能为空")
	}
	if username == "" {
		return FulfillTask{}, fmt.Errorf("Telegram 用户名不能为空")
	}

	taskID := generateTaskID("redeem")
	card, err := s.cards.Reserve(code, taskID)
	if err != nil {
		return FulfillTask{}, err
	}

	req, err := buildFulfillRequestFromGiftCard(card, username)
	if err != nil {
		_ = s.cards.MarkAvailable(card.Code, err.Error())
		return FulfillTask{}, err
	}

	task, _, err := s.app.EnqueueFulfill(req, FulfillTaskMeta{
		ID:       taskID,
		OrderID:  req.OrderID,
		CardCode: card.Code,
	}, queueCallbacks{
		onSuccess: func(task FulfillTask) {
			if task.Response != nil {
				_ = s.cards.MarkRedeemed(card.Code, username, *task.Response)
			}
		},
		onFailure: func(task FulfillTask) {
			_ = s.cards.MarkAvailable(card.Code, task.Error)
		},
	})
	if err != nil {
		_ = s.cards.MarkAvailable(card.Code, err.Error())
		return FulfillTask{}, err
	}

	return task, nil
}

func (s *HTTPServer) loadTaskSnapshot(taskID string) (FulfillTask, bool) {
	if task, ok := s.app.GetTask(taskID); ok {
		return task, true
	}
	card, ok := s.cards.FindByTaskID(taskID)
	if !ok {
		return FulfillTask{}, false
	}
	return taskFromGiftCard(card), true
}

func (s *HTTPServer) loadTaskSnapshots(taskIDs []string) ([]FulfillTask, QueueStats) {
	queueTasks, stats := s.app.ListTasks(taskIDs)
	found := make(map[string]FulfillTask, len(queueTasks))
	for _, task := range queueTasks {
		found[task.ID] = task
	}

	items := make([]FulfillTask, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if task, ok := found[taskID]; ok {
			items = append(items, task)
			continue
		}
		card, ok := s.cards.FindByTaskID(taskID)
		if !ok {
			continue
		}
		items = append(items, taskFromGiftCard(card))
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, stats
}

func taskFromGiftCard(card GiftCard) FulfillTask {
	taskID := strings.TrimSpace(card.TaskID)
	task := FulfillTask{
		ID:             taskID,
		OrderID:        firstNonEmpty(strings.TrimSpace(card.OrderID), taskID),
		CardCode:       card.Code,
		ProductType:    card.ProductType,
		Username:       firstNonEmpty(card.RedeemedBy, ""),
		DurationMonths: card.DurationMonths,
		Stars:          card.Stars,
		Source:         "giftcard-site",
		CreatedAt:      card.CreatedAt,
		UpdatedAt:      card.UpdatedAt,
	}

	switch card.Status {
	case GiftCardRedeemed:
		task.Status = TaskSucceeded
		finishedAt := card.UpdatedAt
		if card.RedeemedAt != nil {
			finishedAt = *card.RedeemedAt
		}
		task.FinishedAt = &finishedAt
		resp := FulfillResponse{
			OK:             true,
			ProductType:    card.ProductType,
			Username:       card.RedeemedBy,
			OrderID:        card.OrderID,
			ReqID:          card.ReqID,
			AmountTON:      card.AmountTON,
			TxHashBase64:   card.TxHashBase64,
			ExplorerURL:    card.ExplorerURL,
			DurationMonths: card.DurationMonths,
			Stars:          card.Stars,
		}
		task.Response = &resp
	case GiftCardRedeeming:
		task.Status = TaskProcessing
		startedAt := card.UpdatedAt
		task.StartedAt = &startedAt
	default:
		if strings.TrimSpace(card.LastError) == "" {
			task.Status = TaskQueued
			task.Position = 0
			return task
		}
		task.Status = TaskFailed
		task.Error = card.LastError
		finishedAt := card.UpdatedAt
		task.FinishedAt = &finishedAt
	}

	return task
}

func redeemErrorStatus(err error) int {
	message := err.Error()
	switch {
	case strings.Contains(message, "正在处理中"), strings.Contains(message, "已使用"), strings.Contains(message, "队列已满"):
		return http.StatusConflict
	case strings.Contains(message, "不存在"), strings.Contains(message, "不能为空"), strings.Contains(message, "无效"), strings.Contains(message, "支持"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *HTTPServer) handleAdminCardsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "只支持 GET", http.StatusMethodNotAllowed)
		return
	}

	if !s.authorizeAdmin(r) {
		http.Error(w, "AdminToken 校验失败，请在 URL 上带 ?token=... 或使用 Authorization Bearer", http.StatusUnauthorized)
		return
	}

	s.renderAdminPage(w, adminPageData{
		AdminToken: s.adminTokenFromRequest(r),
		BuyCardURL: s.settings.Get().BuyCardURL,
		FormType:   string(ProductStars),
		FormStars:  50,
		FormMonths: 3,
		FormCount:  1,
		Cards:      s.cards.List(),
	})
}

func (s *HTTPServer) handleSaveAdminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	if !s.authorizeAdmin(r) {
		http.Error(w, "AdminToken 校验失败", http.StatusUnauthorized)
		return
	}

	data := adminPageData{
		AdminToken: s.adminTokenFromRequest(r),
		BuyCardURL: strings.TrimSpace(r.FormValue("buy_card_url")),
		FormType:   string(ProductStars),
		FormStars:  50,
		FormMonths: 3,
		FormCount:  1,
		Cards:      s.cards.List(),
	}

	settings, err := s.settings.SetBuyCardURL(data.BuyCardURL)
	if err != nil {
		data.Error = err.Error()
		s.renderAdminPage(w, data)
		return
	}

	data.BuyCardURL = settings.BuyCardURL
	if data.BuyCardURL == "" {
		data.Success = "购买卡密地址已清空，兑换页将隐藏购买卡密按钮"
	} else {
		data.Success = "购买卡密地址已保存，兑换页按钮会在新标签页打开"
	}
	s.renderAdminPage(w, data)
}

func (s *HTTPServer) handleGenerateGiftCards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	if !s.authorizeAdmin(r) {
		http.Error(w, "AdminToken 校验失败", http.StatusUnauthorized)
		return
	}

	formType := strings.TrimSpace(strings.ToLower(r.FormValue("type")))
	formStars := parseOptionalPositiveInt(r.FormValue("stars"), 50)
	formMonths := parseOptionalPositiveInt(r.FormValue("duration"), 3)
	formCount := parseOptionalPositiveInt(r.FormValue("count"), 1)
	formNote := strings.TrimSpace(r.FormValue("note"))

	data := adminPageData{
		AdminToken: s.adminTokenFromRequest(r),
		BuyCardURL: s.settings.Get().BuyCardURL,
		FormType:   formType,
		FormStars:  formStars,
		FormMonths: formMonths,
		FormCount:  formCount,
		FormNote:   formNote,
	}

	productType, err := ParseProductType(formType)
	if err != nil {
		data.Error = err.Error()
		data.Cards = s.cards.List()
		s.renderAdminPage(w, data)
		return
	}

	spec := GiftCardSpec{
		ProductType:    productType,
		Stars:          formStars,
		DurationMonths: formMonths,
		Note:           formNote,
	}

	generated, err := s.cards.Generate(spec, formCount)
	if err != nil {
		data.Error = err.Error()
		data.Cards = s.cards.List()
		s.renderAdminPage(w, data)
		return
	}

	data.Success = fmt.Sprintf("已生成 %d 个卡密", len(generated))
	data.Generated = generated
	data.Cards = s.cards.List()
	s.renderAdminPage(w, data)
}

func (s *HTTPServer) handleDeleteGiftCards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "表单解析失败")
		return
	}
	if !s.authorizeAdmin(r) {
		writeError(w, http.StatusUnauthorized, "AdminToken 校验失败")
		return
	}

	codes := make([]string, 0, len(r.Form["code"]))
	codes = append(codes, r.Form["code"]...)
	if extra := strings.TrimSpace(r.FormValue("codes")); extra != "" {
		for _, item := range strings.Split(extra, "\n") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				codes = append(codes, trimmed)
			}
		}
	}

	deleted, err := s.cards.DeleteCodes(codes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"deleted": deleted,
	})
}

func (s *HTTPServer) authorizeAdmin(r *http.Request) bool {
	expected := s.cfg.EffectiveAdminToken()
	if expected == "" {
		return true
	}
	return subtleCompare(s.adminTokenFromRequest(r), expected)
}

func (s *HTTPServer) adminTokenFromRequest(r *http.Request) string {
	_ = r.ParseForm()
	return firstNonEmpty(
		r.FormValue("token"),
		r.URL.Query().Get("token"),
		r.Header.Get("X-Admin-Token"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
	)
}

func subtleCompare(left string, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return strings.EqualFold(left, right) || left == right
}

func (s *HTTPServer) renderRedeemPage(w http.ResponseWriter, data redeemPageData) {
	if data.BuyCardURL == "" {
		data.BuyCardURL = s.settings.Get().BuyCardURL
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := giftCardTemplates.ExecuteTemplate(w, "redeem", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *HTTPServer) renderRedeemDetailPage(w http.ResponseWriter, data redeemDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := giftCardTemplates.ExecuteTemplate(w, "redeem-detail", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *HTTPServer) renderAdminPage(w http.ResponseWriter, data adminPageData) {
	if data.BuyCardURL == "" {
		data.BuyCardURL = s.settings.Get().BuyCardURL
	}
	if data.FormType == "" {
		data.FormType = string(ProductStars)
	}
	if data.FormStars <= 0 {
		data.FormStars = 50
	}
	if data.FormMonths <= 0 {
		data.FormMonths = 3
	}
	if data.FormCount <= 0 {
		data.FormCount = 1
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := giftCardTemplates.ExecuteTemplate(w, "admin", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

const giftCardPageTemplate = `
{{define "styles"}}
<style>
  :root {
    --bg: #f7f2e8;
    --card: #fffdf9;
    --ink: #1f2a37;
    --muted: #6b7280;
    --line: #e7dcc8;
    --accent: #0f766e;
    --accent-2: #c2410c;
    --ok: #166534;
    --warn: #92400e;
    --shadow: 0 18px 40px rgba(38, 31, 24, 0.08);
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    background:
      radial-gradient(circle at top left, rgba(15,118,110,.10), transparent 35%),
      radial-gradient(circle at right center, rgba(194,65,12,.10), transparent 30%),
      var(--bg);
    color: var(--ink);
  }
  .shell { max-width: 1120px; margin: 0 auto; padding: 32px 20px 48px; }
  .hero { display: flex; justify-content: space-between; gap: 20px; flex-wrap: wrap; margin-bottom: 24px; }
  .hero-card, .panel {
    background: rgba(255, 253, 249, 0.92);
    border: 1px solid var(--line);
    border-radius: 22px;
    box-shadow: var(--shadow);
  }
  .hero-card { padding: 24px; flex: 1 1 420px; }
  .eyebrow {
    display: inline-block;
    padding: 6px 12px;
    border-radius: 999px;
    background: rgba(15,118,110,.10);
    color: var(--accent);
    font-size: 13px;
    font-weight: 700;
    letter-spacing: .04em;
  }
  h1 { margin: 14px 0 10px; font-size: 34px; line-height: 1.1; }
  p.lead { margin: 0; color: var(--muted); line-height: 1.7; }
  .panel { padding: 24px; }
  .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; }
  .grid-1 { display: grid; gap: 18px; }
  label { display: block; font-size: 14px; font-weight: 700; margin-bottom: 8px; }
  input, select, textarea {
    width: 100%;
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px solid #d7cab5;
    background: #fff;
    color: var(--ink);
    font-size: 15px;
  }
  textarea { min-height: 96px; resize: vertical; }
  .actions { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: 14px;
    padding: 12px 18px;
    font-size: 15px;
    font-weight: 700;
    cursor: pointer;
    text-decoration: none;
  }
  .btn-primary { background: var(--accent); color: #fff; }
  .btn-secondary { background: rgba(194,65,12,.12); color: var(--accent-2); }
  .hint { color: var(--muted); font-size: 13px; line-height: 1.6; }
  .alert {
    border-radius: 16px;
    padding: 14px 16px;
    margin-bottom: 18px;
    font-size: 14px;
    line-height: 1.6;
  }
  .alert-error { background: rgba(220, 38, 38, .08); color: #991b1b; }
  .alert-ok { background: rgba(22, 101, 52, .08); color: var(--ok); }
  .result-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-top: 16px; }
  .result-item {
    background: rgba(15,118,110,.06);
    border-radius: 16px;
    padding: 14px;
  }
  .result-item b { display: block; font-size: 13px; margin-bottom: 6px; color: var(--muted); }
  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 14px; }
  th, td { padding: 12px 10px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: top; }
  th { color: var(--muted); font-size: 13px; text-transform: uppercase; letter-spacing: .04em; }
  .code {
    font-family: Consolas, Monaco, monospace;
    padding: 4px 8px;
    background: #f2ede2;
    border-radius: 10px;
    display: inline-block;
    max-width: 100%;
    white-space: normal;
    overflow-wrap: anywhere;
    word-break: break-word;
    vertical-align: top;
  }
  .badge {
    display: inline-block;
    padding: 4px 10px;
    border-radius: 999px;
    background: #efe8da;
    color: #6b5a44;
    font-size: 12px;
    font-weight: 700;
  }
  .badge-ok { background: rgba(22,101,52,.12); color: var(--ok); }
  .badge-warn { background: rgba(146,64,14,.14); color: var(--warn); }
  .badge-danger { background: rgba(220,38,38,.12); color: #991b1b; }
  .redeem-layout {
    display: grid;
    grid-template-columns: minmax(0, 1.08fr) minmax(320px, 0.92fr);
    gap: 20px;
  }
  .stat-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    margin-bottom: 16px;
  }
  .stat-card, .queue-item, .detail-card {
    border: 1px solid var(--line);
    border-radius: 18px;
    background: rgba(255,255,255,.66);
  }
  .stat-card, .detail-card {
    padding: 14px 16px;
    min-width: 0;
  }
  .stat-card b, .detail-card b {
    display: block;
    color: var(--muted);
    font-size: 12px;
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: .04em;
  }
  .detail-card span,
  .detail-card a {
    display: block;
    min-width: 0;
    max-width: 100%;
    overflow-wrap: anywhere;
    word-break: break-word;
    white-space: normal;
  }
  .queue-list {
    display: grid;
    gap: 12px;
  }
  .queue-item {
    display: block;
    padding: 16px;
    color: inherit;
    text-decoration: none;
    transition: transform .15s ease, border-color .15s ease, box-shadow .15s ease;
  }
  .queue-item:hover {
    transform: translateY(-1px);
    border-color: rgba(15,118,110,.32);
    box-shadow: 0 12px 28px rgba(38, 31, 24, 0.08);
  }
  .queue-item-title {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 10px;
  }
  .queue-item-meta, .queue-item-extra, .muted {
    color: var(--muted);
    font-size: 13px;
    line-height: 1.6;
  }
  .queue-item-extra {
    margin-top: 10px;
    word-break: break-word;
  }
  .panel-title {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 14px;
  }
  .panel-title h2 {
    margin: 0;
    font-size: 21px;
  }
  .detail-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }
  .empty-state {
    padding: 18px;
    border: 1px dashed var(--line);
    border-radius: 18px;
    color: var(--muted);
    font-size: 14px;
    line-height: 1.7;
  }
  .list-codes {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 10px;
    margin-top: 14px;
  }
  .toolbar {
    display: flex;
    gap: 12px;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    margin-bottom: 14px;
  }
  .toolbar-group {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
  }
  .btn-sm {
    padding: 9px 14px;
    font-size: 13px;
    border-radius: 12px;
  }
  .btn-ghost {
    background: #f2ede2;
    color: var(--ink);
  }
  .code-textarea {
    min-height: 128px;
    font-family: Consolas, Monaco, monospace;
    line-height: 1.6;
    white-space: pre;
  }
  .checkbox-cell {
    width: 42px;
  }
  .pager {
    display: flex;
    gap: 10px;
    align-items: center;
    justify-content: flex-end;
    flex-wrap: wrap;
    margin-top: 16px;
  }
  .pager-status {
    color: var(--muted);
    font-size: 13px;
  }
  .link-danger {
    border: 0;
    background: transparent;
    color: #b91c1c;
    cursor: pointer;
    font-size: 13px;
    padding: 0;
  }
  .hidden { display: none; }
  @media (max-width: 840px) {
    h1 { font-size: 28px; }
    .grid, .result-grid, .redeem-layout, .stat-grid, .detail-grid { grid-template-columns: 1fr; }
    .toolbar {
      align-items: stretch;
    }
    .toolbar-group {
      width: 100%;
    }
  }
</style>
{{end}}

{{define "redeem"}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>卡密兑换</title>
  {{template "styles" .}}
</head>
<body>
  <div class="shell">
    <div class="hero">
      <div class="hero-card">
        <span class="eyebrow">AutoOpenTelegramPremium</span>
        <h1>Telegram 卡密兑换</h1>
        <p class="lead">提交后会进入后端串行队列，右侧面板会显示最近 50 条兑换任务，并自动同步状态。用户不需要盲等，可以随时点进详情看当前进度。</p>
      </div>
    </div>

    <div class="redeem-layout">
      <div class="panel">
        <div class="panel-title">
          <h2>立即兑换</h2>
          <span class="muted">默认串行执行，队列并发 1</span>
        </div>
        <div id="redeem-message">
          {{if .Error}}<div class="alert alert-error">{{.Error}}</div>{{end}}
          {{if .Success}}<div class="alert alert-ok">{{.Success}}</div>{{end}}
        </div>
        <form method="post" action="/redeem" class="grid-1" id="redeem-form">
          <div class="grid">
            <div>
              <label for="code">卡密</label>
              <input id="code" name="code" value="{{.Code}}" placeholder="例如 TGX-ABCD-EFGH-JKLM">
            </div>
            <div>
              <label for="username">Telegram 用户名</label>
              <input id="username" name="username" value="{{.Username}}" placeholder="例如 liuyifei">
            </div>
          </div>
          <div class="actions">
            {{if .BuyCardURL}}<a class="btn btn-secondary" href="{{.BuyCardURL}}" target="_blank" rel="noreferrer noopener" style="text-decoration:none;">购买卡密</a>{{end}}
            <button class="btn btn-primary" type="submit">立即兑换</button>
            <a class="btn btn-ghost" href="/" style="text-decoration:none;">刷新页面</a>
          </div>
          <div class="hint">
            用户名不需要带 <code>@</code>，系统会自动处理。若之前失败过，新的重试会重新生成任务，不会再复用旧订单号。
          </div>
        </form>
      </div>

      <div class="panel">
        <div class="panel-title">
          <h2>最近队列</h2>
          <span class="muted">浏览器本地缓存最近 50 条</span>
        </div>
        <div class="stat-grid" id="queue-stats">
          <div class="stat-card"><b>队列并发</b><span>-</span></div>
          <div class="stat-card"><b>等待中</b><span>-</span></div>
          <div class="stat-card"><b>处理中</b><span>-</span></div>
        </div>
        <div class="queue-list" id="queue-list">
          <div class="empty-state">还没有最近任务。提交兑换后，这里会自动显示排队和结果状态。</div>
        </div>
      </div>
    </div>
  </div>

  <script>
    (function () {
      var storageKey = 'aotp_recent_redeem_tasks_v1';
      var maxItems = 50;
      var form = document.getElementById('redeem-form');
      var message = document.getElementById('redeem-message');
      var queueList = document.getElementById('queue-list');
      var queueStats = document.getElementById('queue-stats');

      function readStore() {
        try {
          var raw = window.localStorage.getItem(storageKey);
          var parsed = raw ? JSON.parse(raw) : [];
          return Array.isArray(parsed) ? parsed : [];
        } catch (error) {
          return [];
        }
      }

      function writeStore(items) {
        var cleaned = items.slice(0, maxItems);
        window.localStorage.setItem(storageKey, JSON.stringify(cleaned));
        return cleaned;
      }

      function upsertTask(task) {
        var items = readStore().filter(function (item) {
          return item && item.id !== task.id;
        });
        items.unshift(task);
        return writeStore(items);
      }

      function formatTime(value) {
        if (!value) {
          return '-';
        }
        var date = new Date(value);
        if (Number.isNaN(date.getTime())) {
          return value;
        }
        return date.toLocaleString();
      }

      function statusLabel(status) {
        switch (status) {
          case 'processing': return '处理中';
          case 'succeeded': return '已完成';
          case 'failed': return '失败';
          default: return '排队中';
        }
      }

      function statusClass(status) {
        switch (status) {
          case 'processing': return 'badge badge-warn';
          case 'succeeded': return 'badge badge-ok';
          case 'failed': return 'badge badge-danger';
          default: return 'badge';
        }
      }

      function taskValue(task) {
        if (task.type === 'premium') {
          if (Number(task.duration || 0) === 12) {
            return '1年';
          }
          return String(task.duration || 0) + '个月';
        }
        return String(task.stars || 0) + ' Stars';
      }

      function renderMessage(kind, text, detailURL) {
        if (!message) {
          return;
        }
        if (!text) {
          message.innerHTML = '';
          return;
        }
        var link = detailURL ? ' <a href="' + detailURL + '">查看详情</a>' : '';
        message.innerHTML = '<div class="alert ' + (kind === 'error' ? 'alert-error' : 'alert-ok') + '">' + text + link + '</div>';
      }

      function renderStats(stats) {
        if (!queueStats) {
          return;
        }
        var values = stats || {};
        queueStats.innerHTML = ''
          + '<div class="stat-card"><b>队列并发</b><span>' + (values.workers || 1) + '</span></div>'
          + '<div class="stat-card"><b>等待中</b><span>' + (values.queued || 0) + '</span></div>'
          + '<div class="stat-card"><b>处理中</b><span>' + (values.processing || 0) + '</span></div>';
      }

      function renderQueue(items) {
        if (!queueList) {
          return;
        }
        if (!items.length) {
          queueList.innerHTML = '<div class="empty-state">还没有最近任务。提交兑换后，这里会自动显示排队和结果状态。</div>';
          return;
        }

        queueList.innerHTML = items.map(function (item) {
          var detailURL = '/redeem/detail?id=' + encodeURIComponent(item.id || '');
          var summary = item.error
            ? item.error
            : (item.response && item.response.req_id ? 'Fragment 请求 ID：' + item.response.req_id : '等待后端同步结果');
          var positionText = item.status === 'queued' && item.position ? '队列第 ' + item.position + ' 位' : formatTime(item.updated_at || item.created_at);
          return ''
            + '<a class="queue-item" href="' + detailURL + '">'
            +   '<div class="queue-item-title">'
            +     '<div>'
            +       '<div><span class="code">' + (item.card_code || '-') + '</span></div>'
            +       '<div class="queue-item-meta">@' + (item.username || '-') + ' · ' + taskValue(item) + '</div>'
            +     '</div>'
            +     '<span class="' + statusClass(item.status) + '">' + statusLabel(item.status) + '</span>'
            +   '</div>'
            +   '<div class="queue-item-meta">' + positionText + '</div>'
            +   '<div class="queue-item-extra">' + summary + '</div>'
            + '</a>';
        }).join('');
      }

      function mergeTasks(tasks) {
        var items = readStore();
        var lookup = {};
        (tasks || []).forEach(function (task) {
          lookup[task.id] = task;
        });
        items = items.map(function (item) {
          return lookup[item.id] ? Object.assign({}, item, lookup[item.id]) : item;
        });
        items.sort(function (left, right) {
          return new Date(right.created_at || 0).getTime() - new Date(left.created_at || 0).getTime();
        });
        writeStore(items);
        renderQueue(items);
      }

      function syncTasks() {
        var items = readStore();
        renderQueue(items);
        if (!items.length) {
          renderStats(null);
          return;
        }

        var ids = items.map(function (item) { return item.id; }).filter(Boolean);
        fetch('/api/redeem/tasks?ids=' + encodeURIComponent(ids.join(',')), { credentials: 'same-origin' })
          .then(function (response) { return response.json(); })
          .then(function (payload) {
            if (!payload || payload.ok !== true) {
              return;
            }
            renderStats(payload.stats || null);
            mergeTasks(payload.tasks || []);
          })
          .catch(function () {
          });
      }

      form.addEventListener('submit', function (event) {
        if (!window.fetch || !window.localStorage) {
          return;
        }
        event.preventDefault();

        var submitButton = form.querySelector('button[type="submit"]');
        if (submitButton) {
          submitButton.disabled = true;
          submitButton.textContent = '提交中...';
        }
        renderMessage('', '');

        var body = new URLSearchParams(new FormData(form));
        fetch('/api/redeem/submit', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8'
          },
          body: body.toString(),
          credentials: 'same-origin'
        })
        .then(function (response) {
          return response.json().then(function (payload) {
            if (!response.ok || !payload || payload.ok !== true) {
              throw new Error(payload && payload.error ? payload.error : '提交失败');
            }
            return payload;
          });
        })
        .then(function (payload) {
          var task = payload.task || {};
          upsertTask(task);
          renderMessage('ok', '已进入队列，系统会自动同步处理结果。', payload.detail_url || '');
          form.querySelector('#code').value = '';
          syncTasks();
        })
        .catch(function (error) {
          renderMessage('error', error.message || '提交失败');
        })
        .finally(function () {
          if (submitButton) {
            submitButton.disabled = false;
            submitButton.textContent = '立即兑换';
          }
        });
      });

      syncTasks();
      window.setInterval(syncTasks, 5000);
    })();
  </script>
</body>
</html>
{{end}}

{{define "redeem-detail"}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>兑换详情</title>
  {{template "styles" .}}
</head>
<body>
  <div class="shell">
    <div class="hero">
      <div class="hero-card">
        <span class="eyebrow">Redeem Detail</span>
        <h1>兑换详情</h1>
        <p class="lead">任务 ID：<span class="code">{{.TaskID}}</span>。页面会自动轮询后端状态，直到任务完成或失败。</p>
      </div>
    </div>

    <div class="panel">
      <div class="panel-title">
        <h2>任务状态</h2>
        <a class="btn btn-ghost btn-sm" href="/redeem">返回兑换页</a>
      </div>
      <div id="detail-alert"></div>
      <div class="detail-grid" id="detail-grid">
        <div class="detail-card"><b>当前状态</b><span>-</span></div>
        <div class="detail-card"><b>队列位置</b><span>-</span></div>
        <div class="detail-card"><b>卡密</b><span>-</span></div>
        <div class="detail-card"><b>目标用户</b><span>-</span></div>
        <div class="detail-card"><b>商品类型</b><span>-</span></div>
        <div class="detail-card"><b>面值</b><span>-</span></div>
        <div class="detail-card"><b>创建时间</b><span>-</span></div>
        <div class="detail-card"><b>开始时间</b><span>-</span></div>
        <div class="detail-card"><b>完成时间</b><span>-</span></div>
        <div class="detail-card"><b>订单号</b><span>-</span></div>
        <div class="detail-card"><b>Fragment 请求 ID</b><span>-</span></div>
        <div class="detail-card"><b>支付金额</b><span>-</span></div>
        <div class="detail-card"><b>链上哈希</b><span>-</span></div>
        <div class="detail-card"><b>区块浏览器</b><span>-</span></div>
        <div class="detail-card" style="grid-column: 1 / -1;"><b>错误信息</b><span>-</span></div>
      </div>
    </div>
  </div>

  <script>
    (function () {
      var taskID = '{{.TaskID}}';
      var alertBox = document.getElementById('detail-alert');
      var detailGrid = document.getElementById('detail-grid');

      function formatTime(value) {
        if (!value) {
          return '-';
        }
        var date = new Date(value);
        if (Number.isNaN(date.getTime())) {
          return value;
        }
        return date.toLocaleString();
      }

      function statusLabel(status) {
        switch (status) {
          case 'processing': return '处理中';
          case 'succeeded': return '已完成';
          case 'failed': return '失败';
          default: return '排队中';
        }
      }

      function taskValue(task) {
        if (task.type === 'premium') {
          if (Number(task.duration || 0) === 12) {
            return '1年';
          }
          return String(task.duration || 0) + '个月';
        }
        return String(task.stars || 0) + ' Stars';
      }

      function renderAlert(task) {
        if (!alertBox) {
          return;
        }
        if (!task) {
          alertBox.innerHTML = '';
          return;
        }
        if (task.status === 'failed') {
          alertBox.innerHTML = '<div class="alert alert-error">' + (task.error || '任务失败') + '</div>';
          return;
        }
        if (task.status === 'succeeded') {
          alertBox.innerHTML = '<div class="alert alert-ok">兑换成功，充值已经完成。</div>';
          return;
        }
        alertBox.innerHTML = '<div class="alert alert-ok">任务已提交，正在后台处理，请稍候自动刷新。</div>';
      }

      function renderTask(task) {
        var response = task && task.response ? task.response : {};
        var rows = [
          ['当前状态', statusLabel(task.status || 'queued')],
          ['队列位置', task.status === 'queued' && task.position ? ('第 ' + task.position + ' 位') : '-'],
          ['卡密', task.card_code || '-'],
          ['目标用户', task.username ? ('@' + task.username) : '-'],
          ['商品类型', task.type === 'premium' ? '会员' : '星星'],
          ['面值', taskValue(task || {})],
          ['创建时间', formatTime(task.created_at)],
          ['开始时间', formatTime(task.started_at)],
          ['完成时间', formatTime(task.finished_at)],
          ['订单号', task.order_id || '-'],
          ['Fragment 请求 ID', response.req_id || '-'],
          ['支付金额', response.amount_ton ? (response.amount_ton + ' TON') : '-'],
          ['链上哈希', response.tx_hash_base64 || '-'],
          ['区块浏览器', response.explorer_url ? '<a href="' + response.explorer_url + '" target="_blank" rel="noreferrer">' + response.explorer_url + '</a>' : '-'],
          ['错误信息', task.error || '-']
        ];

        detailGrid.innerHTML = rows.map(function (row, index) {
          var fullWidth = index === rows.length - 1 ? ' style="grid-column: 1 / -1;"' : '';
          return '<div class="detail-card"' + fullWidth + '><b>' + row[0] + '</b><span>' + row[1] + '</span></div>';
        }).join('');
      }

      function syncTask() {
        fetch('/api/redeem/task?id=' + encodeURIComponent(taskID), { credentials: 'same-origin' })
          .then(function (response) {
            return response.json().then(function (payload) {
              if (!response.ok || !payload || payload.ok !== true) {
                throw new Error(payload && payload.error ? payload.error : '读取任务失败');
              }
              return payload.task;
            });
          })
          .then(function (task) {
            renderAlert(task);
            renderTask(task);
            if (task.status !== 'succeeded' && task.status !== 'failed') {
              window.setTimeout(syncTask, 5000);
            }
          })
          .catch(function (error) {
            alertBox.innerHTML = '<div class="alert alert-error">' + (error.message || '读取任务失败') + '</div>';
          });
      }

      syncTask();
    })();
  </script>
</body>
</html>
{{end}}

{{define "admin"}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>卡密管理</title>
  {{template "styles" .}}
</head>
<body>
  <div class="shell">
    <div class="hero">
      <div class="hero-card">
        <span class="eyebrow">Admin</span>
        <h1>卡密生成台</h1>
        <p class="lead">这里生成本地卡密，不依赖 VFaka。生成后的卡密会写入本地 JSON 文件，用户在兑换页输入卡密即可自动走当前充值服务。</p>
      </div>
    </div>

    <div class="panel" style="margin-bottom: 20px;">
      <div class="panel-title">
        <h2>页面设置</h2>
        <span class="muted">兑换页按钮入口</span>
      </div>
      <form method="post" action="/admin/settings/save" class="grid-1" style="margin-bottom:18px;">
        {{if .AdminToken}}<input type="hidden" name="token" value="{{.AdminToken}}">{{end}}
        <div>
          <label for="buy-card-url">购买卡密地址</label>
          <input id="buy-card-url" name="buy_card_url" value="{{.BuyCardURL}}" placeholder="例如 https://shop.example.com/buy">
          <div class="hint" style="margin-top:8px;">兑换页会在“立即兑换”左边显示“购买卡密”按钮，点击后新标签页打开。留空即可隐藏这个按钮。</div>
        </div>
        <div class="actions">
          <button class="btn btn-secondary" type="submit">保存页面设置</button>
        </div>
      </form>
    </div>

    <div class="panel" style="margin-bottom: 20px;">
      {{if .Error}}<div class="alert alert-error">{{.Error}}</div>{{end}}
      {{if .Success}}<div class="alert alert-ok">{{.Success}}</div>{{end}}
      <form method="post" action="/admin/cards/generate" class="grid-1">
        {{if .AdminToken}}<input type="hidden" name="token" value="{{.AdminToken}}">{{end}}
        <div class="grid">
          <div>
            <label for="type">商品类型</label>
            <select id="type" name="type" data-role="product-type">
              <option value="stars" {{if eq .FormType "stars"}}selected{{end}}>星星</option>
              <option value="premium" {{if eq .FormType "premium"}}selected{{end}}>会员</option>
            </select>
          </div>
          <div>
            <label for="count">生成数量</label>
            <input id="count" name="count" type="number" min="1" max="200" value="{{.FormCount}}">
          </div>
        </div>

        <div class="grid" data-role="stars-fields">
          <div style="grid-column: 1 / -1;">
            <label for="stars">星星数量</label>
            <input id="stars" name="stars" type="number" min="50" value="{{.FormStars}}">
          </div>
        </div>

        <div class="grid" data-role="premium-fields">
          <div style="grid-column: 1 / -1;">
            <label for="duration">会员套餐</label>
            <select id="duration" name="duration">
              <option value="12" {{if eq .FormMonths 12}}selected{{end}}>1年</option>
              <option value="6" {{if eq .FormMonths 6}}selected{{end}}>6个月</option>
              <option value="3" {{if eq .FormMonths 3}}selected{{end}}>3个月</option>
            </select>
            <div class="hint" style="margin-top:8px;">会员卡密固定支持 1年、6个月、3个月 三档，生成后可直接用于兑换开通。</div>
          </div>
        </div>

        <div>
          <label for="note">备注</label>
          <textarea id="note" name="note" placeholder="例如：2026-04 活动批次 / 500 星星">{{.FormNote}}</textarea>
        </div>
        <div class="actions">
          <button class="btn btn-primary" type="submit">生成卡密</button>
          <span class="hint">现在会按商品类型自动切换输入项，不再同时显示星星和会员字段。</span>
        </div>
      </form>

      {{if .Generated}}
      <div style="margin-top:18px;">
        <div class="toolbar">
          <label style="margin:0;">本次生成</label>
          <div class="toolbar-group">
            <button type="button" class="btn btn-secondary btn-sm" data-action="copy-generated">复制本次卡密</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="download-generated">导出 txt</button>
          </div>
        </div>
        <textarea readonly class="code-textarea" data-role="generated-codes">{{range .Generated}}{{.Code}}
{{end}}</textarea>
        <div class="list-codes">
          {{range .Generated}}
          <div class="code" data-generated-code="{{.Code}}">{{.Code}}</div>
          {{end}}
        </div>
      </div>
      {{end}}
    </div>

    <div class="panel table-wrap">
        <div class="toolbar">
          <div class="toolbar-group">
            <strong>卡密列表</strong>
            <span class="pager-status">默认每页 50 个</span>
          </div>
          <div class="toolbar-group">
            <button type="button" class="btn btn-ghost btn-sm" data-action="select-page">全选本页</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="clear-selection">清空选择</button>
            <button type="button" class="btn btn-secondary btn-sm" data-action="copy-selected">复制选中</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="download-selected">导出选中</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="delete-selected">删除选中</button>
          </div>
        </div>
      <table>
        <thead>
          <tr>
            <th class="checkbox-cell">选</th>
            <th>卡密</th>
            <th>类型</th>
            <th>面值</th>
            <th>状态</th>
            <th>备注</th>
            <th>生成时间</th>
            <th>兑换信息</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {{if .Cards}}
            {{range .Cards}}
            <tr class="js-card-row" data-code="{{.Code}}">
              <td class="checkbox-cell"><input type="checkbox" class="js-card-checkbox" value="{{.Code}}"></td>
              <td><span class="code">{{.Code}}</span></td>
              <td>{{productLabel .ProductType}}</td>
              <td>{{cardValue .}}</td>
              <td><span class="{{statusClass .Status}}">{{statusLabel .Status}}</span></td>
              <td>{{if .Note}}{{.Note}}{{else}}-{{end}}</td>
              <td>{{formatTime .CreatedAt}}</td>
              <td>
                {{if .RedeemedBy}}用户：@{{.RedeemedBy}}<br>{{end}}
                {{if .RedeemedAt}}时间：{{formatTimePtr .RedeemedAt}}<br>{{end}}
                {{if .TxHashBase64}}哈希：{{.TxHashBase64}}<br>{{end}}
                {{if .LastError}}错误：{{.LastError}}{{else if not .RedeemedBy}}-{{end}}
              </td>
              <td>
                <button type="button" class="link-danger" data-action="delete-one" data-code="{{.Code}}">删除</button>
              </td>
            </tr>
            {{end}}
          {{else}}
            <tr>
              <td colspan="9" class="hint">还没有卡密，先生成一批再给用户使用。</td>
            </tr>
          {{end}}
        </tbody>
      </table>
      <div class="pager">
        <button type="button" class="btn btn-ghost btn-sm" data-action="prev-page">上一页</button>
        <span class="pager-status" data-role="page-status">第 1 / 1 页</span>
        <button type="button" class="btn btn-ghost btn-sm" data-action="next-page">下一页</button>
      </div>
    </div>
  </div>
  <script>
    (function () {
      const typeSelect = document.querySelector('[data-role="product-type"]');
      const starsFields = document.querySelector('[data-role="stars-fields"]');
      const premiumFields = document.querySelector('[data-role="premium-fields"]');
      if (!typeSelect || !starsFields || !premiumFields) {
        return;
      }

      function syncFields() {
        const isPremium = typeSelect.value === 'premium';
        starsFields.classList.toggle('hidden', isPremium);
        premiumFields.classList.toggle('hidden', !isPremium);
      }

      typeSelect.addEventListener('change', syncFields);
      syncFields();
    })();

    (function () {
      const generatedTextarea = document.querySelector('[data-role="generated-codes"]');
      const pageStatus = document.querySelector('[data-role="page-status"]');
      const rows = Array.from(document.querySelectorAll('.js-card-row'));
      const checkboxes = Array.from(document.querySelectorAll('.js-card-checkbox'));
      const pageSize = 50;
      let currentPage = 1;

      function normalizeCodes(raw) {
        return String(raw || '')
          .split(/\r?\n/)
          .map(item => item.trim())
          .filter(Boolean);
      }

      function copyText(text, successMessage, emptyMessage) {
        if (!text.trim()) {
          alert(emptyMessage);
          return;
        }
        navigator.clipboard.writeText(text).then(function () {
          alert(successMessage);
        }).catch(function () {
          alert('复制失败，请手动复制。');
        });
      }

      function downloadText(text, filename) {
        if (!text.trim()) {
          alert('当前没有可导出的卡密。');
          return;
        }
        const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        const link = document.createElement('a');
        link.href = url;
        link.download = filename;
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
        URL.revokeObjectURL(url);
      }

      function getSelectedCodes() {
        return checkboxes.filter(item => item.checked).map(item => item.value);
      }

      function postDelete(codes) {
        const filtered = codes.filter(Boolean);
        if (!filtered.length) {
          alert('请先选择要删除的卡密。');
          return;
        }
        if (!confirm('确认删除选中的卡密吗？删除后无法恢复。')) {
          return;
        }

        const form = new URLSearchParams();
        {{if .AdminToken}}form.append('token', '{{.AdminToken}}');{{end}}
        filtered.forEach(function (code) {
          form.append('code', code);
        });

        fetch('/admin/cards/delete', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8'
          },
          body: form.toString()
        })
        .then(function (response) {
          return response.json().then(function (payload) {
            if (!response.ok || payload.ok !== true) {
              throw new Error(payload.error || '删除失败');
            }
            return payload;
          });
        })
        .then(function (payload) {
          alert('已删除 ' + payload.deleted + ' 个卡密。');
          window.location.reload();
        })
        .catch(function (error) {
          alert(error.message || '删除失败');
        });
      }

      function updatePager() {
        if (!rows.length) {
          if (pageStatus) {
            pageStatus.textContent = '第 0 / 0 页';
          }
          return;
        }

        const totalPages = Math.max(1, Math.ceil(rows.length / pageSize));
        if (currentPage > totalPages) {
          currentPage = totalPages;
        }

        const start = (currentPage - 1) * pageSize;
        const end = start + pageSize;
        rows.forEach(function (row, index) {
          row.classList.toggle('hidden', index < start || index >= end);
        });

        if (pageStatus) {
          pageStatus.textContent = '第 ' + currentPage + ' / ' + totalPages + ' 页';
        }
      }

      function selectCurrentPage() {
        const start = (currentPage - 1) * pageSize;
        const end = start + pageSize;
        rows.forEach(function (row, index) {
          const checkbox = row.querySelector('.js-card-checkbox');
          if (!checkbox) {
            return;
          }
          if (index >= start && index < end) {
            checkbox.checked = true;
          }
        });
      }

      function clearSelection() {
        checkboxes.forEach(function (item) {
          item.checked = false;
        });
      }

      document.querySelector('[data-action="copy-generated"]')?.addEventListener('click', function () {
        const text = generatedTextarea ? normalizeCodes(generatedTextarea.value).join('\n') : '';
        copyText(text, '本次生成的卡密已复制。', '当前没有可复制的本次生成卡密。');
      });

      document.querySelector('[data-action="download-generated"]')?.addEventListener('click', function () {
        const text = generatedTextarea ? normalizeCodes(generatedTextarea.value).join('\n') : '';
        downloadText(text, 'generated-gift-cards.txt');
      });

      document.querySelector('[data-action="select-page"]')?.addEventListener('click', selectCurrentPage);
      document.querySelector('[data-action="clear-selection"]')?.addEventListener('click', clearSelection);

      document.querySelector('[data-action="copy-selected"]')?.addEventListener('click', function () {
        const text = getSelectedCodes().join('\n');
        copyText(text, '选中的卡密已复制。', '请先勾选要复制的卡密。');
      });

      document.querySelector('[data-action="download-selected"]')?.addEventListener('click', function () {
        const text = getSelectedCodes().join('\n');
        downloadText(text, 'selected-gift-cards.txt');
      });

      document.querySelector('[data-action="delete-selected"]')?.addEventListener('click', function () {
        postDelete(getSelectedCodes());
      });

      document.querySelectorAll('[data-action="delete-one"]').forEach(function (button) {
        button.addEventListener('click', function () {
          postDelete([button.getAttribute('data-code') || '']);
        });
      });

      document.querySelector('[data-action="prev-page"]')?.addEventListener('click', function () {
        if (currentPage > 1) {
          currentPage -= 1;
          updatePager();
        }
      });

      document.querySelector('[data-action="next-page"]')?.addEventListener('click', function () {
        const totalPages = Math.max(1, Math.ceil(rows.length / pageSize));
        if (currentPage < totalPages) {
          currentPage += 1;
          updatePager();
        }
      });

      updatePager();
    })();
  </script>
</body>
</html>
{{end}}
`
