## 关于本项目

Telegram 自动开通 `Premium / Stars` 源代码，基于 `Golang`。

本项目基于原项目二次开发：
https://github.com/TGlimmer/AutoOpenTelegramPremium

当前版本支持三种用法：

- 传统 `.env` 单次执行模式
- HTTP API 服务模式
- 内置卡密生成 / 兑换网站

## 开始

请仔细查看 `.env` 配置文件说明，如配置错误导致付款以后未发货，自行负责。

**如不会获取配置文件的 `COOKIE` 与 `Hash` 请进交流群自行询问，或动手能力强的自行研究。**

### 安装环境
项目运行基于`Golang`，你需要先安装`Golang`

+ Windows
  > https://go.dev/ 前往Golang官方网站进行下载安装，如不会建议Google。
+ Linux
   - Centos  
  > yum install golang
  - Ubuntu  
  > sudo apt-get install golang

### 安装依赖

```bash
go mod tidy
```

### 关键配置

`.env` 至少需要配置以下字段：

```env
ResHash=fragment_api_hash
ResCookie=fragment_cookie
ResDH=fragment_dh
TonAccount={"address":"0:...","chain":"-239","walletStateInit":"...","publicKey":"..."}
TonDevice={"platform":"windows","appName":"tonkeeper","appVersion":"...","maxProtocolVersion":2,"features":[...]}
WalletMnemonic=word1 word2 ... word24
WalletVersion=V5R1Final
```

`TonAccount` 和 `TonDevice` 是 Fragment 当前 TonConnect v2 支付流程需要的字段。
`WalletVersion` 必须和 Tonkeeper 当前钱包合约一致。新版 Tonkeeper 常见值是 `V5R1Final`，
老钱包可能是 `V4R2`。

获取方式：

1. 在 Chrome 打开 `https://fragment.com/stars/buy`
2. 确认页面右上角钱包已连接
3. 打开开发者工具 Console，分别执行：

```js
JSON.stringify(Aj.globalState.tonConnectUI.wallet.account)
JSON.stringify(Aj.globalState.tonConnectUI.wallet.device)
```

把输出结果原样填入 `.env`。

注意：`.env` 包含 Cookie、钱包助记词等敏感信息，绝对不要提交到 Git。

### 运行OR编译
+ Windows
  > 直接运行  
    go run main.go  
    编译运行  
    go build .
+ Linux
  > 同上，如需在windows下交叉编译Linux 请自行 `golang 交叉编译`

### 编译 Linux 版本

如果你的云服务器是常见 `x86_64 / amd64`，可以直接在当前目录执行：

```powershell
$env:CGO_ENABLED='0'
$env:GOOS='linux'
$env:GOARCH='amd64'
go build -trimpath -ldflags="-s -w" -o dist/AutoOpenTelegramPremium-linux-amd64 .
```

编译完成后，产物路径是：

```text
dist/AutoOpenTelegramPremium-linux-amd64
```


## 实现逻辑

1. 请求 `fragment.com` 获取目标用户与订单
2. 获取 `rawRequest`
3. 直接使用 Fragment 返回的原始 Payload 发起 TON 支付
4. 支付完成后返回交易结果

## 支持的商品

- Telegram Premium
- Telegram Stars

## 传统 `.env` 单次执行

### 开通 Premium

```env
OpenType=premium
OpenUserName=target_username
OpenDuration=3
```

然后执行：

```bash
go run .
```

### 购买 Stars

```env
OpenType=stars
OpenUserName=target_username
OpenStars=500
```

然后执行：

```bash
go run .
```

## HTTP API 服务模式

### 启动服务

```env
ListenAddr=:8080
HookToken=your-secret-token
```

然后执行：

```bash
go run .
```

### 手动调用示例

开通 Premium：

```bash
curl -X POST http://127.0.0.1:8080/api/fulfill ^
  -H "Content-Type: application/json" ^
  -H "X-Hook-Token: your-secret-token" ^
  -d "{\"type\":\"premium\",\"username\":\"target_username\",\"duration\":3,\"order_id\":\"TEST-001\"}"
```

