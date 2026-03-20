package api

import (
	"net/http"

	"github.com/x10rst/ai-agent-autonom/internal/api/handler"
	"github.com/x10rst/ai-agent-autonom/internal/api/middleware"
)

// NewRouter creates the HTTP router with all API endpoints.
// apiKey is required for all routes except /health.
func NewRouter(h *handler.Handler, apiKey string) http.Handler {
	mux := http.NewServeMux()

	// Health check — public, no auth
	mux.HandleFunc("GET /health", h.Health)

	// Agent control
	mux.HandleFunc("GET /api/v1/agent/status", h.GetAgentStatus)
	mux.HandleFunc("POST /api/v1/agent/start", h.PostAgentStart)
	mux.HandleFunc("POST /api/v1/agent/pause", h.PostAgentPause)
	mux.HandleFunc("POST /api/v1/agent/stop", h.PostAgentStop)

	// Portfolio & trading
	mux.HandleFunc("GET /api/v1/portfolio", h.GetPortfolio)
	mux.HandleFunc("GET /api/v1/positions", h.GetPositions)
	mux.HandleFunc("POST /api/v1/positions/{id}/close", h.PostForceClosePosition)
	mux.HandleFunc("GET /api/v1/orders", h.GetOrders)

	// Configuration
	mux.HandleFunc("GET /api/v1/config", h.GetConfig)
	mux.HandleFunc("PUT /api/v1/config", h.PutConfig)

	// Apply API key auth to all routes
	return middleware.APIKeyAuth(apiKey)(mux)
}
