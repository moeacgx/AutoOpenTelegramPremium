# Stars 与 VFaka Hook 兼容 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为当前 Go 项目补齐 Telegram Stars 发货能力，并提供兼容 VFaka 标准 Webhook 的 HTTP Hook 服务，同时保留旧版 `.env` 单次执行模式。

**Architecture:** 采用统一 `FulfillRequest -> Fragment 下单 -> rawRequest -> TON 支付` 流程，按商品类型切换 Fragment 方法名。HTTP 层只负责参数解析、鉴权和 VFaka 兼容映射，不直接参与业务逻辑。

**Tech Stack:** Go 1.25、标准库 `net/http`、`godotenv`、`tonutils-go`

---

### Task 1: 建立模块与文档基线

**Files:**
- Create: `AutoOpenTelegramPremium/go.mod`
- Create: `AutoOpenTelegramPremium/docs/plans/2026-04-15-stars-vfaka-design.md`
- Create: `AutoOpenTelegramPremium/docs/plans/2026-04-15-stars-vfaka.md`

### Task 2: 重构统一发货核心

**Files:**
- Modify: `AutoOpenTelegramPremium/main.go`
- Create: `AutoOpenTelegramPremium/config.go`
- Create: `AutoOpenTelegramPremium/fulfill.go`
- Create: `AutoOpenTelegramPremium/fragment.go`
- Create: `AutoOpenTelegramPremium/ton.go`

### Task 3: 新增 HTTP Hook 服务

**Files:**
- Create: `AutoOpenTelegramPremium/server.go`
- Modify: `AutoOpenTelegramPremium/main.go`

### Task 4: 实现 VFaka 兼容映射

**Files:**
- Modify: `AutoOpenTelegramPremium/server.go`

### Task 5: 增加测试

**Files:**
- Create: `AutoOpenTelegramPremium/server_test.go`

### Task 6: 更新说明文档

**Files:**
- Modify: `AutoOpenTelegramPremium/README.md`
- Modify: `AutoOpenTelegramPremium/.env`

### Task 7: 依赖整理与验证

**Files:**
- Create: `AutoOpenTelegramPremium/go.sum`
