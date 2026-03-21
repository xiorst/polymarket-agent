# Live Mode Requirements — Polymarket Agent
**Dibuat:** 2026-03-20  
**Status:** Work in progress — semua item di bawah harus selesai sebelum `AGENT_TRADING_MODE=live`

---

## 🔴 CRITICAL — Wajib Fix (akan menyebabkan transaksi revert/gagal jika diabaikan)

### 1. ~~ABI Encoding `BuildOrderCalldata` — Placeholder~~ ✅ DONE
**File:** `internal/market/polymarket/contract.go`

ABI JSON embed dari Polygonscan, encoding via `go-ethereum/accounts/abi`. Paper mode (unsigned) dan live mode (signed) sudah dipisah dengan benar.

---

### 2. ~~EIP-712 Order Signing — Belum Diimplementasi~~ ✅ DONE
**File:** `internal/market/polymarket/eip712.go`

`OrderSigner` + `CTFOrder` struct implementasi lengkap. Domain separator, `hashOrder`, signature v=27/28 — semua benar.

---

### 3. ~~`BuildCancelCalldata` — Placeholder Selector~~ ✅ DONE
**File:** `internal/market/polymarket/contract.go`

`cancelOrder` sekarang pakai ABI pack yang benar. `BuildCancelCalldataFromOrder` tersedia untuk cancel dengan full CTFOrder struct dari DB.

---

### 4. Stop-Loss Current Price — Placeholder
**File:** `internal/trading/engine.go` (line 281–282)

**Masalah:**  
```go
// TODO: fetch current prices from market provider
currentPrices := make(map[string]decimal.Decimal) // selalu empty map
```
Stop-loss tidak akan pernah trigger di live karena current prices selalu kosong.

**Yang harus dilakukan:**
- Wire `marketProvider.FetchMarketSnapshot()` ke dalam `monitorStopLoss()`
- Fetch harga terkini untuk setiap open position sebelum `CheckStopLoss()` dipanggil

---

### 5. Liquidity Monitor — Market IDs Hardcoded Kosong
**File:** `cmd/agent/main.go` (line 177)

**Masalah:**  
```go
// Return active market IDs — placeholder, in production query from DB
return []string{}
```
Liquidity monitor tidak akan memonitor market apapun.

**Yang harus dilakukan:**
- Query active market IDs dari database
- Return list real market IDs ke liquidity monitor

---

## 🟡 PENTING — Konfigurasi & Secrets

### 6. Isi `.env` untuk Live Mode
Copy `.env.example` ke `.env` dan isi semua value berikut:

| Variable | Keterangan | Status |
|----------|-----------|--------|
| `AGENT_TRADING_MODE` | Set ke `live` | ⬜ |
| `AGENT_BLOCKCHAIN_PRIVATE_KEY` | Private key wallet Polygon (ETH wallet kamu) | ⬜ |
| `AGENT_BLOCKCHAIN_RPC_URL` | Alchemy Polygon endpoint (lebih reliable dari public) | ⬜ |
| `AGENT_BLOCKCHAIN_POLYMARKET_API_KEY` | API key dari Polymarket (daftar di polymarket.com) | ⬜ |
| `AGENT_AUTO_WITHDRAW_SAFE_WALLET_ADDRESS` | Cold wallet address untuk auto-withdraw profit | ⬜ |
| `AGENT_NOTIFICATION_TELEGRAM_BOT_TOKEN` | Bot token Telegram untuk notifikasi | ⬜ |
| `AGENT_NOTIFICATION_TELEGRAM_CHAT_ID` | Chat ID Telegram kamu | ⬜ |
| `AGENT_DATABASE_PASSWORD` | Password PostgreSQL | ⬜ |
| `AGENT_SERVER_API_KEY` | Random key untuk protect REST API `/api/v1/*` | ⬜ |

---

### 7. Polymarket API Key & CLOB Auth
**Masalah:** Polymarket CLOB API butuh autentikasi khusus — bukan sekadar API key biasa.

**Yang harus dilakukan:**
- Daftar di https://polymarket.com dan connect wallet
- Generate L1 API key via signature dari wallet (bukan signup biasa)
- Dokumentasi: https://docs.polymarket.com/#authentication
- Simpan key di `.env` sebagai `AGENT_BLOCKCHAIN_POLYMARKET_API_KEY`

---

### 8. Wallet Harus Ada USDC + MATIC di Polygon
**Masalah:** Wallet ETH kamu (`0x0946faC36B0A6B7EBE88B35fCc1E241812995957`) perlu:

| Asset | Kebutuhan | Fungsi |
|-------|----------|--------|
| USDC (Polygon) | Modal trading — rekomendasi awal: $20–50 | Order placement |
| MATIC | Minimal $5–10 | Gas fee setiap transaksi |

**Catatan:** USDC di Polygon berbeda dengan USDC di Ethereum mainnet. Bridge dulu jika perlu via https://wallet.polygon.technology

---

## 🟢 KONFIGURASI — Perlu Di-review Sebelum Live

### 9. Review Risk Parameters di `config.yaml`
Parameter saat ini di-set untuk balance $10. Sesuaikan jika modal berbeda:

