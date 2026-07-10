# Go SHKeeper 优化待办

更新时间：2026-07-10

## P0：支付正确性

- [x] 将确认交易写入、发票余额更新、状态计算和未确认记录清理合并到单个 MariaDB 事务。
- [x] 使用 `SELECT ... FOR UPDATE` 避免同一发票的并发充值覆盖余额。
- [x] 将发票与发票地址创建合并到单个事务，并增加数据库级 SHA-256 请求幂等键。
- [x] 增加并发入账、重复交易和重复支付请求测试。
- [ ] 增加故障注入测试，验证事务中途失败后的完整回滚。

## P0：多实例安全

- [x] 为主服务 Scheduler 增加数据库 leader lease，确保同一时刻只有一个实例执行后台任务。
- [x] 自动出款前检查活动出款并增加最短重试间隔，避免余额尚未更新时重复发起。
- [ ] 为自动出款增加原子 claim，避免滚动部署或多实例重复出款。
- [ ] 为普通回调任务增加 claim、退避、最大重试次数和 DEAD 状态。

## P1：EVM 扫描可靠性与性能

- [x] 使用收款地址 Topic 过滤 `eth_getLogs`，避免拉取无关 Transfer 日志。
- [x] 增加 EVM 读取 RPC fallback 和 BNB 可用默认节点。
- [x] 将每 20 秒扫描改为 cursor 增量扫描，并保留低频活动窗口 reconciliation。
- [x] 分离读取 RPC 与广播 RPC，读取 fallback 不再改变交易广播主节点。
- [ ] 增加扫描耗时、cursor lag、RPC fallback 和失败指标。

## P1：安全加固

- [x] 统一校验支付与提现 callback URL，拒绝 URL userinfo、localhost 以及内网、回环和链路本地 IP 字面量。
- [ ] 在实际拨号阶段校验 DNS 解析结果并禁止危险重定向，完整防御 DNS rebinding SSRF。
- [ ] 为回调增加事件 ID、时间戳和 HMAC 签名。
- [x] 增加 `SHKEEPER_COOKIE_SECURE`，统一覆盖后台、登录和 2FA Cookie。
- [x] backend key 与 metrics 凭据缺失时 fail closed，部署工具生成随机 metrics 密码。
- [ ] 生产模式下缺失 `SECRET_KEY` 时拒绝启动。
- [ ] 管理员初始化改为一次性 bootstrap token 或仅允许受信来源。
- [ ] 为登录、2FA 和初始化接口增加限速。

## P1：数据库迁移与查询

- [x] 修复迁移错误分类，不能把 `Duplicate entry` 当作索引已存在。
- [ ] 增加 schema migration 版本和单实例迁移锁。
- [ ] 去掉活动发票查询中阻止索引利用的 `COALESCE(NULLIF(status))` 条件。
- [ ] 为关键查询补充 `EXPLAIN`/索引验证测试或运维检查命令。

## P2：监控与持续验证

- [ ] 增加支付入账、回调积压、出款积压、DB 连接池和 EVM 扫描指标。
- [ ] 增加账本与发票缓存余额一致性审计命令。
- [x] CI 增加 `go vet` 和支付/worker 关键路径 `go test -race`。
- [ ] CI 增加固定版本的 `govulncheck`。
- [ ] 完成 MariaDB 集成测试、Docker smoke、`git diff --check` 和 GitHub Actions 验证。

## 发布

- [x] 更新本文档中的完成状态和验证证据。
- [ ] 提交到 `codex/payment-reliability-hardening`。
- [ ] 推送分支并创建草稿 PR。

## 本地验证证据

- [x] Go 1.24.13：`go test ./...`
- [x] Go 1.24.13：`go vet ./...`
- [x] `git diff --check`
- [x] YAML 解析：`docker-compose.example.yml`、`docker-compose.quickstart.yml`、`deploy/modular.example.yml`、`.github/workflows/go-shkeeper.yml`
- [x] Git Bash：`bash -n deploy/shkeeperctl.sh`
- [ ] GitHub Actions：MariaDB 集成测试、race、Docker build 与 smoke
