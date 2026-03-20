# Paper Mode — Full Test Report
**Date:** 2026-03-21
**Mode:** Paper (in-memory, no database required)
**Build:** `go build ./cmd/agent` — 0 errors, 0 warnings

---

## Test Summary — 8 Packages, 57 Test Cases

| Package | Tests | Status | Duration |
|---|---|---|---|
| `internal/backtest` | 8 | ✅ PASS | 0.72s |
| `internal/config` | 8 | ✅ PASS | 0.62s |
| `internal/ml` | 19 | ✅ PASS | 0.72s |
| `internal/reliability` | 10 | ✅ PASS | 5.50s |
| `internal/risk` | 14 | ✅ PASS | 0.68s |
| `internal/trading` | 7 | ✅ PASS | 0.75s |
| `internal/withdraw` | 7 | ✅ PASS | 0.81s |
| `test/integration` | 12 | ✅ PASS | 3.43s |

**Total: 57 / 57 PASS — 0 FAIL**

---

## Integration Paper Pipeline Report

Skenario: **Trending Market** — harga YES bergerak dari $0.40 → $0.70 selama 30 snapshot (15 menit history).

```
Initial balance  : $10.00
Final balance    : $11.49
Total PnL        : +$1.49  (+14.88%)
Total trades     : 1  (Win: 1 / Loss: 0)
Win rate         : 100%
Max drawdown     : 0.00%
```

> Hanya 1 trade terjadi karena pipeline mensyaratkan minimum 20 snapshot sebelum signal dihasilkan.
> Signal muncul setelah data history cukup, kemudian order dieksekusi dengan slippage dan gas cost.

---

## Paper Executor Simulation Report

Dari `TestPaperExecutor_DefaultConfig` — 100 iterasi order BUY @ $0.65, qty 15:

| Perilaku | Konfigurasi | Hasil |
|---|---|---|
| Slippage | max 2% | Fill price selalu ≥ requested (buy), max +2% |
| Gas cost | $0.02/order | Konsisten $0.02 setiap order |
| Partial fill rate | 20% probability | ~20 dari 100 order di-fill sebagian |
| Partial fill ratio | min 50% | FilledQty selalu 50–99% dari requested |
| Status tracking | — | `filled` / `partially_filled` tercatat benar di order |

### Contoh Output Order:
```
[PAPER] order simulated
  requested_price = 0.6500
  fill_price      = 0.6613   (+1.74% slippage)
  requested_qty   = 15.0000
  filled_qty      = 9.3421   (partial fill: 62.3%)
  fee_cost_usd    = 0.0200
  status          = partially_filled
```

---

## ML Predictor Report

### Feature Extraction (7 Features)

| Feature | Test Skenario | Hasil |
|---|---|---|
| `PriceSlope` | Uptrend 0.30→0.55 | `> 0` ✅ |
| `PriceSlope` | Downtrend 0.70→0.45 | `< 0` ✅ |
| `PriceMomentum` | Uptrend | `> 1.0` ✅ |
| `PriceMomentum` | Downtrend | `< 1.0` ✅ |
| `VolumeAccel` | Per-period delta meningkat | `> 1.0` ✅ |
| `BidAskImbalance` | bid_depth >> ask_depth | `> 1.0` ✅ |
| `TimeToExpiry` | Market 4 jam lagi | `≈ 4.0h` ✅ |

### Composite Scoring Weights

| Signal | Weight | Keterangan |
|---|---|---|
| Price Momentum | 25% | Trend continuation |
| Price Slope | 20% | Trend direction & speed |
| Volume Acceleration | 15% | Activity confirmation |
| Bid/Ask Imbalance | 15% | Order book pressure |
| Mean Reversion | 10% | Contrarian value |
| Liquidity Trend | 10% | Market health |
| Spread Penalty | 5% | Cost drag reduction |

### Expiry Penalty

| Sisa Waktu | Behavior | Hasil |
|---|---|---|
| < 6 jam | Confidence di-cap ≤ 0.55 | ✅ Verified |
| 6–24 jam | Gradual cap (linear) | ✅ Verified |
| > 72 jam | Tidak ada penalty | ✅ Verified |

---

## Circuit Breaker Report