购买 Stars：

```bash
curl -X POST http://127.0.0.1:8080/api/fulfill ^
  -H "Content-Type: application/json" ^
  -H "X-Hook-Token: your-secret-token" ^
  -d "{\"type\":\"stars\",\"username\":\"target_username\",\"stars\":500,\"order_id\":\"TEST-002\"}"
```

安全预检 Stars，不发起 TON 转账：

```bash
curl -X POST http://127.0.0.1:8080/api/fulfill ^
  -H "Content-Type: application/json" ^
  -H "X-Hook-Token: your-secret-token" ^
  -d "{\"type\":\"stars\",\"username\":\"target_username\",\"stars\":500,\"order_id\":\"TEST-DRY-001\",\"dry_run\":true}"
```

`dry_run=true` 会完成 Fragment 搜索、下单、确认链接、读取 `rawRequest`，
但不会调用钱包转账，适合上线前测试 Fragment 参数是否有效。

## 内置卡密兑换站

现在可以直接使用程序自带的小站：

- 管理页生成卡密
- 用户页兑换卡密
- 卡密数据保存在本地 JSON 文件

推荐配置：

```env
ListenAddr=:8080
HookToken=your-hook-token
AdminToken=your-admin-token
CardStorePath=data/gift_cards.json
```

说明：

- `AdminToken` 为空时，后台默认复用 `HookToken`
- `CardStorePath` 默认是 `data/gift_cards.json`
- 页面设置会保存在 `data/site_settings.json`
- 生成后的卡密只可兑换一次

### 打开页面

管理页：

```text
http://127.0.0.1:8080/admin/cards?token=your-admin-token
```

兑换页：

```text
http://127.0.0.1:8080/redeem
```

### 使用方式

1. 打开管理页，选择商品类型
2. 如果是 `Stars`，填写星星数量
3. 如果是 `Premium`，选择月数
4. 填写生成数量，点击生成卡密
5. 把卡密发给用户，用户在兑换页填写卡密和 Telegram 用户名即可
6. 如果要给用户一个外部购买入口，可在后台“页面设置”里填写购买卡密地址，兑换页会自动显示“购买卡密”按钮，并在新标签页打开

兑换成功后，后台卡密列表会记录：

- 是否已使用
- 兑换用户名
- 交易哈希
- 失败原因

## Docker 部署

项目已经内置 Docker 配置，云服务器上推荐直接用容器部署。

### 1. 准备 `.env`

至少确认这些配置有效：

```env
ResHash=
ResCookie=
ResDH=
TonAccount=
TonDevice=
WalletMnemonic=
WalletVersion=V5R1Final

ListenAddr=:8080
HookToken=your-hook-token
AdminToken=your-admin-token
CardStorePath=/app/data/gift_cards.json
```

建议把这些单次执行字段清空，避免误触发单次发货：

```env
OpenType=
OpenUserName=
OpenDuration=
OpenStars=
```

### 2. 启动容器

```bash
docker compose up -d --build
```

### 3. 查看日志

```bash
docker compose logs -f
```

### 4. 访问页面

```text
兑换页：http://你的服务器IP:8080/redeem
管理页：http://你的服务器IP:8080/admin/cards?token=你的AdminToken
```

### 5. 数据持久化

卡密数据会保存在宿主机：

```text
./data/gift_cards.json
./data/site_settings.json
```

`docker-compose.yml` 已经默认挂载：

```text
./data:/app/data
```

所以重建容器不会丢卡密数据。

### 健康检查

```bash
curl http://127.0.0.1:8080/healthz
```

## 技术交流/意见反馈

+ Telegram 交流群 https://t.me/vpsbbq

## 许可证

根据 MIT 许可证分发。打开 [LICENSE.txt](LICENSE.txt) 查看更多内容。


<p align="right">(<a href="#top">返回顶部</a>)</p>