| Parameter | Nilai Saat Ini | Keterangan |
|-----------|---------------|-----------|
| `risk.stop_loss` | 5% | Stop-loss per posisi |
| `risk.max_position_per_market` | 20% | Max per market |
| `risk.max_total_exposure` | 70% | Max total exposure |
| `risk.daily_loss_limit` | 5% | Halt jika loss 5%/hari |
| `trading.initial_balance` | $10.00 | Sesuaikan dengan modal nyata |
| `trading.daily_profit_target` | 25% | Target harian — realistis? |
| `market_analysis.confidence_threshold` | 0.65 | Min confidence untuk entry |
| `market_analysis.mispricing_threshold` | 0.05 | Min edge untuk entry |

---

### 10. Verifikasi Contract Address Masih Aktif
Sebelum live, cek dua address ini masih valid di Polygon mainnet:

| Contract | Address | Check |
|----------|---------|-------|
| CTF Exchange | `0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E` | https://polygonscan.com/address/0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E |
| USDC (PoS) | `0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174` | https://polygonscan.com/address/0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174 |

---

## 📋 CHECKLIST RINGKAS

```
CRITICAL (code fix):
[x] 1. ABI encoding + abigen binding untuk fillOrder ✅
[x] 2. EIP-712 order signing ✅
[x] 3. Verifikasi selector cancelOrder ✅
[ ] 4. Wire current prices ke stop-loss monitor
[ ] 5. Wire DB market IDs ke liquidity monitor

KONFIGURASI:
[ ] 6. .env terisi semua (9 variables)
[ ] 7. Polymarket CLOB API key didapat
[ ] 8. Wallet ada USDC + MATIC di Polygon

REVIEW:
[ ] 9.  Risk parameters sesuai modal
[ ] 10. Contract addresses masih aktif

EXTERNAL CONTEXT (Telegram Feed):
[x] 11. Dapat api_id + api_hash dari second account ✅
[x] 11. Second account sudah joined marketfeed + channel relevan ✅
[ ] 11. Implementasi package internal/feeds/telegram/ dengan gotd/td
[ ] 11. Implementasi keyword extraction + sentiment scoring
[ ] 11. Wire ExternalSignal ke FeatureSet ML predictor
[ ] 11. Test userbot read-only sebelum connect ke live pipeline

REKOMENDASI TAMBAHAN:
[ ] Test dengan modal minimal ($20) selama 2 minggu sebelum naikkan
[ ] Jalankan paper mode paralel selain live untuk benchmark
[ ] Pastikan Telegram notifikasi berfungsi sebelum first live trade
```

---

## 🔵 EXTERNAL CONTEXT — Telegram Feed Integration

### 11. Telegram Userbot — Monitor Channel Eksternal
**Package baru:** `internal/feeds/telegram/`

**Tujuan:**  
Memberi polymarket-agent akses ke informasi dunia luar (berita, geopolitik, olahraga) sebagai input tambahan untuk ML predictor — saat ini predictor hanya melihat data internal Polymarket.

**Kenapa userbot (bukan bot biasa):**  
Bot Telegram biasa tidak bisa join/baca channel yang tidak dikontrol sendiri. Userbot login sebagai akun Telegram → bisa baca semua channel yang sudah di-joined.

**Yang dibutuhkan:**
- `api_id` + `api_hash` dari **second account Telegram** (bukan main account — demi keamanan)
  - Daftar di: https://my.telegram.org/apps
  - Login dengan nomor second account → Create application → catat `api_id` (angka) + `api_hash` (string)
- Second account harus sudah **joined** ke channel yang mau dimonitor sebelum userbot dijalankan
- Nomor HP second account untuk auth session pertama kali

**Channel yang akan dimonitor (awal):**
- `t.me/marketfeed` — Market News Feed (geopolitik, ekonomi, berita dunia)
- Channel lain bisa ditambah via `config.yaml`

**Library Go:** `github.com/gotd/td` (MTProto client)

**Alur implementasi:**
```
Channel Telegram (marketfeed, dll)
        ↓
  gotd/td userbot (read-only)
        ↓
  extract keywords + sentiment scoring
  (rule-based atau LLM)
        ↓
  map ke kategori Polymarket market
  (crypto, politik, olahraga, geopolitik)
        ↓
  inject sebagai ExternalSignal ke FeatureSet ML
        ↓
  predictor combine internal + external score
```

**Variables baru di `.env`:**
```
AGENT_TELEGRAM_FEED_API_ID=12345678
AGENT_TELEGRAM_FEED_API_HASH=your_api_hash_here
AGENT_TELEGRAM_FEED_PHONE=+628xxxxxxxxxx
AGENT_TELEGRAM_FEED_CHANNELS=marketfeed,channel2,channel3
AGENT_TELEGRAM_FEED_SESSION_FILE=./data/telegram_session.json
```

**Tips anti-suspend:**
- Set poll interval minimal 1–2 menit (tidak perlu agresif)
- Userbot hanya read-only — tidak mengirim pesan apapun
- Jangan subscribe terlalu banyak channel sekaligus di awal
- Gunakan second account, bukan main account

**Status:** ⬜ Belum diimplementasi — tunggu `api_id` + `api_hash` dari Irishdara

---

## ⚠️ Catatan Penting

- **Jangan live sebelum item 1–5 selesai** — transaksi akan revert dan buang gas fee
- **Start dengan modal kecil** — $20–50 USDC, bukan langsung besar
- **Predictor saat ini blind terhadap external context** (berita, geopolitik) — performa live mungkin berbeda dari paper test yang pakai trending scenario ideal
- **57/57 test pass** adalah fondasi yang bagus, tapi paper scenario masih sederhana

---

*File ini dibuat otomatis oleh Raka berdasarkan code review pada 2026-03-20.*  
*Update checklist ini setiap item selesai.*
