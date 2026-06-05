# Go SHKeeper

[English](README.md) | [简体中文](README.zh-CN.md)

Go SHKeeper 是 SHKeeper 应用层的 Go 重写版本。它尽量保留旧版表名和公开 API 形状，同时加入 Vue 管理后台、完整订单查询 API、Go 原生链 worker，以及只面向 MariaDB 的部署和校验工具。

## 致谢与氛围编码

当前 Go 重写工作采用 OpenAI Codex（GPT-5）参与的氛围编码流程完成：维护者定义产品目标、运行约束和验收标准；Codex 负责实现、文档、诊断和验证；维护者负责最终复核、密钥、部署决策和生产运行。

## 功能概览

- Go 主服务，使用 `database/sql` 连接 MariaDB/MySQL。
- Vue/Vite 管理后台位于 `web/admin`，构建产物从 `internal/app/admin_dist` 嵌入 Go 二进制。
- 支持管理员首次设置密码、登录、账号修改和 TOTP 双因素认证。
- 使用 MariaDB 保存钱包、发票、交易、提现、汇率、通知和订单索引。
- 通过 `/api/v1/orders` 和 `/api/v1/orders/{external_id}` 提供完整订单查询。
- 兼容旧版 SHKeeper 常用接口，包括支付请求、提现、钱包通知、状态检查、汇率和 metrics。
- 模块化 Go 链 worker 覆盖 BTC、LTC、DOGE、FIRO、BTC Lightning、TRON、BNB、EVM 网络、Solana、Monero 和 XRP。
- 提供 Go 原生迁移和验证命令，例如 `/app/import-legacy-main-mariadb`、`/app/import-legacy-accounts`、`/app/deploy-check`、`/app/cutover-preflight`、`/app/release-audit`、`/app/runtime-audit` 和 `/app/goal-audit`。
- 不提供 SQLite 运行时兜底。Go 服务只接受 MariaDB/MySQL DSN，并拒绝 SQLite/PostgreSQL URL。

## 从克隆到运行

最快的首次运行方式是使用 Docker Compose 的最小栈。它只启动主服务和 MariaDB，足够打开管理后台、初始化数据库结构、检查健康接口并开始配置。

### 1. 安装依赖

必须安装：

- Git
- Docker 和 Docker Compose

源码开发可选安装：

- Go 1.24 或更新版本
- Node.js 20 或更新版本

### 2. 克隆仓库

```bash
git clone https://github.com/Sky-JD/go-shkeeper.git
cd go-shkeeper
```

### 3. 创建本地 `.env`

真实部署必须替换为强密码。下面只适合本地首次运行。

```bash
cat > .env <<'EOF'
MARIADB_ROOT_PASSWORD=change-root-password
MARIADB_PASSWORD=change-db-password
SECRET_KEY=change-cookie-secret
SHKEEPER_HOST=127.0.0.1
SHKEEPER_PORT=5000
SHKEEPER_CRYPTOS=USDT
EOF
```

PowerShell：

```powershell
@'
MARIADB_ROOT_PASSWORD=change-root-password
MARIADB_PASSWORD=change-db-password
SECRET_KEY=change-cookie-secret
SHKEEPER_HOST=127.0.0.1
SHKEEPER_PORT=5000
SHKEEPER_CRYPTOS=USDT
'@ | Set-Content .env -Encoding UTF8
```

### 4. 构建并启动

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml up --build -d
```

Docker 构建会先编译 Vue 管理后台并嵌入 Go 服务，然后在 `http://127.0.0.1:5000` 启动主服务。

### 5. 验证服务

```bash
curl http://127.0.0.1:5000/healthz
curl http://127.0.0.1:5000/readyz
```

`readyz` 正常返回：

```json
{"status":"ready"}
```

### 6. 设置第一个管理员密码

打开浏览器：

```text
http://127.0.0.1:5000/set-password
```

为默认 `admin` 用户设置首个密码，然后打开：

```text
http://127.0.0.1:5000/wallets
```

也可以用命令行设置：

```bash
curl -i -X POST http://127.0.0.1:5000/set-password \
  -d 'pw1=change-admin-password' \
  -d 'pw2=change-admin-password'
```

### 7. 停止或重置

停止容器但保留 MariaDB 数据卷：

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml down
```

删除容器和本地数据库卷：

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml down -v
```

## 从源码运行

需要直接修改 Go 代码时可使用这种方式。

