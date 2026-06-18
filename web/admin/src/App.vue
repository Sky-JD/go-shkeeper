<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">SK</div>
        <div>
          <strong>SHKeeper</strong>
          <span>管理台</span>
        </div>
      </div>

      <nav class="nav">
        <button
          v-for="item in navItems"
          :key="item.id"
          class="nav-item"
          :class="{ active: state.view === item.id }"
          type="button"
          @click="go(item.id, item.path)"
        >
          <component :is="item.icon" :size="21" />
          <span>{{ item.label }}</span>
        </button>
      </nav>

      <div class="sidebar-footer">
        <div class="user-pill">
          <UserRound :size="16" />
          <span>{{ state.bootstrap.user?.username || "admin" }}</span>
        </div>
        <a class="logout" href="/logout">退出登录</a>
      </div>
    </aside>

    <main class="main">
      <header class="page-head">
        <div>
          <p class="eyebrow">{{ pageEyebrow }}</p>
          <h1>{{ pageTitle }}</h1>
        </div>
        <div class="head-actions">
          <button class="btn" type="button" :disabled="state.loading" @click="refreshCurrent">
            <RefreshCw :size="17" />
            刷新
          </button>
        </div>
      </header>

      <section v-if="state.view === 'wallets'" class="view-stack">
        <div class="summary-grid">
          <div class="summary-card">
            <span>启用币种</span>
            <strong>{{ enabledWallets.length }} / {{ wallets.length }}</strong>
          </div>
          <div class="summary-card">
            <span>在线钱包</span>
            <strong>{{ healthyWallets.length }}</strong>
          </div>
          <div class="summary-card">
            <span>钱包解锁</span>
            <strong>{{ encryptionLabel }}</strong>
          </div>
          <div class="summary-card">
            <span>默认法币</span>
            <strong>{{ state.selectedFiat }}</strong>
          </div>
        </div>

        <section class="panel service-panel">
          <div class="panel-title service-title">
            <div>
              <h2>币种服务</h2>
              <p>已选择 {{ selectedServiceCount }} 个服务，展开后可调整钱包与 Docker 服务</p>
            </div>
            <button class="btn" type="button" @click="state.serviceExpanded = !state.serviceExpanded">
              <ChevronDown :size="17" :class="{ rotated: state.serviceExpanded }" />
              {{ state.serviceExpanded ? "收起" : "展开" }}
            </button>
          </div>
          <div v-if="state.serviceExpanded" class="service-body">
            <div class="service-grid">
              <label
                v-for="crypto in cryptoCatalog"
                :key="crypto.name"
                class="service-card"
                :class="{ active: crypto.selected }"
              >
                <input v-model="crypto.selected" class="switch" type="checkbox" />
                <span class="service-main">
                  <strong>{{ crypto.name }}</strong>
                  <span>{{ crypto.display_name || crypto.network }}</span>
                </span>
                <span class="service-meta">
                  <span class="badge" :class="crypto.configured ? 'ok' : 'muted'">
                    {{ crypto.configured ? "服务已配置" : "未配置服务" }}
                  </span>
                  <span>{{ crypto.service }}</span>
                </span>
              </label>
            </div>
            <div v-if="cryptoApply.command" class="command-box">
              <strong>服务器应用命令</strong>
              <button class="copy-command" type="button" @click="copyCryptoCommand" title="点击复制命令">
                <code>{{ displayCryptoCommand }}</code>
              </button>
              <span v-if="cryptoApply.auto_apply_enabled">
                已启用自动调用；保存后会尝试执行该命令。
              </span>
              <span v-else>
                当前 Web 容器未配置自动调用，需在宿主机执行该命令后 Docker 服务才会增删。
              </span>
              <pre v-if="cryptoApply.apply_output">{{ cryptoApply.apply_output }}</pre>
              <span v-if="cryptoApply.apply_error" class="text-bad">{{ cryptoApply.apply_error }}</span>
            </div>
            <div class="actions service-actions">
              <button class="btn primary" type="button" :disabled="state.loading" @click="saveCryptoServices">
                <Save :size="16" />
                保存服务
              </button>
            </div>
          </div>
        </section>

        <section class="panel import-panel">
            <div class="panel-title">
              <div>
                <h2>旧钱包导入</h2>
                <p>兼容旧版单钱包对象与地址映射 JSON，导入后写入链上账户表</p>
              </div>
            <button class="btn" type="button" @click="state.importExpanded = !state.importExpanded">
              <ChevronDown :size="17" :class="{ rotated: state.importExpanded }" />
              {{ state.importExpanded ? "收起" : "展开" }}
            </button>
          </div>
          <div v-if="state.importExpanded" class="import-body">
            <div class="form-grid import-grid">
              <label class="field">
                <span>链模块</span>
                <select v-model="importForm.module">
                  <option v-for="module in importModules" :key="module" :value="module">{{ module }}</option>
                </select>
              </label>
              <label class="field">
                <span>默认币种</span>
                <select v-model="importForm.default_crypto">
                  <option v-for="crypto in importCryptoOptions" :key="crypto.name" :value="crypto.name">
                    {{ crypto.name }} - {{ crypto.display_name }}
                  </option>
                </select>
              </label>
              <label class="field">
                <span>新账户加密密码</span>
                <input v-model="importForm.account_password" type="password" autocomplete="new-password" placeholder="必须与对应 worker 的 ACCOUNT_PASSWORD 一致" />
              </label>
              <label class="field">
                <span>旧 Fernet 密码</span>
                <input v-model="importForm.legacy_account_password" type="password" autocomplete="new-password" placeholder="旧 secret 已是明文时可留空" />
              </label>
              <label class="field wide">
                <span>旧版 JSON</span>
                <textarea
                  v-model="importForm.json"
                  class="code-textarea"
                  spellcheck="false"
                  placeholder='{"public_address":"公共地址","secret":"密钥"} 或 {"公共地址":{"public_address":"公共地址","secret":"密钥"}}'
                ></textarea>
              </label>
            </div>
            <div class="actions import-actions">
              <button class="btn primary" type="button" :disabled="state.loading" @click="importLegacyWallets">
                <Plus :size="16" />
                导入钱包
              </button>
            </div>
            <div v-if="importReport" class="command-box">
              <strong>导入结果</strong>
              <span>已写入 {{ importReport.rows || 0 }} 个地址</span>
              <code>{{ importReportText }}</code>
            </div>
          </div>
        </section>

        <div class="wallet-layout">
          <section class="panel wallet-list-panel">
            <div class="panel-title">
              <div>
                <h2>币种查看</h2>
                <p>余额、状态、网络与支付能力</p>
              </div>
            </div>
            <div class="wallet-grid">
              <button
                v-for="wallet in visibleWallets"
                :key="wallet.name"
                class="wallet-card"
                :class="{ active: state.selectedCrypto === wallet.name }"
                type="button"
                @click="selectWallet(wallet.name)"
              >
                <div class="wallet-card-head">
                  <div>
                    <strong>{{ wallet.name }}</strong>
                    <span>{{ wallet.display_name || wallet.network || wallet.adapter }}</span>
                  </div>
                  <span class="badge" :class="wallet.enabled ? 'ok' : 'muted'">
                    {{ wallet.enabled ? "启用" : "停用" }}
                  </span>
                </div>
                <div class="wallet-card-metrics">
                  <div>
                    <span>余额</span>
                    <strong>{{ formatAmount(wallet.balance) }}</strong>
                  </div>
                  <div>
                    <span>状态</span>
                    <strong :class="statusTone(wallet)">{{ walletStatusText(wallet) }}</strong>
                  </div>
                </div>
              </button>
              <div v-if="!visibleWallets.length" class="empty compact-empty">暂无启用币种</div>
            </div>
          </section>

          <section class="panel detail-panel">
            <div class="panel-title">
              <div>
                <h2>{{ state.selectedCrypto || "钱包详情" }}</h2>
                <p>{{ selectedWallet?.display_name || selectedWallet?.network || "选择币种查看配置" }}</p>
              </div>
              <span v-if="selectedWallet" class="badge" :class="selectedWallet.enabled ? 'ok' : 'muted'">
                {{ selectedWallet.enabled ? "支付网关已启用" : "支付网关已停用" }}
              </span>
            </div>

            <div v-if="!selectedWallet" class="empty">暂无币种</div>
            <div v-else class="detail-stack">
              <div class="info-grid">
                <div class="info-item">
                  <span>钱包状态</span>
                  <strong :class="statusTone(selectedWallet)">{{ walletStatusText(selectedWallet) }}</strong>
                </div>
                <div class="info-item">
                  <span>余额来源</span>
                  <strong>{{ selectedWallet.balance_source || "wallet" }}</strong>
                </div>
                <div class="info-item">
                  <span>当前余额</span>
                  <strong>{{ formatAmount(selectedWallet.balance) }}</strong>
                </div>
                <div class="info-item">
                  <span>确认数</span>
                  <strong>{{ walletForm.confirmations }}</strong>
                </div>
              </div>

              <div v-if="activationNotice" class="activation-callout" :class="{ err: activationNotice.tone === 'bad' }">
                <div>
                  <strong>{{ activationNotice.title }}</strong>
                  <span>{{ activationNotice.description }}</span>
                </div>
                <div v-if="activationNotice.sample.length" class="activation-addresses">
                  <code v-for="address in activationNotice.sample" :key="address">{{ address }}</code>
                </div>
              </div>

              <div class="split-grid">
                <div class="sub-panel">
                  <div class="sub-title">
                    <h3>支付网关</h3>
                    <button class="btn compact" type="button" @click="generateToken">
                      <KeyRound :size="16" />
                      生成
                    </button>
                  </div>
                  <div class="form-grid">
                    <label class="field toggle-line">
                      <span>启用收款</span>
                      <input v-model="walletForm.enabled" class="switch" type="checkbox" />
                    </label>
                    <label class="field wide">
                      <span>API Key</span>
                      <input v-model="walletForm.api_key" autocomplete="off" />
                    </label>
                    <div class="actions wide">
                      <button class="btn primary" type="button" @click="saveWalletSettings">
                        <Save :size="16" />
                        保存钱包策略
                      </button>
                      <button class="btn" type="button" @click="syncApiKey">
                        <ShieldCheck :size="16" />
                        同步 API Key
                      </button>
                    </div>
                  </div>
                </div>

                <div class="sub-panel">
                  <div class="sub-title">
                    <h3>节点状态</h3>
                    <button class="btn compact" type="button" @click="saveServer">
                      <Server :size="16" />
                      保存
                    </button>
                  </div>
                  <div class="form-grid">
                    <label class="field wide">
                      <span>节点地址</span>
                      <input v-model="serverForm.host" placeholder="http://host:6000" />
                    </label>
                    <label class="field">
                      <span>用户名</span>
                      <input v-model="serverForm.username" autocomplete="off" />
                    </label>
                    <label class="field">
                      <span>密码</span>
                      <input v-model="serverForm.password" type="password" autocomplete="new-password" />
                    </label>
                    <label class="field wide">
                      <span>完整密钥</span>
                      <input v-model="serverForm.key" autocomplete="off" />
                    </label>
                    <div class="actions wide">
                      <button class="btn" type="button" @click="downloadWalletBackup(false)">
                        <Download :size="16" />
                        Backup
                      </button>
                      <button class="btn danger" type="button" @click="downloadWalletBackup(true)">
                        <KeyRound :size="16" />
                        Export keys
                      </button>
                    </div>
                  </div>
                </div>
              </div>

              <div class="sub-panel">
                <div class="sub-title">
                  <h3>自动提现策略</h3>
                  <span class="badge" :class="walletForm.autopayout_enabled ? 'ok' : 'muted'">
                    {{ walletForm.autopayout_enabled ? "已启用" : "已关闭" }}
                  </span>
                </div>
                <div class="form-grid strategy-grid">
                  <label class="field toggle-line">
                    <span>自动提现</span>
                    <input v-model="walletForm.autopayout_enabled" class="switch" type="checkbox" />
                  </label>
                  <label class="field">
                    <span>策略</span>
                    <select v-model="walletForm.autopayout_policy">
                      <option value="manual">手动</option>
                      <option value="scheduled">定时</option>
                      <option value="limit">余额阈值</option>
                    </select>
                  </label>
                  <label class="field">
                    <span>策略值</span>
                    <input v-model="walletForm.autopayout_condition" />
                  </label>
                  <label class="field">
                    <span>提现地址</span>
                    <input v-model="walletForm.autopayout_destination" />
                  </label>
                  <label class="field">
                    <span>手续费</span>
                    <input v-model="walletForm.autopayout_fee" />
                  </label>
                  <label class="field">
                    <span>保留策略</span>
                    <select v-model="walletForm.reserve_policy">
                      <option value="disable">禁用</option>
                      <option value="amount">固定金额</option>
                      <option value="percent">百分比</option>
                    </select>
                  </label>
                  <label class="field">
                    <span>保留值</span>
                    <input v-model="walletForm.reserve_amount" />
                  </label>
                  <label class="field">
                    <span>部分支付百分比</span>
                    <input v-model="walletForm.partial_paid_percent" inputmode="decimal" />
                  </label>
                  <label class="field">
                    <span>超额支付百分比</span>
                    <input v-model="walletForm.overpaid_percent" inputmode="decimal" />
                  </label>
                  <label class="field">
                    <span>重新计算小时</span>
                    <input v-model.number="walletForm.recalculate_after" type="number" min="0" />
                  </label>
                  <label class="field">
                    <span>交易确认数</span>
                    <input v-model.number="walletForm.confirmations" type="number" min="0" />
                  </label>
                </div>
              </div>

              <div class="sub-panel">
                <div class="sub-title">
                  <h3>提现地址簿</h3>
                  <button class="btn compact" type="button" @click="addDestination">
                    <Plus :size="16" />
                    添加
                  </button>
                </div>
                <div class="form-grid">
                  <label class="field">
                    <span>地址</span>
                    <input v-model="destinationForm.addr" />
                  </label>
                  <label class="field">
                    <span>备注</span>
                    <input v-model="destinationForm.comment" />
                  </label>
                </div>
                <div class="destination-list">
                  <div v-for="item in payoutDestinations" :key="item.addr" class="destination-row">
                    <div>
                      <strong class="mono">{{ item.addr }}</strong>
                      <span>{{ item.comment || "无备注" }}</span>
                    </div>
                    <button class="icon-btn danger" type="button" @click="deleteDestination(item.addr)" title="删除">
                      <Trash2 :size="17" />
                    </button>
                  </div>
                  <div v-if="!payoutDestinations.length" class="empty compact-empty">暂无提现地址</div>
                </div>
              </div>
            </div>
          </section>
        </div>
      </section>

      <section v-else-if="state.view === 'rates'" class="view-stack">
        <div class="panel">
          <div class="panel-title">
            <div>
              <h2>汇率自动设置</h2>
              <p>按法币配置来源、手续费策略与手动价格</p>
            </div>
            <div class="segmented">
              <button
                v-for="fiat in fiats"
                :key="fiat"
                class="segment"
                :class="{ active: state.selectedFiat === fiat }"
                type="button"
                @click="selectFiat(fiat)"
              >
                {{ fiat }}
              </button>
            </div>
          </div>

          <div class="bulk-bar">
            <label class="field">
              <span>批量来源</span>
              <select v-model="bulk.source">
                <option value="">不更改</option>
                <option v-for="source in rateSources" :key="source.value" :value="source.value">
                  {{ source.label }}
                </option>
              </select>
            </label>
            <label class="field">
              <span>批量手续费</span>
              <input v-model="bulk.fee" inputmode="decimal" placeholder="%" />
            </label>
            <label class="field">
              <span>批量策略</span>
              <select v-model="bulk.fee_policy">
                <option value="">不更改</option>
                <option v-for="policy in feePolicies" :key="policy.value" :value="policy.value">
                  {{ policy.label }}
                </option>
              </select>
            </label>
            <button class="btn" type="button" @click="applyBulkRates">批量设置</button>
            <button class="btn primary" type="button" @click="saveRates">
              <Save :size="16" />
              保存更改
            </button>
          </div>

          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>币种</th>
                  <th>来源</th>
                  <th>当前价格</th>
                  <th>手动价格</th>
                  <th>手续费策略</th>
                  <th>百分比</th>
                  <th>固定费</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="rate in rates" :key="`${rate.crypto}-${rate.fiat}`">
                  <td data-label="币种"><strong>{{ rate.crypto }}</strong><span class="muted"> / {{ rate.fiat }}</span></td>
                  <td data-label="来源">
                    <select v-model="rate.source">
                      <option v-for="source in rateSources" :key="source.value" :value="source.value">
                        {{ source.label }}
                      </option>
                    </select>
                  </td>
                  <td data-label="当前价格">
                    <span v-if="rate.current_rate_error" class="text-bad">{{ rate.current_rate_error }}</span>
                    <span v-else>{{ formatAmount(rate.current_rate) }}</span>
                  </td>
                  <td data-label="手动价格"><input v-model="rate.rate" inputmode="decimal" /></td>
                  <td data-label="手续费策略">
                    <select v-model="rate.fee_policy">
                      <option v-for="policy in feePolicies" :key="policy.value" :value="policy.value">
                        {{ policy.label }}
                      </option>
                    </select>
                  </td>
                  <td data-label="百分比"><input v-model="rate.fee" inputmode="decimal" /></td>
                  <td data-label="固定费"><input v-model="rate.fixed_fee" inputmode="decimal" /></td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section v-else-if="state.view === 'orders'" class="view-stack">
        <div class="panel">
          <div class="panel-title">
            <div>
              <h2>订单管理</h2>
              <p>支付订单、提现订单与链上交易明细</p>
            </div>
            <button class="btn primary" type="button" @click="loadOrders(false)">
              <Search :size="16" />
              查询
            </button>
          </div>
          <div class="filters">
            <label class="field">
              <span>外部 ID</span>
              <input v-model="orderFilters.external_id" />
            </label>
            <label class="field">
              <span>状态</span>
              <select v-model="orderFilters.status">
                <option value="">全部</option>
                <option v-for="status in orderStatuses" :key="status" :value="status">{{ status }}</option>
              </select>
            </label>
            <label class="field">
              <span>币种</span>
              <select v-model="orderFilters.crypto">
                <option value="">全部</option>
                <option v-for="wallet in wallets" :key="wallet.name" :value="wallet.name">{{ wallet.name }}</option>
              </select>
            </label>
            <label class="field">
              <span>开始日期</span>
              <input v-model="orderFilters.from_date" type="date" />
            </label>
            <label class="field">
              <span>结束日期</span>
              <input v-model="orderFilters.to_date" type="date" />
            </label>
          </div>

          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>外部 ID</th>
                  <th>收款</th>
                  <th>提现</th>
                  <th>状态</th>
                  <th>更新时间</th>
                  <th>详情</th>
                </tr>
              </thead>
              <tbody>
                <template v-for="order in orders" :key="order.external_id">
                  <tr>
                    <td data-label="外部 ID"><span class="mono">{{ order.external_id }}</span></td>
                    <td data-label="收款">{{ order.invoices?.length || 0 }}</td>
                    <td data-label="提现">{{ order.payouts?.length || 0 }}</td>
                    <td data-label="状态"><span class="badge" :class="orderTone(order)">{{ orderSummary(order) }}</span></td>
                    <td data-label="更新时间">{{ orderUpdatedAt(order) }}</td>
                    <td data-label="详情">
                      <button class="btn compact" type="button" @click="toggleOrder(order.external_id)">
                        {{ expandedOrders.has(order.external_id) ? "收起" : "展开" }}
                      </button>
                    </td>
                  </tr>
                  <tr v-if="expandedOrders.has(order.external_id)" class="detail-row">
                    <td colspan="6">
                      <div class="order-detail">
                        <div v-for="invoice in order.invoices || []" :key="`i-${invoice.id}`" class="mini-card">
                          <h3>收款 {{ invoice.crypto }} · {{ invoice.status }}</h3>
                          <p><span>金额</span><strong>{{ invoice.amount_crypto }} {{ invoice.crypto }}</strong></p>
                          <p><span>地址</span><strong class="mono">{{ invoice.addr }}</strong></p>
                          <p><span>交易</span><strong>{{ invoice.txs?.length || 0 }}</strong></p>
                        </div>
                        <div v-for="payout in order.payouts || []" :key="`p-${payout.id}`" class="mini-card">
                          <h3>提现 {{ payout.crypto }} · {{ payout.status }}</h3>
                          <p><span>金额</span><strong>{{ payout.amount }} {{ payout.crypto }}</strong></p>
                          <p><span>地址</span><strong class="mono">{{ payout.destination }}</strong></p>
                          <p><span>TxID</span><strong class="mono">{{ (payout.txids || []).join(", ") || "-" }}</strong></p>
                          <div v-if="(payout.transactions || []).length" class="tx-detail-list">
                            <div v-for="tx in payout.transactions" :key="tx.id || `${tx.kind}-${tx.txid}-${tx.amount}`" class="tx-detail-row">
                              <span class="badge muted">{{ tx.kind || "payout" }}</span>
                              <span class="mono">{{ tx.txid || "-" }}</span>
                              <span>{{ formatAmount(tx.amount) }} {{ tx.crypto || payout.crypto }}</span>
                              <span>{{ tx.status }}</span>
                              <span v-if="tx.error" class="text-bad">{{ tx.error }}</span>
                            </div>
                          </div>
                        </div>
                      </div>
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
          <div class="load-row">
            <button v-if="state.nextOrderCursor" class="btn" type="button" @click="loadOrders(true)">加载更多</button>
            <span v-else class="muted">已显示当前结果</span>
          </div>
        </div>
      </section>

      <section v-else-if="state.view === 'payouts'" class="view-stack">
        <div class="split-grid">
          <div class="panel">
            <div class="panel-title">
              <div>
                <h2>创建提现</h2>
                <p>向链上地址发起单笔提现</p>
              </div>
              <Send :size="22" class="text-blue" />
            </div>
            <div class="form-grid">
              <label class="field">
                <span>币种</span>
                <select v-model="payoutForm.crypto">
                  <option v-if="!enabledWallets.length" value="">暂无启用币种</option>
                  <option v-for="wallet in enabledWallets" :key="wallet.name" :value="wallet.name">{{ wallet.name }}</option>
                </select>
              </label>
              <label class="field">
                <span>金额</span>
                <input v-model="payoutForm.amount" inputmode="decimal" />
              </label>
              <label class="field wide">
                <span>目标地址</span>
                <input v-model="payoutForm.destination" />
              </label>
              <label class="field">
                <span>预计手续费</span>
                <input :value="payoutQuoteFee ? `${formatAmount(payoutQuoteFee)} ${payoutQuoteFeeAsset}` : ''" readonly />
              </label>
              <label class="field">
                <span>外部 ID</span>
                <input v-model="payoutForm.external_id" />
              </label>
              <label class="field wide">
                <span>回调地址</span>
                <input v-model="payoutForm.callback_url" />
              </label>
              <div class="quote-grid wide">
                <div class="quote-card">
                  <span>当前余额</span>
                  <strong>{{ formatAmount(payoutQuoteBalance) }}</strong>
                </div>
                <div class="quote-card">
                  <span>单地址可提</span>
                  <strong :class="{ 'text-bad': payoutAmountExceedsSingleAccount }">{{ formatAmount(payoutQuoteMaxSingle) }}</strong>
                </div>
                <div class="quote-card">
                  <span>预计手续费</span>
                  <strong>{{ formatAmount(payoutQuoteFee) }} <small>{{ payoutQuoteFeeAsset }}</small></strong>
                </div>
              </div>
              <p v-if="payoutQuoteLoading" class="hint wide">正在计算余额和手续费...</p>
              <p v-else-if="payoutQuoteError" class="hint error wide">{{ payoutQuoteError }}</p>
              <p v-else-if="payoutQuoteCacheHint" class="hint wide">{{ payoutQuoteCacheHint }}</p>
              <p v-else-if="payoutAmountExceedsSingleAccount" class="hint error wide">
                当前单笔提现需要一个地址余额足够。该币种总余额可能分散在多个地址，请降低金额或先归集资金。
              </p>
              <div class="actions wide">
                <button class="btn primary" type="button" :disabled="payoutSubmitDisabled" @click="createPayout">
                  <Send :size="16" />
                  发起提现
                </button>
              </div>
            </div>
          </div>

          <div class="panel">
            <div class="panel-title">
              <div>
                <h2>提现记录</h2>
                <p>按币种、状态、地址或 txid 筛选</p>
              </div>
              <button class="btn" type="button" @click="loadPayouts">
                <Search :size="16" />
                查询
              </button>
            </div>
            <div class="filters payout-filters">
              <label class="field">
                <span>币种</span>
                <select v-model="payoutFilters.crypto">
                  <option value="">全部</option>
                  <option v-for="wallet in wallets" :key="wallet.name" :value="wallet.name">{{ wallet.name }}</option>
                </select>
              </label>
              <label class="field">
                <span>状态</span>
                <select v-model="payoutFilters.status">
                  <option value="">全部</option>
                  <option value="IN_PROGRESS">IN_PROGRESS</option>
                  <option value="SUCCESS">SUCCESS</option>
                  <option value="PARTIAL">PARTIAL</option>
                  <option value="FAIL">FAIL</option>
                </select>
              </label>
              <label class="field">
                <span>地址</span>
                <input v-model="payoutFilters.dest_addr" />
              </label>
              <label class="field">
                <span>TxID</span>
                <input v-model="payoutFilters.txid" />
              </label>
            </div>
          </div>
        </div>

        <div class="panel">
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>币种</th>
                  <th>金额</th>
                  <th>手续费</th>
                  <th>目标地址</th>
                  <th>状态</th>
                  <th>TxID</th>
                  <th>错误</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="payout in payouts" :key="payout.id">
                  <td data-label="时间">{{ formatTime(payout.created_at) }}</td>
                  <td data-label="币种">{{ payout.crypto }}</td>
                  <td data-label="金额">{{ payout.amount }}</td>
                  <td data-label="手续费">{{ payout.fee ? `${formatAmount(payout.fee)} ${payout.fee_asset || ''}` : '-' }}</td>
                  <td data-label="目标地址"><span class="mono">{{ payout.destination }}</span></td>
                  <td data-label="状态"><span class="badge" :class="payoutTone(payout.status)">{{ payout.status }}</span></td>
                  <td data-label="TxID">
                    <span class="mono">{{ (payout.txids || []).join(", ") || "-" }}</span>
                    <div v-if="(payout.transactions || []).length" class="tx-detail-list">
                      <div v-for="tx in payout.transactions" :key="tx.id || `${tx.kind}-${tx.txid}-${tx.amount}`" class="tx-detail-row">
                        <span class="badge muted">{{ tx.kind || "payout" }}</span>
                        <span class="mono">{{ tx.txid || "-" }}</span>
                        <span>{{ formatAmount(tx.amount) }} {{ tx.crypto || payout.crypto }}</span>
                        <span>{{ tx.status }}</span>
                        <span v-if="tx.error" class="text-bad">{{ tx.error }}</span>
                      </div>
                    </div>
                  </td>
                  <td data-label="错误"><span class="text-bad">{{ payout.error }}</span></td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section v-else-if="state.view === 'settings'" class="view-stack">
        <div class="panel settings-panel">
          <div class="panel-title">
            <div>
              <h2>管理员设置</h2>
              <p>更新管理员用户名与密码</p>
            </div>
            <ShieldCheck :size="22" class="text-blue" />
          </div>
          <div class="form-grid">
            <label class="field">
              <span>用户名</span>
              <input v-model="settingsForm.username" autocomplete="username" />
            </label>
            <label class="field">
              <span>当前密码</span>
              <input v-model="settingsForm.current_password" type="password" autocomplete="current-password" />
            </label>
            <label class="field">
              <span>新密码</span>
              <input v-model="settingsForm.new_password" type="password" autocomplete="new-password" />
            </label>
            <label class="field">
              <span>确认新密码</span>
              <input v-model="settingsForm.confirm_password" type="password" autocomplete="new-password" />
            </label>
            <div class="actions wide">
              <button class="btn primary" type="button" @click="saveSettings">
                <Save :size="16" />
                保存账户
              </button>
            </div>
          </div>
        </div>

        <div class="panel api-doc-panel">
          <div class="panel-title">
            <div>
              <h2>API 与 SDK 文档</h2>
              <p>接入收款、订单查询、回调、提现与管理接口</p>
            </div>
            <button class="btn compact" type="button" @click="copyDocSnippet(fullApiReferenceText, 'API 文档已复制')">
              <Copy :size="16" />
              复制文档
            </button>
          </div>

          <div class="api-overview">
            <div class="api-overview-card">
              <span>Base URL</span>
              <strong>{{ apiBaseURL }}</strong>
            </div>
            <div class="api-overview-card">
              <span>商户 Header</span>
              <strong>X-Shkeeper-Api-Key</strong>
            </div>
            <div class="api-overview-card">
              <span>当前 API Key</span>
              <strong>{{ maskedMerchantApiKey }}</strong>
              <button class="link-button" type="button" :disabled="!merchantApiKey" @click="copyDocSnippet(merchantApiKey, 'API Key 已复制')">
                复制
              </button>
            </div>
            <div class="api-overview-card">
              <span>已启用币种</span>
              <strong>{{ enabledWallets.map((item) => item.name).join(", ") || "暂无" }}</strong>
            </div>
          </div>

          <div class="doc-callout">
            <strong>认证方式</strong>
            <span>业务系统只需要使用请求头 <code>X-Shkeeper-Api-Key</code>。这里仅展示支付、订单、余额、回调联调等商户对接接口，后台管理、钱包配置和内部 worker 通知不会出现在对接文档中。</span>
          </div>

          <div class="sdk-grid">
            <article v-for="snippet in sdkSnippets" :key="snippet.title" class="sdk-card">
              <div class="sdk-head">
                <div>
                  <h3>{{ snippet.title }}</h3>
                  <p>{{ snippet.description }}</p>
                </div>
                <button class="icon-btn" type="button" :title="`复制 ${snippet.title}`" @click="copyDocSnippet(snippet.code, `${snippet.title} 已复制`)">
                  <Copy :size="16" />
                </button>
              </div>
              <pre><code>{{ snippet.code }}</code></pre>
            </article>
          </div>

          <div class="api-section-grid">
            <section v-for="group in apiDocGroups" :key="group.title" class="api-section">
              <div class="api-section-title">
                <h3>{{ group.title }}</h3>
                <p>{{ group.description }}</p>
              </div>
              <div class="endpoint-list">
                <article
                  v-for="endpoint in group.endpoints"
                  :key="endpointKey(endpoint)"
                  class="endpoint-row"
                  :class="{ expanded: isEndpointExpanded(endpoint) }"
                >
                  <button class="endpoint-summary" type="button" @click="toggleApiEndpoint(endpoint)">
                    <span class="method-badge" :class="endpoint.method.toLowerCase()">{{ endpoint.method }}</span>
                    <code>{{ endpoint.path }}</code>
                    <span class="endpoint-auth">{{ endpoint.auth }}</span>
                    <ChevronDown :size="16" :class="{ rotated: isEndpointExpanded(endpoint) }" />
                    <p>{{ endpoint.note }}</p>
                  </button>
                  <div v-if="isEndpointExpanded(endpoint)" class="endpoint-detail">
                    <div class="endpoint-detail-head">
                      <strong>请求体与调用方式</strong>
                      <button class="icon-btn" type="button" title="复制接口格式" @click="copyDocSnippet(endpointRequestText(endpoint), '接口格式已复制')">
                        <Copy :size="16" />
                      </button>
                    </div>
                    <div class="endpoint-detail-grid">
                      <div class="endpoint-doc-block">
                        <span>路径 / 查询 / Header</span>
                        <pre><code>{{ endpointParamsText(endpoint) }}</code></pre>
                      </div>
                      <div class="endpoint-doc-block">
                        <span>请求体格式</span>
                        <pre><code>{{ endpointBodyText(endpoint) }}</code></pre>
                      </div>
                      <div class="endpoint-doc-block">
                        <span>响应示例</span>
                        <pre><code>{{ endpointResponseText(endpoint) }}</code></pre>
                      </div>
                      <div class="endpoint-doc-block">
                        <span>调用示例</span>
                        <pre><code>{{ endpointCurlText(endpoint) }}</code></pre>
                      </div>
                    </div>
                  </div>
                </article>
              </div>
            </section>
          </div>

          <div class="api-section callback-section">
            <div class="api-section-title">
              <h3>回调 Payload</h3>
              <p>收款和提现完成后会向业务系统的 callback_url 发送 JSON</p>
            </div>
            <div class="callback-grid">
              <article v-for="callback in callbackDocs" :key="callback.title" class="callback-card">
                <div class="sdk-head">
                  <div>
                    <h3>{{ callback.title }}</h3>
                    <p>{{ callback.description }}</p>
                  </div>
                  <button class="icon-btn" type="button" :title="`复制 ${callback.title}`" @click="copyDocSnippet(callback.payload, `${callback.title} 已复制`)">
                    <Copy :size="16" />
                  </button>
                </div>
                <pre><code>{{ callback.payload }}</code></pre>
              </article>
            </div>
          </div>
        </div>
      </section>
    </main>

    <div v-if="state.notice" class="notice" :class="{ err: state.noticeType === 'error' }">
      {{ state.notice }}
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from "vue";
import {
  Activity,
  ChartNoAxesCombined,
  ChevronDown,
  Copy,
  Download,
  KeyRound,
  Plus,
  RefreshCw,
  Save,
  Search,
  Send,
  Server,
  Settings,
  ShieldCheck,
  Trash2,
  UserRound,
  Wallet
} from "@lucide/vue";
import { api, queryString } from "./api";

