package blockchain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/x10rst/ai-agent-autonom/internal/config"
)

type Client struct {
	eth        *ethclient.Client
	cfg        config.BlockchainConfig
	privateKey *ecdsa.PrivateKey
	address    common.Address
	chainID    *big.Int

	mu          sync.Mutex
	localNonce  uint64
	nonceLoaded bool
}

// NewClient creates a blockchain client connected to Polygon (where Polymarket operates).
func NewClient(ctx context.Context, cfg config.BlockchainConfig) (*Client, error) {
	eth, err := ethclient.DialContext(ctx, cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial rpc: %w", err)
	}

	c := &Client{
		eth:     eth,
		cfg:     cfg,
		chainID: big.NewInt(cfg.ChainID),
	}

	// Parse private key if provided (not needed in paper mode)
	if cfg.PrivateKey != "" {
		pk := strings.TrimPrefix(cfg.PrivateKey, "0x")
		privKey, err := crypto.HexToECDSA(pk)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		c.privateKey = privKey
		c.address = crypto.PubkeyToAddress(privKey.PublicKey)
		slog.Info("blockchain client initialized", "address", c.address.Hex())
	}

	return c, nil
}

// Address returns the wallet address.
func (c *Client) Address() common.Address {
	return c.address
}

// PrivateKey returns the ECDSA private key for EIP-712 order signing.
// Returns nil in paper mode (no private key configured).
func (c *Client) PrivateKey() *ecdsa.PrivateKey {
	return c.privateKey
}

// GetBalance returns the ETH balance (for gas) in wei.
func (c *Client) GetBalance(ctx context.Context) (*big.Int, error) {
	return c.eth.BalanceAt(ctx, c.address, nil)
}

// GetUSDCBalance returns the USDC balance by calling balanceOf on the USDC contract.
func (c *Client) GetUSDCBalance(ctx context.Context) (*big.Int, error) {
	usdcAddr := common.HexToAddress(c.cfg.USDCContract)

	// ERC20 balanceOf(address) selector: 0x70a08231
	data := common.Hex2Bytes("70a08231000000000000000000000000" + c.address.Hex()[2:])

	msg := ethereum.CallMsg{
		To:   &usdcAddr,
		Data: data,
	}
	result, err := c.eth.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("call balanceOf: %w", err)
	}

	balance := new(big.Int).SetBytes(result)
	return balance, nil
}

// GetNonce returns the next nonce, using local tracking to avoid conflicts.
func (c *Client) GetNonce(ctx context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	onchainNonce, err := c.eth.PendingNonceAt(ctx, c.address)
	if err != nil {
		return 0, fmt.Errorf("get pending nonce: %w", err)
	}

	if !c.nonceLoaded || onchainNonce > c.localNonce {
		c.localNonce = onchainNonce
		c.nonceLoaded = true
	}

	nonce := c.localNonce
	c.localNonce++
	return nonce, nil
}

// SuggestGasPrice returns the suggested gas price with buffer applied.
func (c *Client) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	gasPrice, err := c.eth.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest gas price: %w", err)
	}

	// Apply buffer
	bufferBig := big.NewFloat(c.cfg.GasPriceBuffer)
	gasPriceFloat := new(big.Float).SetInt(gasPrice)
	buffered, _ := new(big.Float).Mul(gasPriceFloat, bufferBig).Int(nil)

	// Check ceiling
	maxGwei := big.NewInt(int64(c.cfg.MaxGasPriceGwei * 1e9))
	if buffered.Cmp(maxGwei) > 0 {
		slog.Warn("gas price exceeds ceiling, capping",
			"suggested_gwei", new(big.Float).Quo(new(big.Float).SetInt(buffered), big.NewFloat(1e9)),
			"max_gwei", c.cfg.MaxGasPriceGwei,
		)
		return maxGwei, nil
	}

	return buffered, nil
}

// SendTransaction signs and sends a transaction.
func (c *Client) SendTransaction(ctx context.Context, to common.Address, data []byte, value *big.Int) (*types.Transaction, error) {
	if c.privateKey == nil {
		return nil, fmt.Errorf("no private key configured (paper mode?)")
	}

	nonce, err := c.GetNonce(ctx)
	if err != nil {
		return nil, err
	}

	gasPrice, err := c.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	gasLimit := uint64(200000) // default, can be estimated

	tx := types.NewTransaction(nonce, to, value, gasLimit, gasPrice, data)

	signer := types.NewEIP155Signer(c.chainID)
	signedTx, err := types.SignTx(tx, signer, c.privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}

	if err := c.eth.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("send transaction: %w", err)
	}

	slog.Info("transaction sent",
		"hash", signedTx.Hash().Hex(),
		"nonce", nonce,
		"to", to.Hex(),
	)

	return signedTx, nil
}

// TransferUSDC sends USDC to the given address.
func (c *Client) TransferUSDC(ctx context.Context, to common.Address, amount *big.Int) (*types.Transaction, error) {
	usdcAddr := common.HexToAddress(c.cfg.USDCContract)

	// ERC20 transfer(address,uint256) selector: 0xa9059cbb
	paddedTo := common.LeftPadBytes(to.Bytes(), 32)
	paddedAmount := common.LeftPadBytes(amount.Bytes(), 32)
	data := append(common.Hex2Bytes("a9059cbb"), paddedTo...)
	data = append(data, paddedAmount...)

	return c.SendTransaction(ctx, usdcAddr, data, big.NewInt(0))
}

// WaitForReceipt waits for a transaction to be mined and returns the receipt.
func (c *Client) WaitForReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	for {
		receipt, err := c.eth.TransactionReceipt(ctx, txHash)
		if err == nil {
			return receipt, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			continue
		}
	}
}

// Close disconnects the client.
func (c *Client) Close() {
	c.eth.Close()
}

