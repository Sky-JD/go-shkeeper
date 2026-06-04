# Go SHKeeper 中文部署与接入指南

`go-shkeeper` 是 SHKeeper 应用层的 Go 版本，运行数据库固定为 MariaDB/MySQL 协议。它保留了 SHKeeper 常用支付、订单、钱包、提现和管理接口，并提供 `shkeeperctl` 运维脚本用于安装、更新、卸载、启停、币种启用和密钥管理。

## 运行要求

- Linux 服务器，推荐 Ubuntu/Debian。
- Docker 与 Docker Compose v2。
- Git、curl。
- MariaDB。当前项目不建议也不支持 PostgreSQL/PGDB 作为运行库；Go 服务会拒绝 `postgres://`、`postgresql://`、`pgdb://`、`sqlite://` 等数据库地址。

## 一键安装

交互式安装推荐使用下面的命令。脚本会拉取公开仓库、进入 `/root/go-shkeeper`，然后调用 `deploy/shkeeperctl.sh install`。首次安装会引导输入绑定 IP、端口和启用币种。

```bash
cd /tmp
curl -fsSL https://raw.githubusercontent.com/Sky-JD/go-shkeeper/main/deploy/install.sh -o go-shkeeper-install.sh
bash go-shkeeper-install.sh
```

可提前指定安装目录、监听地址、端口和初始币种：

```bash
cd /tmp
curl -fsSL https://raw.githubusercontent.com/Sky-JD/go-shkeeper/main/deploy/install.sh -o go-shkeeper-install.sh
GO_SHKEEPER_DIR=/root/go-shkeeper \
SHKEEPER_HOST=0.0.0.0 \
SHKEEPER_PORT=8080 \
SHKEEPER_INIT_CRYPTOS=BTC,USDT,BNB-USDT \
bash go-shkeeper-install.sh
```

如果已经克隆仓库，也可以直接执行：

```bash
cd /root/go-shkeeper
bash deploy/shkeeperctl.sh install
```

`install` 会在权限允许时安装 `/usr/local/bin/shkeeperctl`。之后直接输入 `shkeeperctl` 即可打开管理面板。

## 管理面板

```bash
shkeeperctl
```

面板会显示：

- SHKeeper 状态：绿色表示运行中，黄色表示部分运行，红色表示未配置、未安装、已停止或 Docker 不可用。
- 监听配置：例如 `127.0.0.1:5000`。
- 当前启用币种。
- API Key、Cookie `SECRET_KEY`、Backend Key、MariaDB 地址是否已设置。

常用命令：

```bash
shkeeperctl status
shkeeperctl logs
shkeeperctl configure
shkeeperctl upgrade
shkeeperctl restart
shkeeperctl stop
CONFIRM_UNINSTALL=GO_SHKEEPER shkeeperctl uninstall
```

保留脚本原始调用方式也可用：

```bash
bash deploy/shkeeperctl.sh install-manager
bash deploy/shkeeperctl.sh configure
bash deploy/shkeeperctl.sh upgrade
CONFIRM_UNINSTALL=GO_SHKEEPER bash deploy/shkeeperctl.sh uninstall
```

删除容器并同时删除数据卷需要二次确认：

```bash
PURGE_DATA=1 CONFIRM_UNINSTALL=GO_SHKEEPER CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA shkeeperctl uninstall
```

## 端口、数据库和密钥

`.env` 是部署配置文件，管理脚本会自动创建和更新它。

关键配置：

```env
SHKEEPER_HOST=127.0.0.1
SHKEEPER_PORT=5000
SHKEEPER_CRYPTOS=BTC,USDT,BNB-USDT
MARIADB_DATABASE_URL=mariadb://user:password@mariadb:3306/shkeeper
SECRET_KEY=change-me
SHKEEPER_BACKEND_KEY=change-me
SUGGESTED_WALLET_APIKEY=change-me
```

直接通过命令设置密钥：

```bash
bash deploy/shkeeperctl.sh set-api-key /secure/api_key
bash deploy/shkeeperctl.sh set-secret-key /secure/secret_key
bash deploy/shkeeperctl.sh set-backend-key /secure/backend_key
```

也可以通过短命令：

```bash
shkeeperctl set-api-key /root/go-shkeeper/secrets/api_key
shkeeperctl set-secret-key /root/go-shkeeper/secrets/secret_key
shkeeperctl set-backend-key /root/go-shkeeper/secrets/backend_key
```

本地输入式设置请进入面板，选择对应菜单。面板会把输入内容写入 `secrets/`，文件权限为 `0600`。

## 管理员账号

管理员账号密码写入 MariaDB，不写入 README 或 Git。

```bash
shkeeperctl admin-password admin /root/go-shkeeper/secrets/admin_password
```

或者进入面板选择“设置管理员账号密码”。

浏览器登录地址：

```text
http://服务器IP:端口/login
```

管理员接口可以使用登录态，也可以使用 HTTP Basic。启用 2FA 后，Basic 管理接口会被拒绝，需要使用登录态。

## 币种启用与禁用

查看可用币种：

```bash
shkeeperctl list-cryptos
```

交互式选择：

```bash
shkeeperctl configure
```

支持输入编号、范围、币种名、网络名或 `all`。例如 `8,12,15,18` 会按菜单编号多选。

直接设置完整币种列表：

```bash
shkeeperctl set-cryptos BTC,USDT,BNB-USDT
```

追加启用：

```bash
shkeeperctl enable-crypto TRX USDT BNB-USDT
```

禁用：