const navItems = [
  { id: "wallets", label: "币种", path: "/wallets", icon: Wallet },
  { id: "orders", label: "订单", path: "/transactions", icon: Activity },
  { id: "rates", label: "汇率", path: "/rates", icon: ChartNoAxesCombined },
  { id: "payouts", label: "提现", path: "/payouts", icon: Send },
  { id: "settings", label: "设置", path: "/settings", icon: Settings }
];

const rateSources = [
  { value: "dynamic", label: "自动" },
  { value: "binance", label: "Binance" },
  { value: "coinbase", label: "Coinbase" },
  { value: "kraken", label: "Kraken" },
  { value: "kucoin", label: "KuCoin" },
  { value: "manual", label: "手动" }
];

const feePolicies = [
  { value: "NO_FEE", label: "不收手续费" },
  { value: "PERCENT_FEE", label: "按百分比" },
  { value: "FIXED_FEE", label: "固定费用" },
  { value: "PERCENT_OR_MINIMAL_FIXED_FEE", label: "百分比或最低固定费" }
];

const orderStatuses = ["UNPAID", "PARTIAL", "PAID", "OVERPAID", "CANCELLED", "REFUNDED", "OUTGOING", "IN_PROGRESS", "SUCCESS", "FAIL"];

const state = reactive({
  view: "wallets",
  loading: false,
  notice: "",
  noticeType: "success",
  bootstrap: {},
  selectedCrypto: "",
  selectedFiat: "USD",
  walletDetail: null,
  serviceExpanded: false,
  importExpanded: false,
  nextOrderCursor: ""
});

