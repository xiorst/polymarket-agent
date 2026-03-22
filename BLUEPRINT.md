# Polymarket Agent — Blueprint

**Version:** 2026-03-22  
**Stack:** Go 1.26 · PostgreSQL 16 · Polygon (chain_id: 137)  
**Mode saat ini:** Paper (simulasi) → target: Live

---

## 1. Overview

Agent ini adalah sistem trading otomatis untuk **Polymarket** — prediction market berbasis Polygon blockchain. Agent menganalisis market, membuat prediksi menggunakan ML, lalu mengeksekusi order secara otomatis.

```
Dunia Luar (berita, geopolitik, olahraga)
        +
Polymarket (harga, volume, order book)
        ↓
  Analisis & Prediksi (ML)
        ↓
  Risk Check → Order Execution
        ↓
  Profit → Auto-Withdraw ke Cold Wallet
```

---

## 2. Arsitektur Komponen

```
┌─────────────────────────────────────────────────────────────┐
│                        cmd/agent                            │
│                    (main entry point)                       │
└──────────────────────────┬──────────────────────────────────┘
                           │ orchestrates
        ┌──────────────────┼──────────────────┐
        ▼                  ▼                  ▼
  [Data Layer]      [Analysis Layer]   [Execution Layer]
  
  market/           feeds/             trading/
  collector.go      telegram/          engine.go
  polymarket/       scorer/            live_executor.go
  provider.go                          paper_executor.go
  
  database/         ml/                risk/
  PostgreSQL        predictor.go       manager.go
                    features.go
                    pipeline.go        reliability/
                                       circuit_breaker.go
  
                                       withdraw/
                                       auto_withdraw.go
```

---

## 3. Full Flow — Step by Step

### FASE 1: Data Collection (setiap 30 detik)

```
Polymarket Gamma API
  GET /markets?active=true
        ↓
  market/collector.go
  → simpan ke DB: markets table
  → ambil snapshot (price, volume, liquidity, order book)
  → simpan ke DB: market_snapshots table
  → hitung volume_per_period (delta vs snapshot sebelumnya)
```

**Data yang dikumpulkan per snapshot:**
| Field | Sumber | Fungsi |
|-------|--------|--------|
| `outcome_prices` | Gamma API | Harga YES/NO per outcome |
| `volume_per_period` | Dihitung | Trading activity |
| `liquidity` | Gamma API | Kesehatan market |
| `bid_depth` / `ask_depth` | CLOB API | Tekanan beli vs jual |
| `spread` | CLOB API | Biaya transaksi |

---

### FASE 2: External Context (setiap 90 detik)

```
Telegram channel (marketfeed, dll)
        ↓
  feeds/telegram/feed.go
  → gotd/td userbot (second account, read-only)
  → ambil 20 pesan terbaru per channel
        ↓
  feeds/scorer/scorer.go
  → keyword matching
  → assign: Category + Sentiment + Confidence
        ↓
  ml/pipeline.IngestExternalSignal()
  → simpan di memory (max 30 menit, lalu stale)
```

**Kategori yang dideteksi:**
| Kategori | Contoh keyword | Contoh efek |
|----------|---------------|-------------|
| `crypto` | btc, ethereum, solana | Boost crypto market |
| `politics` | election, trump, vote | Boost politik |
| `geopolitics` | nuclear, airstrike, israel, iran | Bearish signal |
| `economy` | ecb, fed, rate cut, fomc | Bullish/bearish ekonomi |
| `sports` | championship, world cup | Boost sports market |

---

### FASE 3: ML Prediction (setiap 30 detik, per market aktif)

```
DB: ambil 100 snapshot terbaru per market
        ↓
  ml/features.go — ExtractFeatures()
  → hitung 8 fitur statistik + external signal
        ↓
  ml/predictor.go — score()
  → composite score [0, 1]
        ↓
  ml/pipeline.go — GenerateSignals()
  → Gate 1: confidence > threshold (0.65)
  → Gate 2: mispricing > threshold (0.05)
  → simpan ke DB: signals table
```

**8 Sinyal dalam composite score:**

