// Command server runs the OpenCode free-tier proxy: an OpenAI-compatible
// router (chat completions + responses + models) backed solely by the
// opencode free provider.
package main

import (
	"log"
	"net/http"
	"os"

	"opencode-free-proxy/internal/config"
	"opencode-free-proxy/internal/identity"
	"opencode-free-proxy/internal/router"
	"opencode-free-proxy/internal/upstream"
)

func main() {
	cfg := config.FromEnv()
	uaCache := identity.NewUserAgentCache()

	server := &router.Server{
		Cfg:      cfg,
		Upstream: upstream.NewClient(),
		UA:       uaCache,
	}

	// Warm the opencode UA cache in the background; requests before the
	// GitHub probe completes use the pinned fallback (fail-open).
	go func() {
		ua := uaCache.Warm(server.Upstream.HTTP)
		log.Printf("opencode UA cache warm: %s", ua)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", server.HandleChatCompletions)
	mux.HandleFunc("POST /v1/responses", server.HandleResponses)
	mux.HandleFunc("GET /v1/models", server.HandleModels)
	mux.HandleFunc("OPTIONS /", server.HandleOptions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":" + cfg.Port
	log.Printf("opencode-free-proxy listening on %s (upstream %s)", addr, cfg.UpstreamBase)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server exited: %v", err)
	}
	os.Exit(0)
}