const wallets = ref([]);
const cryptoCatalog = ref([]);
const rates = ref([]);
const orders = ref([]);
const payouts = ref([]);
const importReport = ref(null);
const payoutQuote = ref(null);
const payoutQuoteLoading = ref(false);
const payoutQuoteError = ref("");
const expandedOrders = reactive(new Set());
const expandedApiEndpoint = ref("");

const walletForm = reactive({
  enabled: false,
  api_key: "",
  autopayout_enabled: false,
  autopayout_policy: "manual",
  autopayout_condition: "",
  autopayout_destination: "",
  autopayout_fee: "",
  reserve_policy: "disable",
  reserve_amount: "",
  partial_paid_percent: "95",
  overpaid_percent: "105",
  recalculate_after: 0,
  confirmations: 1
});

const serverForm = reactive({ host: "", username: "", password: "", key: "" });
const destinationForm = reactive({ addr: "", comment: "" });
const cryptoApply = reactive({ command: "", short_command: "", auto_apply_enabled: false, apply_output: "", apply_error: "" });
const importForm = reactive({
  module: "BNB",
  default_crypto: "BNB-USDT",
  account_password: "",
  legacy_account_password: "",
  json: ""
});
const bulk = reactive({ source: "", fee: "", fee_policy: "" });
const orderFilters = reactive({ external_id: "", status: "", crypto: "", from_date: "", to_date: "", limit: 30 });
const payoutFilters = reactive({ crypto: "", status: "", dest_addr: "", txid: "", limit: 50 });
const payoutForm = reactive({ crypto: "", amount: "", destination: "", external_id: "", callback_url: "" });
const settingsForm = reactive({ username: "", current_password: "", new_password: "", confirm_password: "" });