| # | Sinyal | Bobot | Keterangan |
|---|--------|-------|-----------|
| 1 | Price Momentum | 22% | recent avg / older avg |
| 2 | Price Slope | 18% | linear regression trend |
| 3 | Volume Acceleration | 13% | trading activity naik/turun |
| 4 | Bid/Ask Imbalance | 14% | tekanan beli vs jual |
| 5 | Mean Reversion | 9% | harga jauh dari rata-rata |
| 6 | Liquidity Trend | 9% | market makin sehat/sakit |
| 7 | Spread Penalty | 5% | biaya transaksi |
| 8 | External Signal | 10% | berita dari Telegram feed |

**Formula External Signal:**
```
sentimentScore = (sentiment + 1.0) / 2    # [-1,+1] → [0,1]
externalSignal = 0.5 + (sentimentScore - 0.5) * confidence
```
- Bullish news (1.0) + confidence 0.8 → externalSignal = 0.90 → boost score
- Bearish news (-1.0) + confidence 0.8 → externalSignal = 0.10 → turunkan score
- Tidak ada berita → externalSignal = 0.5 → netral

**Expiry Penalty:**
| Sisa waktu market | Max confidence |
|-------------------|---------------|
| < 6 jam | 0.55 (terlalu berisiko) |
| 6–24 jam | Gradual cap |
| > 72 jam | Tidak ada penalty |

---

### FASE 4: Risk Check (sebelum setiap order)

```
Signal dari ML pipeline
        ↓
  risk/manager.go — PreTradeCheck()
  → Cek daily loss limit (5% portfolio)
  → Cek position size per market (max 20%)
  → Cek total exposure (max 70%)
  → Cek slippage tolerance (max 5%)
  → Cek price impact (> 3% → split order)
        ↓
  reliability/circuit_breaker.go
  → CLOSED (normal) → lanjut
  → OPEN (halt) → skip semua order
  → HALF_OPEN → coba 1 order test
```

**Circuit Breaker States:**
```
CLOSED ──(3 consecutive failures)──→ OPEN
OPEN   ──(10 menit cooldown)──────→ HALF_OPEN
HALF_OPEN ──(success)────────────→ CLOSED
HALF_OPEN ──(failure)────────────→ OPEN

CLOSED ──(balance drop >10% / 5 menit)──→ OPEN (emergency)
```

---

### FASE 5: Order Execution

**Paper Mode:**
```
trading/paper_executor.go
→ simulasi slippage (max 2%)
→ simulasi partial fill (20% probability)
→ simulasi gas cost ($0.02)
→ catat di DB (orders table)
→ update paper balance
```

**Live Mode:**
```
trading/live_executor.go
→ market/polymarket/contract.go
  → BuildSignedOrder() — EIP-712 signing
  → encodefillOrder() — ABI pack
→ blockchain/client.go
  → SendTransaction() ke Polygon
  → WaitForReceipt() — konfirmasi on-chain
→ Catat TX hash di DB (pending_transactions)
```

**Order flow detail (Live):**
```
1. Cek USDC allowance → approve jika kurang
2. Resolve market → CTF Exchange address
3. Build CTFOrder struct
4. Sign dengan EIP-712 (wallet private key)
5. Pack calldata → fillOrder(order, fillAmount, sig)
6. Broadcast ke Polygon via Alchemy RPC
7. Tunggu receipt (timeout 3 menit)
8. Jika stuck → replace TX dengan gas lebih tinggi (x1.3)
```

---

### FASE 6: Position Monitoring (setiap trading cycle)

```
DB: ambil semua open positions
        ↓
  risk/manager.go — CheckStopLoss()
  → fetch harga terkini dari Polymarket API
  → hitung P&L per posisi
  → Stop-loss: -15% per posisi → close otomatis
  → Daily loss: -5% portfolio → halt trading hari ini
        ↓
  market/liquidity_monitor.go
  → pantau spread setiap 15 detik
  → spread > 2x normal → warning
  → spread > 4x normal → halt eksekusi
```

---

### FASE 7: Resolution & Profit

```
resolution/handler.go (setiap 5 menit)
→ cek market yang sudah expired
→ claim winning positions
→ update balance
        ↓
withdraw/auto_withdraw.go (setiap 5 menit, live only)
→ jika balance > $10 (min hot wallet)
→ transfer profit ke cold wallet (safe address)
→ compounding plan: 25% profit ditahan, 75% di-withdraw
```

