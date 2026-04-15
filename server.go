package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type HTTPServer struct {
	app   *App
	cfg   Config
	cards *GiftCardStore
}

type fieldBag map[string]string

func NewHTTPHandler(app *App, cfg Config) (http.Handler, error) {
	store, err := NewGiftCardStore(cfg.EffectiveCardStorePath())
	if err != nil {
		return nil, err
	}

	server := &HTTPServer{app: app, cfg: cfg, cards: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handleIndex)
	mux.HandleFunc("/redeem", server.handleRedeemPage)
	mux.HandleFunc("/admin/cards", server.handleAdminCardsPage)
	mux.HandleFunc("/admin/cards/generate", server.handleGenerateGiftCards)
	mux.HandleFunc("/admin/cards/delete", server.handleDeleteGiftCards)
	mux.HandleFunc("/healthz", server.handleHealthz)
	mux.HandleFunc("/api/fulfill", server.handleFulfill)
	mux.HandleFunc("/api/vfaka/fulfill", server.handleVFakaFulfill)
	return server.withLogging(mux), nil
}

func (s *HTTPServer) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s source=%s cost=%s", r.Method, r.URL.Path, r.RemoteAddr, time.Since(startedAt))
	})
}

func (s *HTTPServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"time": time.Now().Format(time.RFC3339),
	})
}

func (s *HTTPServer) handleFulfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}

	fields, err := parseRequestFields(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !s.authorize(r, fields) {
		writeError(w, http.StatusUnauthorized, "HookToken 校验失败")
		return
	}

	req, err := buildManualRequest(fields, r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.executeFulfill(w, r, req)
}

func (s *HTTPServer) handleVFakaFulfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}

	fields, err := parseRequestFields(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !s.authorize(r, fields) {
		writeError(w, http.StatusUnauthorized, "HookToken 校验失败")
		return
	}

	req, err := buildVFakaRequest(fields, r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.executeFulfill(w, r, req)
}

func (s *HTTPServer) executeFulfill(w http.ResponseWriter, r *http.Request, req FulfillRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	resp, err := s.app.Fulfill(ctx, req)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "正在处理中") || strings.Contains(err.Error(), "已处理失败") {
			status = http.StatusConflict
		}
		if strings.Contains(err.Error(), "不能为空") ||
			strings.Contains(err.Error(), "无效") ||
			strings.Contains(err.Error(), "支持") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *HTTPServer) authorize(r *http.Request, fields fieldBag) bool {
	if s.cfg.HookToken == "" {
		return true
	}

	provided := firstNonEmpty(
		fields.Get("token"),
		fields.Get("hook_token"),
		r.URL.Query().Get("token"),
		r.Header.Get("X-Hook-Token"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
	)

	if provided == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.HookToken)) == 1
}

func parseRequestFields(r *http.Request) (fieldBag, error) {
	fields := fieldBag{}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))

	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case len(body) == 0:
		return fields, nil
	case strings.Contains(contentType, "application/json"):
		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %w", err)
		}
		for key, value := range raw {
			fields[key] = stringifyValue(value)
		}
	default:
		if err := r.ParseForm(); err != nil {
			return nil, fmt.Errorf("表单解析失败: %w", err)
		}
		for key, values := range r.PostForm {
			if len(values) > 0 {
				fields[key] = values[0]
			}
		}
	}

	return fields, nil
}

func buildManualRequest(fields fieldBag, query url.Values) (FulfillRequest, error) {
	productType, err := ParseProductType(firstNonEmpty(
		fields.Get("type"),
		fields.Get("product_type"),
		fields.Get("mode"),
		query.Get("type"),
	))
	if err != nil {
		return FulfillRequest{}, err
	}

	req := FulfillRequest{
		ProductType: productType,
		Username: normalizeUsername(firstNonEmpty(
			fields.Get("username"),
			fields.Get("recipient"),
			fields.Get("telegram_username"),
			fields.Get("tg_username"),
		)),
		OrderID:    firstNonEmpty(fields.Get("order_id"), fields.Get("order_no")),
		ShowSender: parseBoolDefault(firstNonEmpty(fields.Get("show_sender"), query.Get("show_sender")), true),
		DryRun:     parseBoolDefault(firstNonEmpty(fields.Get("dry_run"), query.Get("dry_run")), false),
		Force:      parseBoolDefault(firstNonEmpty(fields.Get("force"), query.Get("force")), false),
		Source:     "manual-api",
	}

	switch productType {
	case ProductPremium:
		req.DurationMonths, err = parsePositiveInt(firstNonEmpty(
			fields.Get("duration"),
			fields.Get("months"),
			query.Get("duration"),
			query.Get("months"),
		), "duration")
	case ProductStars:
		req.Stars, err = parsePositiveInt(firstNonEmpty(
			fields.Get("stars"),
			fields.Get("quantity"),
			query.Get("stars"),
			query.Get("quantity"),
		), "stars")
	}
	if err != nil {
		return FulfillRequest{}, err
	}

	if err := req.Validate(); err != nil {
		return FulfillRequest{}, err
	}
	return req, nil
}