const fiats = computed(() => state.bootstrap.fiats?.length ? state.bootstrap.fiats : ["USD"]);
const enabledWallets = computed(() => wallets.value.filter((item) => item.enabled));
const visibleWallets = enabledWallets;
const selectedWallet = computed(() => state.walletDetail || visibleWallets.value.find((item) => item.name === state.selectedCrypto));
const selectedPayoutWallet = computed(() => wallets.value.find((item) => item.name === payoutForm.crypto));
const payoutQuoteBalance = computed(() => payoutQuote.value?.balance ?? selectedPayoutWallet.value?.balance ?? "");
const payoutQuoteMaxSingle = computed(() => payoutQuote.value?.max_single_account ?? payoutQuoteBalance.value);
const payoutQuoteFee = computed(() => payoutQuote.value?.fee || "");
const payoutQuoteFeeAsset = computed(() => payoutQuote.value?.fee_asset || payoutForm.crypto || "");
const payoutAmountExceedsSingleAccount = computed(() => decimalGreaterThan(payoutForm.amount, payoutQuoteMaxSingle.value));
const payoutQuoteUnavailable = computed(() => payoutQuote.value?.status === "disabled" || payoutQuote.value?.cache_ready === false);
const payoutSubmitDisabled = computed(() => (
  payoutQuoteLoading.value
  || payoutQuoteUnavailable.value
  || Boolean(payoutQuoteError.value)
  || payoutAmountExceedsSingleAccount.value
));
const payoutQuoteCacheHint = computed(() => {
  if (payoutQuote.value?.cache_ready === false) return "TRON 余额缓存正在后台刷新，稍后会自动更新。";
  if (payoutQuote.value?.cache_stale) return "余额缓存可能已过期，后台会继续刷新。";
  return "";
});
const activationNotice = computed(() => {
  const activation = selectedWallet.value?.activation;
  if (!activation || activation.required === false) return null;
  if (activation.error) {
    return {
      tone: "bad",
      title: "TRON 激活状态检测失败",
      description: activation.error,
      sample: []
    };
  }
  const inactive = Number(activation.inactive || 0);
  if (inactive <= 0) return null;
  const checked = Number(activation.checked || 0);
  const total = Number(activation.total || 0);
  const coverage = total && checked && checked < total ? `已检查 ${checked} / ${total} 个地址，` : "";
  return {
    tone: "warn",
    title: `发现 ${inactive} 个未激活地址`,
    description: `${coverage}向未激活地址转入少量 TRX 即可激活；TRC20 提现仍需要保留 TRX 支付网络资源或手续费。`,
    sample: Array.isArray(activation.sample_inactive) ? activation.sample_inactive : []
  };
});
const primaryDocCrypto = computed(() => enabledWallets.value.find((item) => item.name === "USDT")?.name || enabledWallets.value[0]?.name || "USDT");
const apiBaseURL = computed(() => window.location.origin);
const merchantApiKey = computed(() => {
  const wallet = enabledWallets.value.find((item) => item.api_key) || wallets.value.find((item) => item.api_key);
  return wallet?.api_key || "";
});
const maskedMerchantApiKey = computed(() => maskSecret(merchantApiKey.value));
const sdkSnippets = computed(() => {
  const baseURL = apiBaseURL.value;
  const apiKey = merchantApiKey.value || "YOUR_API_KEY";
  const crypto = primaryDocCrypto.value;
  return [
    {
      title: "cURL 收款",
      description: "创建收款地址，随后用 external_id 查询订单状态。",
      code: `BASE_URL="${baseURL}"
API_KEY="${apiKey}"

curl -X POST "$BASE_URL/api/v1/${crypto}/payment_request" \\
  -H "X-Shkeeper-Api-Key: $API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"external_id":"order-1001","fiat":"USD","amount":"19.90","callback_url":"https://merchant.example/shkeeper/callback"}'

curl -H "X-Shkeeper-Api-Key: $API_KEY" \\
  "$BASE_URL/api/v1/orders/order-1001"`
    },
    {
      title: "JavaScript SDK",
      description: "适合 Node.js、Nuxt、Vue 服务端或任何支持 fetch 的运行时。",
      code: `export class ShkeeperClient {
  constructor({ baseURL = "${baseURL}", apiKey = "${apiKey}" } = {}) {
    this.baseURL = baseURL.replace(/\\/$/, "");
    this.apiKey = apiKey;
  }

  async request(path, options = {}) {
    const response = await fetch(this.baseURL + path, {
      ...options,
      headers: {
        "Content-Type": "application/json",
        "X-Shkeeper-Api-Key": this.apiKey,
        ...(options.headers || {})
      }
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.message || response.statusText);
    return data;
  }

  createPayment({ crypto = "${crypto}", externalId, fiat = "USD", amount, callbackUrl }) {
    return this.request("/api/v1/" + encodeURIComponent(crypto) + "/payment_request", {
      method: "POST",
      body: JSON.stringify({
        external_id: externalId,
        fiat,
        amount: String(amount),
        callback_url: callbackUrl
      })
    });
  }

  getOrder(externalId) {
    return this.request("/api/v1/orders/" + encodeURIComponent(externalId));
  }
}`
    },
    {
      title: "PHP SDK",
      description: "适合传统 PHP 商城、回调接收端和后台任务脚本。",
      code: `final class ShkeeperClient {
  public function __construct(
    private string $baseUrl = "${baseURL}",
    private string $apiKey = "${apiKey}"
  ) {}

  public function request(string $method, string $path, array $payload = null): array {
    $ch = curl_init(rtrim($this->baseUrl, "/") . $path);
    $headers = ["X-Shkeeper-Api-Key: " . $this->apiKey, "Content-Type: application/json"];
    curl_setopt_array($ch, [
      CURLOPT_RETURNTRANSFER => true,
      CURLOPT_CUSTOMREQUEST => $method,
      CURLOPT_HTTPHEADER => $headers,
    ]);
    if ($payload !== null) {
      curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($payload, JSON_UNESCAPED_SLASHES));
    }
    $body = curl_exec($ch);
    $status = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    if ($body === false || $status >= 300) {
      throw new RuntimeException($body ?: curl_error($ch));
    }
    return json_decode($body, true) ?: [];
  }

  public function createPayment(string $externalId, string $amount, string $callbackUrl, string $crypto = "${crypto}"): array {
    return $this->request("POST", "/api/v1/" . rawurlencode($crypto) . "/payment_request", [
      "external_id" => $externalId,
      "fiat" => "USD",
      "amount" => $amount,
      "callback_url" => $callbackUrl,
    ]);
  }
}`
    },
    {
      title: "Go SDK",
      description: "适合后端服务直接创建订单、查询状态和接收回调。",
      code: `package shkeeper

import (
  "bytes"
  "context"
  "encoding/json"
  "fmt"
  "net/http"
  "strings"
)

type Client struct {
  BaseURL string
  APIKey  string
  HTTP    *http.Client
}

func (c Client) Do(ctx context.Context, method, path string, body any, out any) error {
  var payload []byte
  if body != nil {
    var err error
    payload, err = json.Marshal(body)
    if err != nil {
      return err
    }
  }
  req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
  if err != nil {
    return err
  }
  req.Header.Set("Content-Type", "application/json")
  req.Header.Set("X-Shkeeper-Api-Key", c.APIKey)
  httpClient := c.HTTP
  if httpClient == nil {
    httpClient = http.DefaultClient
  }
  res, err := httpClient.Do(req)
  if err != nil {
    return err
  }
  defer res.Body.Close()
  if res.StatusCode >= 300 {
    return fmt.Errorf("shkeeper: %s", res.Status)
  }
  return json.NewDecoder(res.Body).Decode(out)
}`
    }
  ];
});
const apiDocGroups = computed(() => {
  const crypto = primaryDocCrypto.value;
  return [
    {
      title: "基础发现",
      description: "业务系统初始化时可用来发现当前可支付币种。",
      endpoints: [
        {
          method: "GET",
          path: "/api/v1/crypto",
          auth: "公开",
          note: "列出系统支持的币种和支付网关状态。",
          response: {
            status: "success",
            crypto: [{ name: crypto, display_name: crypto, enabled: true }]
          }
        }
      ]
    },
    {
      title: "支付订单",
      description: "创建收款、报价、校验链上交易，这是商户系统最常用的接入面。",
      endpoints: [
        {
          method: "POST",
          path: "/api/v1/{crypto}/payment_request",
          auth: "商户 API Key",
          note: "创建收款订单；返回链上收款地址和本次换算金额。",
          body: {
            external_id: "order-1001",
            fiat: "USD",
            amount: "19.90",
            callback_url: "https://merchant.example/shkeeper/callback"
          },
          response: {
            status: "success",
            id: 123,
            exchange_rate: "1",
            amount: "19.90",
            wallet: "on-chain-payment-address",
            recalculate_after: 0,
            display_name: crypto
          }
        },
        {
          method: "POST",
          path: "/api/v1/{crypto}/quote",
          auth: "商户 API Key",
          note: "按法币金额预估需要支付的币种金额。",
          body: { fiat: "USD", amount: "19.90" },
          response: {
            status: "success",
            fiat: "USD",
            amount_fiat: "19.90",
            crypto,
            amount_crypto: "19.90",
            exchange_rate: "1"
          }
        },
        {
          method: "POST",
          path: "/api/v1/{crypto}/verified-transaction",
          auth: "商户 API Key",
          note: "手动校验指定 txid 是否匹配订单地址和金额。",
          body: {
            txid: "0x...",
            addr: "on-chain-payment-address",
            amount: "19.90",
            external_id: "order-1001",
            confirmations: 12,
            send_callback: true
          },
          response: {
            status: "success",
            id: 1,
            duplicate: false,
            invoice: { external_id: "order-1001", status: "PAID" }
          }
        }
      ]
    },
    {
      title: "订单查询",
      description: "按业务订单号、分页条件或交易哈希同步支付状态。",
      endpoints: [
        {
          method: "GET",
          path: "/api/v1/orders",
          auth: "商户 API Key",
          note: "聚合查询收款和提现订单，支持分页和筛选。",
          query: {
            limit: 30,
            cursor: "next_cursor",
            external_id: "order-1001",
            status: "PAID",
            crypto
          },
          response: {
            status: "success",
            orders: [{ external_id: "order-1001", status: "PAID", crypto }],
            next_cursor: ""
          }
        },
        {
          method: "GET",
          path: "/api/v1/orders/{external_id}",
          auth: "商户 API Key",
          note: "查询单个业务订单的收款、提现和状态汇总。",
          path_params: { external_id: "order-1001" },
          response: {
            status: "success",
            order: { external_id: "order-1001", status: "PAID", invoices: [], payouts: [] }
          }
        },
        {
          method: "GET",
          path: "/api/v1/invoices",
          auth: "商户 API Key",
          note: "分页查询收款发票，兼容旧版 invoice 接入。",
          query: { limit: 30, cursor: "next_cursor", status: "PAID", crypto },
          response: {
            status: "success",
            invoices: [{ external_id: "order-1001", status: "PAID", crypto }],
            next_cursor: ""
          }
        },
        {
          method: "GET",
          path: "/api/v1/invoices/{external_id}",
          auth: "商户 API Key",
          note: "按业务订单号查询收款发票。",
          path_params: { external_id: "order-1001" },
          response: {
            status: "success",
            invoice: { external_id: "order-1001", status: "PAID", crypto }
          }
        },
        {
          method: "GET",
          path: "/api/v1/transactions",
          auth: "商户 API Key",
          note: "查询链上交易记录。",
          query: { limit: 30, cursor: "next_cursor", crypto, addr: "on-chain-payment-address" },
          response: {
            status: "success",
            transactions: [{ txid: "0x...", crypto, amount: "19.90" }],
            next_cursor: ""
          }
        },
        {
          method: "GET",
          path: "/api/v1/transactions/{crypto}/{addr}",
          auth: "商户 API Key",
          note: "按币种和地址查询交易记录。",
          path_params: { crypto, addr: "on-chain-payment-address" },
          response: {
            status: "success",
            transactions: [{ txid: "0x...", crypto, amount: "19.90" }]
          }
        },
        {
          method: "GET",
          path: "/api/v1/tx-info/{txid}/{external_id}",
          auth: "商户 API Key",
          note: "按 txid 和 external_id 查询交易归属。",
          path_params: { txid: "0x...", external_id: "order-1001" },
          response: {
            status: "success",
            txid: "0x...",
            external_id: "order-1001",
            crypto
          }
        }
      ]
    },
    {
      title: "余额与地址",
      description: "用于商户系统展示可用余额、网关状态和收款地址列表。",
      endpoints: [
        {
          method: "GET",
          path: "/api/v1/crypto/balances?includes=USDT,BNB",
          auth: "商户 API Key",
          note: "批量查询余额；includes 可选。",
          query: { includes: "USDT,BNB" },
          response: {
            status: "success",
            balances: [{ crypto, balance: "19.90" }]
          }
        },
        {
          method: "GET",
          path: "/api/v1/{crypto}/balance",
          auth: "商户 API Key",
          note: "查询单币种余额。",
          response: { status: "success", crypto, balance: "19.90" }
        },
        {
          method: "GET",
          path: "/api/v1/{crypto}/status",
          auth: "商户 API Key",
          note: "查询 worker 在线、可用、错误状态。",
          response: { status: "Synced", crypto }
        },
        {
          method: "GET",
          path: "/api/v1/{crypto}/addresses",
          auth: "商户 API Key",
          note: "列出当前币种地址。",
          response: { status: "success", addresses: ["on-chain-payment-address"] }
        },
        {
          method: "GET",
          path: "/api/v1/{crypto}/fee-deposit-address",
          auth: "商户 API Key",
          note: "查询手续费充值地址。",
          response: { status: "success", address: "fee-deposit-address" }
        }
      ]
    },
    {
      title: "提现订单查询",
      description: "只提供商户可查询的提现状态，不暴露后台发起提现接口。",
      endpoints: [
        {
          method: "GET",
          path: "/api/v1/{crypto}/payout/status?external_id=payout-1001",
          auth: "商户 API Key",
          note: "按 external_id 查询提现状态。",
          query: { external_id: "payout-1001" },
          response: {
            id: 456,
            external_id: "payout-1001",
            crypto,
            status: "SUCCESS",
            amount: "10",
            destination: "destination-address",
            txid: "0x..."
          }
        },
        {
          method: "GET",
          path: "/api/v1/{crypto}/payouts?amount=10",
          auth: "商户 API Key",
          note: "查询满足金额条件的可提现地址和余额。",
          query: { amount: "10" },
          response: {
            status: "success",
            payouts: [{ addr: "source-address", amount: "10" }]
          }
        }
      ]
    },
    {
      title: "联调工具",
      description: "用于测试业务系统回调接收链路。",
      endpoints: [
        {
          method: "POST",
          path: "/api/v1/test-callback-receiver",
          auth: "商户 API Key",
          note: "测试业务系统 callback_url 的接收能力。",
          body: {
            callback_url: "https://merchant.example/shkeeper/callback",
            payload: { external_id: "order-1001", status: "PAID" }
          },
          response: { status: "success" }
        }
      ]
    }
  ];
});
const callbackDocs = computed(() => [
  {
    title: "收款回调",
    description: "订单达到支付状态后发送到 payment_request 中的 callback_url。",
    payload: JSON.stringify({
      id: 123,
      external_id: "order-1001",
      tx_hash: "0x...",
      amount_crypto: "19.90",
      crypto: primaryDocCrypto.value,
      amount_fiat: "19.90",
      fiat: "USD",
      balance_fiat: "0",
      balance_crypto: "0",
      status: "PAID",
      transactions: [
        {
          txid: "0x...",
          addr: "merchant-address",
          amount: "19.90",
          confirmations: 12
        }
      ]
    }, null, 2)
  },
  {
    title: "提现回调",
    description: "提现成功或失败后发送到 payout 请求中的 callback_url。",
    payload: JSON.stringify({
      payout_id: 456,
      external_id: "payout-1001",
      tx_hash: "0x...",
      dest_addr: "destination-address",
      amount: "10",
      crypto: primaryDocCrypto.value,
      amount_fiat: "10.00",
      fiat: "USD",
      timestamp: Math.floor(Date.now() / 1000)
    }, null, 2)
  }
]);
const fullApiReferenceText = computed(() => {
  const endpoints = apiDocGroups.value.map((group) => {
    const lines = group.endpoints.map((endpoint) => [
      `### ${endpoint.method} ${endpoint.path}`,
      `认证: ${endpoint.auth}`,
      endpoint.note,
      "",
      "路径 / 查询 / Header:",
      endpointParamsText(endpoint),
      "",
      "请求体格式:",
      endpointBodyText(endpoint),
      "",
      "响应示例:",
      endpointResponseText(endpoint)
    ].join("\n"));
    return `## ${group.title}\n${group.description}\n${lines.join("\n")}`;
  }).join("\n\n");
  const snippets = sdkSnippets.value.map((snippet) => `## ${snippet.title}\n${snippet.description}\n\n\`\`\`\n${snippet.code}\n\`\`\``).join("\n\n");
  const callbacks = callbackDocs.value.map((callback) => `## ${callback.title}\n${callback.description}\n\n\`\`\`json\n${callback.payload}\n\`\`\``).join("\n\n");
  return [
    "# SHKeeper 接入 API 与 SDK 文档",
    `Base URL: ${apiBaseURL.value}`,
    "商户请求头: X-Shkeeper-Api-Key: YOUR_API_KEY",
    "范围: 仅包含业务系统对接支付、订单、余额和回调联调需要的接口。",
    `当前启用币种: ${enabledWallets.value.map((item) => item.name).join(", ") || "暂无"}`,
    "",
    endpoints,
    "",
    "# SDK 示例",
    snippets,
    "",
    "# 回调 Payload",
    callbacks
  ].join("\n");
});
const healthyWallets = computed(() => visibleWallets.value.filter((item) => statusTone(item) === "text-ok"));
const payoutDestinations = computed(() => state.walletDetail?.payout_destinations || []);
const selectedServiceCount = computed(() => cryptoCatalog.value.filter((item) => item.selected).length);
const displayCryptoCommand = computed(() => cryptoApply.short_command || cryptoApply.command);
const importModules = computed(() => {
  const values = cryptoCatalog.value.map((item) => importModuleForCrypto(item)).filter(Boolean);
  return Array.from(new Set(values)).sort();
});
const importCryptoOptions = computed(() => {
  const scoped = cryptoCatalog.value.filter((item) => importModuleForCrypto(item) === importForm.module);
  return scoped.length ? scoped : cryptoCatalog.value;
});
const importReportText = computed(() => {
  if (!importReport.value) return "";
  return JSON.stringify({ modules: importReport.value.modules || {}, cryptos: importReport.value.cryptos || {} });
});
const encryptionLabel = computed(() => {
  const encryption = state.bootstrap.wallet_encryption || {};
  return encryption.runtime_status || encryption.persistent_status || "未知";
});