先启动 MariaDB：

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml up -d mariadb
```

再运行主服务：

```bash
export MARIADB_DATABASE_URL='mariadb://shkeeper:change-db-password@127.0.0.1:3306/shkeeper'
export SECRET_KEY='change-cookie-secret'
export SHKEEPER_CRYPTOS='USDT'
go run ./cmd/shkeeper
```

PowerShell：

```powershell
$env:MARIADB_DATABASE_URL = 'mariadb://shkeeper:change-db-password@127.0.0.1:3306/shkeeper'
$env:SECRET_KEY = 'change-cookie-secret'
$env:SHKEEPER_CRYPTOS = 'USDT'
go run ./cmd/shkeeper
```

## 管理后台开发

生产镜像会构建 `web/admin`，并把产物复制到 `internal/app/admin_dist`。

```bash
cd web/admin
npm install
npm run build
```

前端开发模式：

```bash
cd web/admin
npm install
npm run dev
```

Vite dev server 只用于前端迭代。生产运行仍由 Go 服务提供嵌入后的管理后台。

## 配置项

常用环境变量：

- `MARIADB_DATABASE_URL` 或 `DATABASE_URL`：MariaDB/MySQL DSN，例如 `mariadb://user:pass@host:3306/shkeeper`。
- `SECRET_KEY`：管理后台会话 Cookie 签名密钥。
- `SHKEEPER_LISTEN`：监听地址，默认 `:5000`。
- `SHKEEPER_CRYPTOS`：启用币种列表，例如 `BTC,TRX,USDT`。
- `SCHEDULER_ENABLED`：是否启用后台提现和通知任务。quickstart 默认关闭。
- `DB_MAX_OPEN_CONNS`、`DB_MAX_IDLE_CONNS`、`DB_CONN_MAX_IDLE_SECONDS`、`DB_CONN_MAX_LIFETIME_SECONDS`：MariaDB 连接池参数。

Go 运行时会拒绝 SQLite、SQLite 文件、PostgreSQL 和 `SQLALCHEMY_DATABASE_URI`。请使用 MariaDB，以保持旧版 SHKeeper 表结构和索引契约。

## API 概览

商户接口使用 `X-Shkeeper-Api-Key`。

```http
GET /api/v1/crypto
POST /api/v1/{crypto}/payment_request
GET /api/v1/orders
GET /api/v1/orders/{external_id}
GET /api/v1/invoices
GET /api/v1/invoices/{external_id}
GET /api/v1/transactions
GET /api/v1/{crypto}/status
POST /api/v1/{crypto}/payout
POST /api/v1/walletnotify/{crypto}/{txid}
```

后台页面和后台 API 使用管理员会话或 HTTP Basic Auth：

```http
GET /wallets
GET /settings
PATCH /api/v1/admin/account
GET /api/v1/admin/wallets
GET /api/v1/admin/orders
GET /api/v1/admin/payouts
```

## 链 Worker

`/app/chain-worker` 提供兼容旧 sidecar 的 Go worker。worker 会把生成的账户和提现任务保存到 MariaDB，并暴露以下类型接口：

```http
GET /readyz
POST /{crypto}/status
POST /{crypto}/balance
POST /{crypto}/generate-address
POST /{crypto}/payout
GET /{crypto}/task/{id}
```

完整多 worker 编排可参考 `docker-compose.example.yml`。启用真实支付或提现前，必须配置 fullnode URL、worker BasicAuth、账户加密密码、token 合约和 RPC 凭据。

## 从旧版 SHKeeper 迁移

全新部署只需要空 MariaDB 数据库。已有 SHKeeper 安装可使用：

- `/app/import-legacy-main-mariadb`：从旧 MariaDB 主库导入主服务数据。
- `/app/import-legacy-json`：导入已经离线导出的旧版数据包。
- `/app/import-legacy-accounts`：导入 sidecar 账户，并重写为 Go `v1:` AES-GCM 密文。
- `/app/cutover-preflight`、`/app/deploy-check`、`/app/cutover-audit`、`/app/post-cutover-verify`、`/app/release-audit` 和 `/app/goal-audit`：分阶段验证迁移和切换结果。

生产密钥不要写入仓库。请通过环境文件、Docker secrets 或挂载的密钥文件传入。

## 测试与验证

```bash
go test ./...
```

带 MariaDB 集成测试库：

```bash
export GO_SHKEEPER_TEST_DATABASE_URL='mariadb://user:pass@127.0.0.1:3306/go_shkeeper_test'
go test ./...
```

运行时镜像审计：

```bash
docker build -t go-shkeeper .
docker run --rm go-shkeeper /app/runtime-audit
```

## 项目结构

```text
cmd/                      Go 命令入口
internal/app/             主服务、管理后台路由、API handler、数据库结构
internal/chainworker/     链 worker 适配器和存储
internal/*audit/          切换、发布、运行时和目标审计包
web/admin/                Vue/Vite 管理后台源码
internal/app/admin_dist/  嵌入 Go 服务的后台构建产物
deploy/                   生产部署和验证资产
docker-compose.quickstart.yml  最小首次运行栈
docker-compose.example.yml     完整多 worker 示例栈
```

## 安全注意事项

- 不要提交 `.env`、私钥、助记词、API key 或管理员密码。
- 服务暴露到 localhost 以外之前，必须替换所有示例密钥。
- 生产环境请使用 HTTPS 和反向代理。
- 启用提现前，先做小额真实链路测试。
- 导入、迁移和切换前保留 MariaDB 备份。

## 许可证

见 [LICENSE](LICENSE)。
