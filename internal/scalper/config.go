package scalper

// Config holds all configuration for the scalper engine.
type Config struct {
	Enabled           bool
	SeriesSlug        string
	TradeSize         float64
	TotalCapital      float64
	TakeProfitMin     float64
	TakeProfitMax     float64
	StopLoss          float64
	MomentumThreshold float64
	APIKey            string
	APISecret         string
	APIPassphrase     string
	BuilderAddress    string
	SignerAddress     string
	PrivateKey        string // hex, untuk EIP-712 signing
}