---

### FASE 8: Notification

```
notification/telegram.go
→ kirim alert ke Irishdara via Telegram bot
→ Events yang di-alert:
  - Agent started/stopped
  - Order placed/filled
  - Stop-loss triggered
  - Circuit breaker tripped
  - TX stuck
  - Profit withdrawn
  - Daily loss limit hit
```

---

## 4. Database Schema

```
markets              — daftar market aktif dari Polymarket
market_snapshots     — data harga/volume setiap 30 detik
signals              — sinyal trading dari ML pipeline
orders               — order yang sudah dikirim (paper/live)
positions            — open positions saat ini
pending_transactions — TX on-chain yang belum konfirmasi
```

---

## 5. Konfigurasi Utama (`config.yaml`)

| Parameter | Nilai | Keterangan |
|-----------|-------|-----------|
| `trading.mode` | `paper` / `live` | Mode eksekusi |
| `trading.initial_balance` | `$10` | Modal awal |
| `market_analysis.confidence_threshold` | `0.65` | Min ML score untuk entry |
| `market_analysis.mispricing_threshold` | `0.05` | Min edge vs market price |
| `risk.stop_loss` | `5%` | Stop-loss per posisi |
| `risk.daily_loss_limit` | `5%` | Halt jika loss 5%/hari |
| `risk.max_position_per_market` | `20%` | Max per market |
| `risk.max_total_exposure` | `70%` | Max total exposure |
| `circuit_breaker.rapid_drop_threshold` | `10%` | Emergency halt threshold |
| `telegram_feed.poll_interval_seconds` | `90` | Interval baca Telegram |

---

## 6. Deployment

```
docker compose up --build
  ├── postgres:16        — database
  └── agent              — trading agent

# Environment variables (.env):
AGENT_TRADING_MODE=live
AGENT_BLOCKCHAIN_PRIVATE_KEY=0x...
AGENT_BLOCKCHAIN_RPC_URL=https://polygon-mainnet.g.alchemy.com/v2/...
AGENT_BLOCKCHAIN_POLYMARKET_API_KEY=...
AGENT_AUTO_WITHDRAW_SAFE_WALLET_ADDRESS=0x...
AGENT_NOTIFICATION_TELEGRAM_BOT_TOKEN=...
AGENT_NOTIFICATION_TELEGRAM_CHAT_ID=...
AGENT_TELEGRAM_FEED_API_ID=...
AGENT_TELEGRAM_FEED_API_HASH=...
AGENT_TELEGRAM_FEED_PHONE=+62...
AGENT_TELEGRAM_FEED_ENABLED=true
```

---

## 7. Status & Sisa Pekerjaan

### ✅ Selesai
- Market data collection (Gamma API + CLOB API)
- ML predictor: 7 sinyal statistik + 1 external signal
- Telegram feed: userbot read-only, scorer keyword-based
- EIP-712 order signing (production-ready)
- ABI encoding `fillOrder` / `cancelOrder`
- Paper executor: slippage, partial fill, gas simulation
- Circuit breaker: state machine lengkap
- Risk manager: stop-loss, daily limit, position sizing, order split
- Auto-withdraw: compounding plan
- Auth Telegram second account: session tersimpan

### ❌ Belum Selesai (sebelum live)
- **Fix #4:** Wire current prices ke stop-loss monitor (`engine.go` line 281)
- **Token ID resolution:** `tokenID` masih `0` di `BuildOrderCalldata` — perlu fetch dari CLOB API
- **Setup Docker PostgreSQL** + jalankan agent penuh
- **Isi .env live credentials** (private key, Polymarket API key)
- **Top-up USDC + MATIC** di Polygon wallet

---

## 8. Estimasi Timeline ke Live

| Step | Estimasi |
|------|---------|
| Fix stop-loss prices (#4) | ~1 jam |
| Token ID resolution | ~2 jam |
| Docker setup + first run | ~30 menit |
| Isi credentials + test paper penuh | ~1 jam |
| Top-up USDC + switch live | Kapanpun siap |

---

*Dibuat: 2026-03-22 — update setiap ada perubahan signifikan*