const pageTitle = computed(() => ({
  wallets: "币种与钱包",
  orders: "订单管理",
  rates: "汇率设置",
  payouts: "提现管理",
  settings: "设置"
}[state.view] || "管理台"));

const pageEyebrow = computed(() => ({
  wallets: "Wallets",
  orders: "Orders",
  rates: "Exchange rates",
  payouts: "Payouts",
  settings: "Admin"
}[state.view] || "Admin"));

function notify(message, type = "success") {
  state.notice = message;
  state.noticeType = type;
  window.clearTimeout(notify.timer);
  notify.timer = window.setTimeout(() => {
    state.notice = "";
  }, 3200);
}

async function runTask(task, successMessage) {
  state.loading = true;
  try {
    const result = await task();
    if (successMessage) notify(successMessage);
    return result;
  } catch (error) {
    notify(error.message || "请求失败", "error");
    throw error;
  } finally {
    state.loading = false;
  }
}

async function copyText(text) {
  const value = String(text || "");
  if (!value) return false;
  if (navigator.clipboard?.writeText && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Fall through to the textarea path for denied permissions.
    }
  }
  if (legacyCopyText(value)) return true;
  window.prompt("复制以下内容", value);
  return false;
}

function legacyCopyText(value) {
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  const activeElement = document.activeElement;
  try {
    textarea.focus({ preventScroll: true });
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
    if (activeElement?.focus) activeElement.focus({ preventScroll: true });
  }
}

