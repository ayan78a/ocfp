package router

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"opencode-free-proxy/internal/config"
)

// modelsEntry is one /v1/models item.
type modelsEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HandleModels is GET /v1/models: fetches the upstream zen model list and
// filters it to the free tier (src/app/api/providers/suggested-models/
// filters.js "opencode-free"): ids ending in "-free" (plus big-pickle),
// minus known-dead ids. Falls back to the static registry models when the
// upstream list is unreachable.
func (s *Server) HandleModels(w http.ResponseWriter, r *http.Request) {
	var entries []modelsEntry
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, config.ModelsURL, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+config.PublicBearer)
		if resp, err := client.Do(req); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			entries = parseUpstreamModels(raw)
		}
	}
	if entries == nil {
		entries = staticFreeModels()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   entries,
	})
}

// parseUpstreamModels applies the free filter to the upstream JSON.
func parseUpstreamModels(raw []byte) []modelsEntry {
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Data) == 0 {
		// Some versions return a bare array.
		var arr []struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			return nil
		}
		for _, m := range arr {
			parsed.Data = append(parsed.Data, struct {
				ID string `json:"id"`
			}{m.ID})
		}
		if len(parsed.Data) == 0 {
			return nil
		}
	}
	seen := map[string]bool{}
	out := make([]modelsEntry, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := m.ID
		if id == "" || seen[id] {
			continue
		}
		if !strings.HasSuffix(id, "-free") && !knownFree(id) {
			continue
		}
		if deadFree(id) {
			continue
		}
		seen[id] = true
		out = append(out, modelsEntry{ID: id, Name: id})
	}
	if len(out) == 0 {
		return nil
	}
	sortModels(out)
	return out
}

func knownFree(id string) bool {
	for _, k := range strings.Split(config.KnownFreeModels, ",") {
		if strings.TrimSpace(k) == id {
			return true
		}
	}
	return false
}

func deadFree(id string) bool {
	for _, d := range strings.Split(config.DeadFreeModels, ",") {
		if strings.TrimSpace(d) == id {
			return true
		}
	}
	return false
}

// staticFreeModels is the registry fallback (providers/registry/opencode.js
// models + the known-free id).
func staticFreeModels() []modelsEntry {
	out := []modelsEntry{
		{ID: "muse-spark-1.2-contributor-free", Name: "Muse Spark 1.2 Contributor Free"},
		{ID: "muse-spark-1.3-contributor-free", Name: "Muse Spark 1.3 Contributor Free"},
	}
	if known := knownFree("big-pickle"); known {
		out = append(out, modelsEntry{ID: "big-pickle", Name: "big-pickle"})
	}
	return out
}

func sortModels(entries []modelsEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}
