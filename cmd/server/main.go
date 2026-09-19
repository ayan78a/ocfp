// Command server runs the OpenCode free-tier proxy: an OpenAI-compatible
// router (chat completions + responses + models) backed solely by the
// opencode free provider.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"opencode-free-proxy/internal/config"
	"opencode-free-proxy/internal/identity"
	"opencode-free-proxy/internal/router"
	"opencode-free-proxy/internal/upstream"
)

// version is stamped at build time (Dockerfile: -ldflags "-X main.version=…").
var version = "dev"

func main() {
	// Subcommand dispatch: the runtime image is `scratch` — no shell, no curl —
	// so the Docker HEALTHCHECK runs the entrypoint binary itself against its
	// own /healthz. Anything else (or no args) serves.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			log.Fatalf("healthcheck failed: %v", err)
		}
		return
	}

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
	log.Printf("opencode-free-proxy %s listening on %s (upstream %s)", version, addr, cfg.UpstreamBase)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server exited: %v", err)
	}
	os.Exit(0)
}

// runHealthcheck backs the Docker HEALTHCHECK: GET the server's own /healthz
// on the configured port and report success. The 5 s client timeout matches
// HEALTHCHECK --timeout=5s.
func runHealthcheck() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = config.DefaultPort
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz status %d", resp.StatusCode)
	}
	return nil
}
