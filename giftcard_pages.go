package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

type redeemPageData struct {
	Error    string
	Success  string
	Code     string
	Username string
	Card     *GiftCard
	Response *FulfillResponse
}

type adminPageData struct {
	Error      string
	Success    string
	AdminToken string
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
		switch card.ProductType {
		case ProductPremium:
			return fmt.Sprintf("%d 个月", card.DurationMonths)
		case ProductStars:
			return fmt.Sprintf("%d Stars", card.Stars)
		default:
			return "-"
		}
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

		card, err := s.cards.Reserve(code)
		if err != nil {
			page.Error = err.Error()
			s.renderRedeemPage(w, page)
			return
		}
		page.Card = &card

		req, err := buildFulfillRequestFromGiftCard(card, username)
		if err != nil {
			_ = s.cards.MarkAvailable(card.Code, err.Error())
			page.Error = err.Error()
			s.renderRedeemPage(w, page)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancel()

		resp, err := s.app.Fulfill(ctx, req)
		if err != nil {
			_ = s.cards.MarkAvailable(card.Code, err.Error())
			page.Error = err.Error()
			s.renderRedeemPage(w, page)
			return
		}

		if err := s.cards.MarkRedeemed(card.Code, username, resp); err != nil {
			page.Error = err.Error()
			s.renderRedeemPage(w, page)
			return
		}

		page.Success = "兑换成功，已经提交充值"
		page.Response = &resp
		s.renderRedeemPage(w, page)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "只支持 GET 和 POST", http.StatusMethodNotAllowed)
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
		FormType:   string(ProductStars),
		FormStars:  50,
		FormMonths: 3,
		FormCount:  1,
		Cards:      s.cards.List(),
	})
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := giftCardTemplates.ExecuteTemplate(w, "redeem", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *HTTPServer) renderAdminPage(w http.ResponseWriter, data adminPageData) {
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
    .grid, .result-grid { grid-template-columns: 1fr; }
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
        <p class="lead">输入卡密和 Telegram 用户名，系统会直接调用当前服务完成充值。星星和会员都走同一套后端流程，卡密只能使用一次。</p>
      </div>
    </div>

    <div class="panel">
      {{if .Error}}<div class="alert alert-error">{{.Error}}</div>{{end}}
      {{if .Success}}<div class="alert alert-ok">{{.Success}}</div>{{end}}
      <form method="post" action="/redeem" class="grid-1">
        <div class="grid">
          <div>
            <label for="code">卡密</label>
            <input id="code" name="code" value="{{.Code}}" placeholder="例如 TGX-ABCD-EFGH-JKLM">
          </div>
          <div>
            <label for="username">Telegram 用户名</label>
            <input id="username" name="username" value="{{.Username}}" placeholder="例如 ciyuancat">
          </div>
        </div>
        <div class="actions">
          <button class="btn btn-primary" type="submit">立即兑换</button>
          <span class="hint">用户名不需要带 <code>@</code>，系统会自动处理。</span>
        </div>
      </form>

      {{if .Response}}
      <div class="result-grid">
        <div class="result-item"><b>商品类型</b>{{productLabel .Response.ProductType}}</div>
        <div class="result-item"><b>目标用户</b>@{{.Response.Username}}</div>
        <div class="result-item"><b>订单标识</b>{{.Response.OrderID}}</div>
        <div class="result-item"><b>Fragment 请求 ID</b>{{.Response.ReqID}}</div>
        <div class="result-item"><b>支付金额</b>{{.Response.AmountTON}} TON</div>
        <div class="result-item"><b>链上哈希</b>{{if .Response.TxHashBase64}}{{.Response.TxHashBase64}}{{else}}-{{end}}</div>
      </div>
      {{if .Response.ExplorerURL}}
      <p class="hint" style="margin-top:14px;">区块浏览器：<a href="{{.Response.ExplorerURL}}" target="_blank" rel="noreferrer">{{.Response.ExplorerURL}}</a></p>
      {{end}}
      {{end}}
    </div>
  </div>
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
              <option value="3" {{if eq .FormMonths 3}}selected{{end}}>3 个月</option>
              <option value="6" {{if eq .FormMonths 6}}selected{{end}}>6 个月</option>
              <option value="12" {{if eq .FormMonths 12}}selected{{end}}>12 个月</option>
            </select>
            <div class="hint" style="margin-top:8px;">会员暂时使用预设套餐，后面再对接你要的官网实际套餐映射。</div>
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