```bash
shkeeperctl disable-crypto BTC-LIGHTNING
```

hk-16-16 模块化 compose 示例：

```bash
SHKEEPER_COMPOSE_FILE=deploy/hk-16-16.modular.example.yml shkeeperctl set-cryptos TRX,USDT,USDC,BNB,BNB-USDT,SOL,XMR,XRP
```

非交互式安装示例：

```bash
SHKEEPER_HOST=0.0.0.0 SHKEEPER_PORT=8080 SHKEEPER_INIT_CRYPTOS=TRX,USDT,BNB-USDT bash deploy/shkeeperctl.sh install
```

## Worker serverkey

Worker 的 BasicAuth 信息保存在 MariaDB 的 `wallet.serverkey` 中，格式由工具写入，避免手动拼错。

```bash
shkeeperctl worker-serverkey BNB,BNB-USDT worker /root/go-shkeeper/secrets/worker_password
```

如果要给当前启用的所有币种写入同一组 worker 账号，可以在面板中选择“写入 worker serverkey”，默认币种就是当前启用列表。

## API 接入

默认服务地址：

```text
http://服务器IP:5000
```

商户 API 使用请求头：

```text
X-Shkeeper-Api-Key: 你的钱包APIKey
```

这个 key 来自 `.env` 的 `SUGGESTED_WALLET_APIKEY`，也可以通过管理接口 `/api/v1/{crypto}/payment-gateway/token` 写入所有钱包。

### 创建支付订单

```bash
curl -X POST http://127.0.0.1:5000/api/v1/USDT/payment_request \
  -H "Content-Type: application/json" \
  -H "X-Shkeeper-Api-Key: $SHKEEPER_API_KEY" \
  -d '{
    "external_id": "order-10001",
    "fiat": "USD",
    "amount": "10.00",
    "callback_url": "https://merchant.example.com/shkeeper/callback"
  }'
```

返回重点字段：

```json
{
  "status": "success",
  "id": 1,
  "amount": "10.000000",
  "wallet": "收款地址或支付请求",
  "exchange_rate": "1",
  "display_name": "TRC20-USDT"
}
```

### 查询单个订单

```bash
curl http://127.0.0.1:5000/api/v1/orders/order-10001 \
  -H "X-Shkeeper-Api-Key: $SHKEEPER_API_KEY"
```

### 查询订单列表

```bash
curl "http://127.0.0.1:5000/api/v1/orders?limit=50&status=UNPAID&crypto=USDT" \
  -H "X-Shkeeper-Api-Key: $SHKEEPER_API_KEY"
```

常用查询参数：

- `limit`：每页数量，默认 50。
- `cursor`：下一页游标。
- `status`：订单状态，例如 `UNPAID`、`PAID`、`PARTIAL`、`OVERPAID`、`CANCELLED`、`REFUNDED`。
- `crypto`：币种，例如 `BTC`、`USDT`、`BNB-USDT`。
- `external_id`：商户订单号。
- `from_date` / `to_date`：时间范围。

### 查询币种状态

```bash
curl http://127.0.0.1:5000/api/v1/USDT/status \
  -H "X-Shkeeper-Api-Key: $SHKEEPER_API_KEY"
```

返回中的 `server` 为链或 worker 状态，常见正常值为 `Synced`。

### 发起提现

提现属于管理操作，需要管理员登录态或 HTTP Basic：

```bash
curl -u admin:"$ADMIN_PASSWORD" \
  -X POST http://127.0.0.1:5000/api/v1/USDT/payout \
  -H "Content-Type: application/json" \
  -d '{
    "external_id": "payout-10001",
    "destination": "目标地址",
    "amount": "1.25",
    "callback_url": "https://merchant.example.com/shkeeper/payout-callback"
  }'
```

查询提现：

```bash
curl "http://127.0.0.1:5000/api/v1/USDT/payout/status?external_id=payout-10001" \
  -H "X-Shkeeper-Api-Key: $SHKEEPER_API_KEY"
```

### 回调说明

支付和提现回调会发送到创建订单或提现时传入的 `callback_url`。回调请求会携带当前钱包 API Key，商户侧应校验来源、订单号、金额、币种和状态后再入账。

## 健康检查

```bash
curl http://127.0.0.1:5000/healthz
curl http://127.0.0.1:5000/readyz
```

- `/healthz`：进程存活。
- `/readyz`：进程存活且 MariaDB 可连接。

## Docker 与本地开发

本地运行：

```bash
go run ./cmd/shkeeper
```

构建镜像：

```bash
docker build -t go-shkeeper .
```

运行容器：

```bash
docker run --rm -p 5000:5000 \
  -e MARIADB_DATABASE_URL="mariadb://user:password@host.docker.internal:3306/shkeeper" \
  go-shkeeper
```

运行测试：

```bash
go test ./...
```

运行部署脚本 dry-run：

```bash
SHKEEPER_DRY_RUN=1 bash deploy/shkeeperctl.sh install
```

## 生产建议

- 对外暴露前建议放在 Nginx、Caddy 或 Cloudflare Tunnel 后面，并启用 HTTPS。
- 不要把 `.env`、`secrets/`、管理员密码、钱包 API Key 提交到 Git。
- 上线前执行 `shkeeperctl status`、`shkeeperctl readiness` 和 `/readyz`。
- 对真实链提现先用小额测试，确认回调、余额、手续费和 worker serverkey 正常。
- MariaDB 需要定期备份，尤其是 `wallet`、`invoice`、`payout`、`order_index`、`transaction` 等表。