func buildVFakaRequest(fields fieldBag, query url.Values) (FulfillRequest, error) {
	productType, err := ParseProductType(firstNonEmpty(query.Get("type"), fields.Get("type"), fields.Get("product_type")))
	if err != nil {
		return FulfillRequest{}, err
	}

	usernameFrom := strings.TrimSpace(query.Get("username_from"))
	username := extractUsername(fields, usernameFrom)
	if username == "" {
		return FulfillRequest{}, fmt.Errorf("未能从 VFaka 请求中提取 Telegram 用户名，请检查 username_from 配置")
	}

	orderQuantity := parseOptionalPositiveInt(fields.Get("quantity"), 1)

	req := FulfillRequest{
		ProductType: productType,
		Username:    username,
		OrderID:     firstNonEmpty(fields.Get("order_no"), fields.Get("order_id")),
		ShowSender:  parseBoolDefault(firstNonEmpty(query.Get("show_sender"), fields.Get("show_sender")), true),
		DryRun:      parseBoolDefault(firstNonEmpty(query.Get("dry_run"), fields.Get("dry_run")), false),
		Force:       parseBoolDefault(firstNonEmpty(query.Get("force"), fields.Get("force")), false),
		Source:      "vfaka-webhook",
	}

	switch productType {
	case ProductPremium:
		req.DurationMonths, err = parsePositiveInt(firstNonEmpty(query.Get("duration"), query.Get("months")), "duration")
		if err != nil {
			return FulfillRequest{}, err
		}
		if parseBoolDefault(query.Get("multiply_quantity"), false) {
			req.DurationMonths *= orderQuantity
		}
	case ProductStars:
		fixedStars := firstNonEmpty(query.Get("stars"), fields.Get("stars"))
		if fixedStars != "" {
			req.Stars, err = parsePositiveInt(fixedStars, "stars")
			if err != nil {
				return FulfillRequest{}, err
			}
			if parseBoolDefault(query.Get("multiply_quantity"), true) {
				req.Stars *= orderQuantity
			}
		} else {
			req.Stars, err = parsePositiveInt(firstNonEmpty(fields.Get("quantity"), query.Get("quantity")), "stars")
			if err != nil {
				return FulfillRequest{}, err
			}
		}
	}

	if err := req.Validate(); err != nil {
		return FulfillRequest{}, err
	}
	return req, nil
}

func extractUsername(fields fieldBag, usernameFrom string) string {
	if usernameFrom != "" {
		value := fields.Get(usernameFrom)
		if strings.EqualFold(usernameFrom, "email") && looksLikeEmail(value) {
			return normalizeUsername(strings.SplitN(value, "@", 2)[0])
		}
		return normalizeUsername(value)
	}

	candidates := []string{
		fields.Get("username"),
		fields.Get("recipient"),
		fields.Get("telegram_username"),
		fields.Get("tg_username"),
		fields.Get("query_password"),
	}

	for _, candidate := range candidates {
		if normalized := normalizeUsername(candidate); normalized != "" {
			return normalized
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"ok":    false,
		"error": message,
	})
}

func stringifyValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parsePositiveInt(raw string, fieldName string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("%s 不能为空", fieldName)
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", fieldName)
	}
	return value, nil
}

func parseOptionalPositiveInt(raw string, defaultValue int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func parseBoolDefault(raw string, defaultValue bool) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return defaultValue
	}

	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "@")
	return len(parts) == 2 && strings.Contains(parts[1], ".")
}

func (f fieldBag) Get(key string) string {
	return strings.TrimSpace(f[key])
}
