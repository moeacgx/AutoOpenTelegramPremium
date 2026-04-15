# Stars 与 VFaka Hook 兼容设计

**目标**

在保留现有 `.env` 单次执行开通会员能力的前提下，把项目扩展为同时支持：

- Telegram Premium 发货
- Telegram Stars 发货
- 外部 HTTP Hook 调用
- VFaka 标准 Webhook 兼容

**设计原则**

- 优先复用现有 Fragment + TON 支付链路
- 不要求修改 VFaka 源码
- 保持旧环境变量可继续使用
- 尽量避免把 Premium 和 Stars 分成两套逻辑

**核心方案**

1. 抽象统一的发货请求 `FulfillRequest`
2. 用 `ProductType` 区分 `premium` 和 `stars`
3. Fragment 接口按商品类型切换方法名
4. 支付阶段不再手工拼接备注文本，而是直接使用 Fragment `rawRequest` 返回的原始 payload
5. 增加 HTTP 服务模式，提供：
   - `POST /api/fulfill`
   - `POST /api/vfaka/fulfill`
   - `GET /healthz`

**VFaka 兼容策略**

VFaka 当前商品 Webhook 默认只会发送订单基础字段，例如：

- `order_no`
- `product_id`
- `quantity`
- `total_amount`
- `email`
- `payment_method`
- `status`
- `cards_snapshot`

它默认不会带 Telegram 用户名，也不会带 Premium 月数或 Stars 数量。

因此本项目采用“Webhook Body + URL 查询参数”的组合兼容方案：

- Webhook Body 使用 VFaka 默认 JSON
- 商品类型与发货参数放在 Webhook URL 查询参数中
- 用户名从 VFaka 现有字段中提取，默认优先 `query_password`

推荐示例：

```text
http://127.0.0.1:8080/api/vfaka/fulfill?token=你的密钥&type=stars&stars=500&username_from=query_password
```

对于不改 VFaka 代码但仍希望用户填写 Telegram 用户名的场景，推荐把用户名填写在：

- `query_password` 字段

如果必须复用邮箱输入框，则可使用：

- `username_from=email`

程序会自动截取邮箱 `@` 前缀作为 Telegram 用户名。

**兼容模式**

程序启动时分两种模式：

- 若配置 `ListenAddr`，启动 HTTP 服务
- 否则回退到旧的 `.env` 单次执行模式

旧模式兼容：

- `OpenUserName + OpenDuration` 继续用于 Premium
- 新增 `OpenType=stars + OpenStars` 支持 Stars 单次执行

**错误处理**

- 参数缺失时返回明确 JSON 错误
- `order_no` 存在时做内存级幂等保护
- 同一订单重复成功请求时直接返回上次结果
- 同一订单正在处理中时返回冲突

**测试范围**

- 手动接口参数解析
- VFaka 兼容参数解析
- 用户名提取逻辑
- Stars 数量与 VFaka 订单数量联动逻辑

**已确认的外部依据**

- Fragment Stars 页面存在 `/stars/buy`
- 前端脚本存在：
  - `searchStarsRecipient`
  - `initBuyStarsRequest`
  - `getBuyStarsLink`
- VFaka 默认 `webhook` 后置动作只发送订单基础 JSON，不包含 Telegram 用户名字段