| Skenario | State Transition | Hasil |
|---|---|---|
| State awal | `CLOSED` | ✅ |
| 3 consecutive failures | `CLOSED` → `OPEN` | ✅ |
| Cooldown elapsed | `OPEN` → `HALF_OPEN` | ✅ |
| 1 success di HALF_OPEN | `HALF_OPEN` → `CLOSED` | ✅ |
| Failure di HALF_OPEN | `HALF_OPEN` → `OPEN` | ✅ |
| Rapid balance drop > 10% | Auto trip → `OPEN` | ✅ |
| Balance drop ≤ 10% | Tetap `CLOSED` | ✅ |

---

## Risk Manager Report

| Rule | Threshold | Skenario | Hasil |
|---|---|---|---|
| Stop-loss trigger | ≥ 15% loss | Entry $0.60 → Current $0.50 (-16.7%) | ✅ Triggered |
| Stop-loss no trigger | < 15% loss | Entry $0.60 → Current $0.55 (-8.3%) | ✅ Not triggered |
| Profitable position | — | Entry $0.40 → Current $0.70 (+75%) | ✅ Not triggered |
| Daily loss limit | 5% portfolio | Loss $0.51 dari portfolio $10 | ✅ Trading dihentikan |
| Order split | > $100 value | Order $150 → 5 chunks @ $30 | ✅ Split benar |

---

## Auto-Withdraw Compounding Report

Berdasarkan 25% compounding plan — initial balance $10:

| Hari | Expected Balance |
|---|---|
| Day 1 | $10.00 |
| Day 10 | $11.85 |
| Day 30 | $16.60 |

> 25% profit harian ditahan di hot wallet, 75% di-withdraw ke cold wallet.
> Auto-withdraw hanya berjalan di live mode, tidak aktif di paper mode.

---

## Data Pipeline Fixes (Applied)

Perbaikan yang sudah diverifikasi berjalan benar:

| Fix | Sebelum | Sesudah |
|---|---|---|
| MarketID pada snapshot | UUID zero (data tidak terhubung) | UUID dari DB via `RETURNING id` |
| Volume feature | Cumulative (tidak berguna) | Per-period delta antar snapshot |
| Order book depth | Tidak dipakai ML | `bid_depth`, `ask_depth`, `spread` masuk DB & ML |
| Minimum snapshots | 5 (2.5 menit) | 20 (10 menit history) |
| TimeToExpiry | Dihitung tapi tidak dipakai | Masuk scoring + expiry penalty |

---

## Infrastructure Status

| Komponen | Status |
|---|---|
| Go module | ✅ `github.com/x10rst/ai-agent-autonom` |
| Build binary | ✅ `agent.exe` (35MB) |
| config.yaml | ✅ Polygon mainnet, paper mode default |
| .env.example | ✅ Semua secrets terdokumentasi |
| Dockerfile | ✅ Multi-stage build, alpine runtime |
| docker-compose.yml | ✅ PostgreSQL 16 + agent service |
| DB Migrations | ✅ 3 migration files (001, 002, 003) |
| Chain target | ✅ Polygon (chain_id: 137) — Polymarket native |

---

## Cara Menjalankan

### Paper Mode (dengan Docker)
```bash
cp .env.example .env
# Edit .env: set AGENT_DATABASE_PASSWORD

docker compose up --build
# Agent berjalan di http://localhost:8080
# Paper mode aktif by default (AGENT_TRADING_MODE=paper)
```

### Paper Mode (manual, PostgreSQL sudah running)
```bash
./agent --config config.yaml --log-level debug
```

### Live Mode (Polygon)
```bash
# Pastikan wallet punya MATIC (gas) + USDC di Polygon
export AGENT_TRADING_MODE=live
export AGENT_BLOCKCHAIN_PRIVATE_KEY=0x...
export AGENT_BLOCKCHAIN_RPC_URL=https://polygon-rpc.com
export AGENT_AUTO_WITHDRAW_SAFE_WALLET_ADDRESS=0x...
export AGENT_NOTIFICATION_TELEGRAM_BOT_TOKEN=...
export AGENT_NOTIFICATION_TELEGRAM_CHAT_ID=...

docker compose up --build
```

---

## Catatan

- Paper mode **tidak memerlukan** private key, RPC URL, atau MATIC.
- Agent baru akan menghasilkan signal setelah **10 menit pertama** data terkumpul (20 snapshots minimum).
- Di live mode, agent perlu koneksi ke Polymarket CLOB API yang stabil.
- Sharpe ratio dan profit factor menunjukkan `0.000` pada 1 trade — metrik ini baru bermakna setelah ≥ 30 trade.