function routeState(pathname = window.location.pathname) {
  if (pathname.startsWith("/rates")) return { view: "rates" };
  if (pathname.startsWith("/transactions")) return { view: "orders" };
  if (pathname.startsWith("/payouts")) return { view: "payouts" };
  if (pathname.startsWith("/settings")) return { view: "settings" };
  if (pathname.startsWith("/payout/")) return { view: "payouts", crypto: decodeURIComponent(pathname.split("/").pop()) };
  if (pathname.startsWith("/wallet/")) return { view: "wallets", crypto: decodeURIComponent(pathname.split("/").pop()) };
  return { view: "wallets" };
}

function applyRoute() {
  const next = routeState();
  state.view = next.view;
  if (next.crypto) {
    state.selectedCrypto = next.crypto;
    payoutForm.crypto = next.crypto;
  }
}

function go(view, path) {
  state.view = view;
  window.history.pushState({}, "", path);
  refreshCurrent();
}

async function loadBootstrap() {
  const data = await api("/api/v1/admin/bootstrap");
  state.bootstrap = data;
  state.selectedFiat = (data.fiats?.[0] || "USD").toUpperCase();
  settingsForm.username = data.user?.username || "";
}

async function loadWallets(options = {}) {
  const includeDetail = options.includeDetail ?? state.view === "wallets";
  await loadCryptoCatalog();
  const data = await api("/api/v1/admin/wallets");
  wallets.value = data.wallets || [];
  scheduleWalletBalanceRefresh(wallets.value);
  const activeWallets = wallets.value.filter((item) => item.enabled);
  const selectedVisible = activeWallets.some((item) => item.name === state.selectedCrypto);
  if (!selectedVisible) {
    state.selectedCrypto = activeWallets[0]?.name || "";
    state.walletDetail = null;
  }
  const defaultPayoutWallet = activeWallets[0];
  if ((!payoutForm.crypto || !wallets.value.some((item) => item.name === payoutForm.crypto && item.enabled)) && defaultPayoutWallet) {
    payoutForm.crypto = defaultPayoutWallet.name;
  }
  if (includeDetail && state.selectedCrypto) await loadWalletDetail(state.selectedCrypto);
}

let walletBalanceRefreshTimer = 0;
let walletBalanceRefreshAttempts = 0;
const walletBalanceRefreshDelayMs = 2500;
const walletBalanceRefreshMaxAttempts = 18;
function scheduleWalletBalanceRefresh(list) {
  window.clearTimeout(walletBalanceRefreshTimer);
  const needsRefresh = list.some((wallet) => wallet?.refreshing || wallet?.cache_ready === false || wallet?.balance_source === "warming");
  if (!needsRefresh) {
    walletBalanceRefreshAttempts = 0;
    return;
  }
  if (state.view !== "wallets" || walletBalanceRefreshAttempts >= walletBalanceRefreshMaxAttempts) return;
  walletBalanceRefreshAttempts += 1;
  walletBalanceRefreshTimer = window.setTimeout(async () => {
    if (state.view !== "wallets") return;
    try {
      await loadWallets({ includeDetail: false });
    } catch {
      // Keep the foreground UI quiet; manual refresh still reports errors through runTask.
    }
  }, walletBalanceRefreshDelayMs);
}

async function loadCryptoCatalog() {
  const data = await api("/api/v1/admin/cryptos");
  applyCryptoCatalog(data);
}

function applyCryptoCatalog(data) {
  cryptoCatalog.value = (data.cryptos || []).map((item) => ({ ...item, selected: Boolean(item.selected) }));
  cryptoApply.command = data.command || "";
  cryptoApply.short_command = data.short_command || "";
  cryptoApply.auto_apply_enabled = Boolean(data.auto_apply_enabled);
  cryptoApply.apply_output = data.apply_output || "";
  cryptoApply.apply_error = data.apply_error || "";
  if (!importModules.value.includes(importForm.module) && importModules.value.length) {
    importForm.module = importModules.value[0];
  }
  if (!importCryptoOptions.value.some((item) => item.name === importForm.default_crypto) && importCryptoOptions.value.length) {
    importForm.default_crypto = importCryptoOptions.value[0].name;
  }
}

async function copyCryptoCommand() {
  const text = cryptoApply.command || displayCryptoCommand.value;
  if (!text) return;
  if (await copyText(text)) {
    notify("命令已复制");
  } else {
    notify("复制失败，请手动选中命令复制", "error");
  }
}

async function copyDocSnippet(text, successMessage) {
  if (!text) return;
  if (await copyText(text)) {
    notify(successMessage || "已复制");
  } else {
    notify("复制失败，请手动选中文本复制", "error");
  }
}

function endpointKey(endpoint) {
  return `${endpoint.method}-${endpoint.path}`;
}

function isEndpointExpanded(endpoint) {
  return expandedApiEndpoint.value === endpointKey(endpoint);
}

function toggleApiEndpoint(endpoint) {
  const key = endpointKey(endpoint);
  expandedApiEndpoint.value = expandedApiEndpoint.value === key ? "" : key;
}

function endpointRequestText(endpoint) {
  return [
    `${endpoint.method} ${resolvedEndpointPath(endpoint)}`,
    "",
    "路径 / 查询 / Header:",
    endpointParamsText(endpoint),
    "",
    "请求体格式:",
    endpointBodyText(endpoint),
    "",
    "响应示例:",
    endpointResponseText(endpoint),
    "",
    "调用示例:",
    endpointCurlText(endpoint)
  ].join("\n");
}

function endpointParamsText(endpoint) {
  const lines = [];
  if (endpoint.auth.includes("商户")) {
    lines.push(`Header: X-Shkeeper-Api-Key: ${merchantApiKey.value || "YOUR_API_KEY"}`);
  } else {
    lines.push("Header: 无需认证");
  }
  const pathParams = { ...(endpoint.path_params || {}) };
  for (const name of endpoint.path.matchAll(/\{([^}]+)\}/g)) {
    if (pathParams[name[1]] === undefined) pathParams[name[1]] = samplePathParam(name[1]);
  }
  if (Object.keys(pathParams).length) {
    lines.push("Path: " + JSON.stringify(pathParams, null, 2));
  } else {
    lines.push("Path: 无");
  }
  if (endpoint.query && Object.keys(endpoint.query).length) {
    lines.push("Query: " + JSON.stringify(endpoint.query, null, 2));
  } else {
    lines.push("Query: 无");
  }
  return lines.join("\n");
}

function endpointBodyText(endpoint) {
  if (endpoint.method === "GET") return "无请求体";
  return docJSONString(endpoint.body, "无请求体");
}

function endpointResponseText(endpoint) {
  return docJSONString(endpoint.response || { status: "success" }, "{}");
}

function endpointCurlText(endpoint) {
  const url = `${apiBaseURL.value}${resolvedEndpointPath(endpoint)}`;
  const lines = [`curl -X ${endpoint.method} "${url}"`];
  if (endpoint.auth.includes("商户")) {
    lines.push(`  -H "X-Shkeeper-Api-Key: ${merchantApiKey.value || "YOUR_API_KEY"}"`);
  }
  if (endpoint.method !== "GET") {
    lines.push('  -H "Content-Type: application/json"');
    lines.push(`  -d '${docJSONString(endpoint.body || {}, "{}")}'`);
  }
  return lines.join(" \\\n");
}

function resolvedEndpointPath(endpoint) {
  let path = endpoint.path
    .replaceAll("{crypto}", encodeURIComponent(primaryDocCrypto.value))
    .replaceAll("{external_id}", "order-1001")
    .replaceAll("{txid}", "0x...")
    .replaceAll("{addr}", "on-chain-payment-address");
  if (!path.includes("?") && endpoint.query && Object.keys(endpoint.query).length) {
    const params = new URLSearchParams();
    Object.entries(endpoint.query).forEach(([key, value]) => {
      if (value !== undefined && value !== null && value !== "") params.set(key, String(value));
    });
    const query = params.toString();
    if (query) path += `?${query}`;
  }
  return path;
}

function samplePathParam(name) {
  if (name === "crypto") return primaryDocCrypto.value;
  if (name === "external_id") return "order-1001";
  if (name === "txid") return "0x...";
  if (name === "addr") return "on-chain-payment-address";
  return `{${name}}`;
}

function docJSONString(value, emptyText) {
  if (value === undefined || value === null || value === "") return emptyText;
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

function maskSecret(value) {
  const text = String(value || "");
  if (!text) return "未配置";
  if (text.length <= 14) return text;
  return `${text.slice(0, 7)}...${text.slice(-7)}`;
}

function importModuleForCrypto(item) {
  const name = item.name || "";
  const network = item.network || name;
  if (network === "TRX" || ["TRX", "USDT", "USDC"].includes(name)) return "TRON";
  if (network === "SOL" || name.startsWith("SOLANA-")) return "SOL";
  return network;
}

function normalizeLegacyWalletJSON(input) {
  let text = String(input || "")
    .replace(/^\uFEFF/, "")
    .replace(/```(?:json)?/gi, "")
    .replace(/```/g, "")
    .replace(/[\u200B-\u200D\u2060]/g, "")
    .trim();
  text = text
    .replace(/[“”]/g, "\"")
    .replace(/[‘’]/g, "'")
    .replace(/，/g, ",")
    .replace(/：/g, ":");
  const starts = [text.indexOf("{"), text.indexOf("[")].filter((index) => index >= 0);
  if (starts.length) {
    const start = Math.min(...starts);
    const end = Math.max(text.lastIndexOf("}"), text.lastIndexOf("]"));
    if (end > start) text = text.slice(start, end + 1).trim();
  }
  return text;
}

async function saveCryptoServices() {
  const selected = cryptoCatalog.value.filter((item) => item.selected).map((item) => item.name);
  if (!selected.length) {
    notify("至少保留一个币种服务", "error");
    return;
  }
  await runTask(async () => {
    const data = await api("/api/v1/admin/cryptos", { method: "POST", body: { cryptos: selected } });
    applyCryptoCatalog(data);
    await loadWallets();
    if (state.view === "rates") await loadRates();
  }, cryptoApply.auto_apply_enabled ? "币种服务已保存并应用" : "币种服务已保存");
}

async function importLegacyWallets() {
  if (!importForm.json.trim()) {
    notify("请粘贴旧版钱包 JSON", "error");
    return;
  }
  const normalizedJSON = normalizeLegacyWalletJSON(importForm.json);
  try {
    JSON.parse(normalizedJSON);
  } catch {
    notify("旧版钱包 JSON 格式不正确", "error");
    return;
  }
  await runTask(async () => {
    const data = await api("/api/v1/admin/wallet-import", {
      method: "POST",
      body: {
        module: importForm.module,
        default_crypto: importForm.default_crypto,
        account_password: importForm.account_password,
        legacy_account_password: importForm.legacy_account_password,
        json: normalizedJSON
      }
    });
    importReport.value = data.report;
    await loadWallets();
    if (state.view === "rates") await loadRates();
  }, "旧钱包已导入");
}

async function loadWalletDetail(crypto = state.selectedCrypto) {
  if (!crypto) return;
  const data = await api(`/api/v1/admin/wallets/${encodeURIComponent(crypto)}`);
  state.walletDetail = data.wallet;
  applyWalletToForms(data.wallet);
}

function applyWalletToForms(wallet) {
  if (!wallet) return;
  walletForm.enabled = Boolean(wallet.enabled);
  walletForm.api_key = wallet.api_key || "";
  walletForm.autopayout_enabled = Boolean(wallet.autopayout_enabled);
  walletForm.autopayout_policy = wallet.autopayout_policy || "manual";
  walletForm.autopayout_condition = wallet.autopayout_condition || "";
  walletForm.autopayout_destination = wallet.autopayout_destination || "";
  walletForm.autopayout_fee = wallet.autopayout_fee || "";
  walletForm.reserve_policy = wallet.reserve_policy || "disable";
  walletForm.reserve_amount = wallet.reserve_amount || "";
  walletForm.partial_paid_percent = wallet.partial_paid_percent || "95";
  walletForm.overpaid_percent = wallet.overpaid_percent || "105";
  walletForm.recalculate_after = Number(wallet.recalculate_after || 0);
  walletForm.confirmations = Number(wallet.confirmations || 0);
  serverForm.host = wallet.server?.host || wallet.server?.url || "";
  serverForm.username = wallet.server?.username || "";
  serverForm.password = "";
  serverForm.key = wallet.server?.key || "";
}

async function selectWallet(crypto) {
  state.selectedCrypto = crypto;
  window.history.pushState({}, "", `/wallet/${encodeURIComponent(crypto)}`);
  await runTask(() => loadWalletDetail(crypto));
}

async function saveWalletSettings() {
  await runTask(async () => {
    const crypto = state.selectedCrypto;
    await api(`/api/v1/${encodeURIComponent(crypto)}/payment-gateway`, { method: "POST", body: { enabled: walletForm.enabled } });
    await api(`/api/v1/${encodeURIComponent(crypto)}/autopayout`, {
      method: "POST",
      body: {
        policyStatus: walletForm.autopayout_enabled,
        policy: walletForm.autopayout_policy,
        policyValue: walletForm.autopayout_condition,
        add: walletForm.autopayout_destination,
        fee: walletForm.autopayout_fee,
        prespolicyOption: walletForm.reserve_policy,
        prespolicyValue: walletForm.reserve_amount,
        partiallPaid: walletForm.partial_paid_percent,
        addedFee: walletForm.overpaid_percent,
        recalc: walletForm.recalculate_after,
        confirationNum: walletForm.confirmations
      }
    });
    await loadWallets();
  }, "钱包策略已保存");
}

async function syncApiKey() {
  if (!walletForm.api_key.trim()) {
    notify("API Key 不能为空", "error");
    return;
  }
  await runTask(async () => {
    await api(`/api/v1/${encodeURIComponent(state.selectedCrypto)}/payment-gateway/token`, {
      method: "POST",
      body: { token: walletForm.api_key.trim() }
    });
    await loadWallets();
  }, "API Key 已同步");
}

function generateToken() {
  const bytes = new Uint8Array(32);
  window.crypto.getRandomValues(bytes);
  walletForm.api_key = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function saveServer() {
  await runTask(async () => {
    const crypto = encodeURIComponent(state.selectedCrypto);
    if (serverForm.host.trim()) {
      await api(`/api/v1/${crypto}/server/host`, { method: "POST", body: { host: serverForm.host.trim() } });
    }
    if (serverForm.key.trim() || serverForm.username.trim() || serverForm.password.trim()) {
      await api(`/api/v1/${crypto}/server/key`, {
        method: "POST",
        body: {
          key: serverForm.key.trim(),
          username: serverForm.username.trim(),
          password: serverForm.password
        }
      });
    }
    await loadWalletDetail(state.selectedCrypto);
  }, "节点配置已保存");
}

function downloadWalletBackup(includePrivateKey = false) {
  if (!state.selectedCrypto) return;
  const crypto = encodeURIComponent(state.selectedCrypto);
  const query = includePrivateKey ? "?include_private_key=1" : "";
  window.location.href = `/api/v1/${crypto}/backup${query}`;
}

async function addDestination() {
  if (!destinationForm.addr.trim()) {
    notify("地址不能为空", "error");
    return;
  }
  await runTask(async () => {
    await api(`/api/v1/${encodeURIComponent(state.selectedCrypto)}/payout_destinations`, {
      method: "POST",
      body: { action: "add", daddress: destinationForm.addr.trim(), comment: destinationForm.comment.trim() }
    });
    destinationForm.addr = "";
    destinationForm.comment = "";
    await loadWalletDetail(state.selectedCrypto);
  }, "提现地址已添加");
}

async function deleteDestination(addr) {
  await runTask(async () => {
    await api(`/api/v1/${encodeURIComponent(state.selectedCrypto)}/payout_destinations`, {
      method: "POST",
      body: { action: "delete", daddress: addr }
    });
    await loadWalletDetail(state.selectedCrypto);
  }, "提现地址已删除");
}

async function loadRates() {
  const data = await api(`/api/v1/admin/rates?${queryString({ fiat: state.selectedFiat })}`);
  rates.value = (data.rates || []).map((item) => ({ ...item }));
}

async function selectFiat(fiat) {
  state.selectedFiat = fiat;
  window.history.pushState({}, "", `/rates/${encodeURIComponent(fiat)}`);
  await runTask(loadRates);
}

function applyBulkRates() {
  rates.value = rates.value.map((rate) => ({
    ...rate,
    source: bulk.source || rate.source,
    fee: bulk.fee !== "" ? bulk.fee : rate.fee,
    fee_policy: bulk.fee_policy || rate.fee_policy
  }));
}

async function saveRates() {
  await runTask(async () => {
    await api("/api/v1/admin/rates", { method: "POST", body: { fiat: state.selectedFiat, rates: rates.value } });
    await loadRates();
  }, "汇率设置已保存");
}

async function loadOrders(append = false) {
  await runTask(async () => {
    const params = {
      ...orderFilters,
      cursor: append ? state.nextOrderCursor : "",
      limit: orderFilters.limit || 30
    };
    const data = await api(`/api/v1/admin/orders?${queryString(params)}`);
    orders.value = append ? orders.value.concat(data.orders || []) : (data.orders || []);
    state.nextOrderCursor = data.next_cursor || "";
  });
}

function toggleOrder(id) {
  if (expandedOrders.has(id)) expandedOrders.delete(id);
  else expandedOrders.add(id);
}

async function loadPayouts() {
  await runTask(async () => {
    const data = await api(`/api/v1/admin/payouts?${queryString(payoutFilters)}`);
    payouts.value = data.payouts || [];
  });
}

let payoutQuoteRequest = 0;
let payoutQuoteAbort = null;
async function loadPayoutQuote() {
  const crypto = payoutForm.crypto;
  if (!crypto) {
    payoutQuoteAbort?.abort();
    payoutQuoteAbort = null;
    payoutQuote.value = null;
    payoutQuoteError.value = "";
    payoutQuoteLoading.value = false;
    return;
  }
  payoutQuoteAbort?.abort();
  const controller = new AbortController();
  payoutQuoteAbort = controller;
  const request = ++payoutQuoteRequest;
  payoutQuoteLoading.value = true;
  payoutQuoteError.value = "";
  try {
    const data = await api(`/api/v1/admin/payout-quote?${queryString({
      crypto,
      amount: payoutForm.amount || "0",
      address: payoutForm.destination
    })}`, { signal: controller.signal });
    if (controller.signal.aborted || request !== payoutQuoteRequest) return;
    payoutQuote.value = data;
    if (data.balance_error) payoutQuoteError.value = data.balance_error;
    else if (data.fee_error) payoutQuoteError.value = data.fee_error;
  } catch (error) {
    if (controller.signal.aborted || error.name === "AbortError") return;
    if (request === payoutQuoteRequest) {
      payoutQuoteError.value = error.message || "余额和手续费计算失败";
      payoutQuote.value = null;
    }
  } finally {
    if (payoutQuoteAbort === controller) payoutQuoteAbort = null;
    if (request === payoutQuoteRequest) payoutQuoteLoading.value = false;
  }
}

async function createPayout() {
  if (!payoutForm.crypto || !payoutForm.destination || !payoutForm.amount) {
    notify("币种、地址和金额不能为空", "error");
    return;
  }
  if (payoutAmountExceedsSingleAccount.value) {
    notify("金额超过单地址可提现余额，请降低金额或先归集资金", "error");
    return;
  }
  if (payoutQuoteUnavailable.value || payoutQuoteError.value) {
    notify(payoutQuoteError.value || payoutQuoteCacheHint.value || "当前币种暂不可提现", "error");
    return;
  }
  await runTask(async () => {
    await api(`/api/v1/${encodeURIComponent(payoutForm.crypto)}/payout`, {
      method: "POST",
      body: {
        destination: payoutForm.destination,
        amount: payoutForm.amount,
        external_id: payoutForm.external_id,
        callback_url: payoutForm.callback_url
      }
    });
    payoutQuoteAbort?.abort();
    payoutQuoteAbort = null;
    payoutQuoteRequest++;
    payoutForm.amount = "";
    payoutForm.destination = "";
    payoutForm.external_id = "";
    payoutForm.callback_url = "";
    await loadPayouts();
    payoutQuote.value = null;
    payoutQuoteError.value = "";
  }, "提现已发起");
}

async function saveSettings() {
  await runTask(async () => {
    await api("/api/v1/admin/account", { method: "PATCH", body: settingsForm });
    settingsForm.current_password = "";
    settingsForm.new_password = "";
    settingsForm.confirm_password = "";
  }, "管理员账户已更新");
}

async function refreshCurrent() {
  if (state.view === "wallets") await runTask(loadWallets);
  if (state.view === "rates") await runTask(loadRates);
  if (state.view === "orders") {
    if (!wallets.value.length) await loadWallets();
    await loadOrders(false);
  }
  if (state.view === "payouts") {
    if (!wallets.value.length) await loadWallets();
    await loadPayouts();
    await loadPayoutQuote();
  }
  if (state.view === "settings" && !wallets.value.length) {
    await runTask(() => loadWallets({ includeDetail: false }));
  }
}

function walletStatusText(wallet) {
  if (!wallet) return "未知";
  if (wallet.wallet_error) return "配置缺失";
  if (wallet.balance_error && Object.keys(wallet.balance_error).length) return "余额异常";
  if (typeof wallet.status === "string") return wallet.status || "未知";
  if (wallet.status?.status) return wallet.status.status;
  if (wallet.status?.available === false || wallet.status?.ok === false) return "离线";
  if (wallet.status?.available === true || wallet.status?.ok === true) return "在线";
  return wallet.enabled ? "已启用" : "已停用";
}

function statusTone(wallet) {
  const text = walletStatusText(wallet).toLowerCase();
  if (text.includes("online") || text.includes("ready") || text.includes("success") || text.includes("在线") || text.includes("启用")) return "text-ok";
  if (text.includes("error") || text.includes("fail") || text.includes("offline") || text.includes("异常") || text.includes("缺失")) return "text-bad";
  return "text-warn";
}

function payoutTone(status = "") {
  if (status === "SUCCESS") return "ok";
  if (status === "FAIL") return "bad";
  return "warn";
}

function orderTone(order) {
  const summary = orderSummary(order);
  if (summary.includes("PAID") || summary.includes("SUCCESS")) return "ok";
  if (summary.includes("FAIL") || summary.includes("CANCELLED") || summary.includes("REFUNDED")) return "bad";
  return "warn";
}

function orderSummary(order) {
  const statuses = new Set();
  if (order.status) statuses.add(order.status);
  if (order.invoice_status) statuses.add(order.invoice_status);
  if (order.payout_status) statuses.add(order.payout_status);
  (order.invoices || []).forEach((item) => statuses.add(item.status));
  (order.payouts || []).forEach((item) => statuses.add(item.status));
  return Array.from(statuses).filter(Boolean).join(" / ") || "EMPTY";
}

function orderUpdatedAt(order) {
  const dates = [];
  (order.invoices || []).forEach((item) => dates.push(item.updated_at || item.created_at));
  (order.payouts || []).forEach((item) => dates.push(item.updated_at || item.created_at));
  return formatTime(dates.sort().pop());
}

function formatAmount(value) {
  if (value === undefined || value === null || value === "") return "-";
  const text = String(value);
  if (text.length > 18) return Number(text).toLocaleString(undefined, { maximumFractionDigits: 8 });
  return text;
}

function decimalGreaterThan(left, right) {
  const a = Number(left);
  const b = Number(right);
  if (!Number.isFinite(a) || !Number.isFinite(b)) return false;
  return a > b;
}

function formatTime(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

onMounted(async () => {
  applyRoute();
  window.addEventListener("popstate", () => {
    applyRoute();
    refreshCurrent();
  });
  await runTask(async () => {
    await loadBootstrap();
    await loadWallets();
    if (state.view === "rates") await loadRates();
    if (state.view === "orders") await loadOrders(false);
    if (state.view === "payouts") {
      await loadPayouts();
      await loadPayoutQuote();
    }
  });
});

let payoutQuoteTimer = 0;
watch(
  () => [payoutForm.crypto, payoutForm.amount, payoutForm.destination],
  () => {
    window.clearTimeout(payoutQuoteTimer);
    if (state.view !== "payouts") return;
    payoutQuoteTimer = window.setTimeout(loadPayoutQuote, 650);
  }
);
</script>
