package router

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"opencode-free-proxy/internal/cloak"
	"opencode-free-proxy/internal/config"
	"opencode-free-proxy/internal/relay"
	"opencode-free-proxy/internal/upstream"
)

// lineRelay is the per-request SSE relay contract. ProcessTail receives an
// unterminated final segment (upstream closed mid-line): passthrough forwards
// it raw (stream.js flush), translate re-parses it like any line.
type lineRelay interface {
	ProcessLine(line string) error
	ProcessTail(line string) error
	Flush() error
}

// htmlTitleRe pulls the <title> out of an upstream HTML error page.
var htmlTitleRe = regexp.MustCompile(`(?i)<title>([^<]+)</title>`)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// stream dispatches a streaming client response: relay selection follows
// streamingHandler.js buildTransformStream — translate when the client and
// upstream formats differ, passthrough otherwise. Responses passthrough
// synthesizes response.failed + [DONE] when the stream aborts or stalls
// before a terminal event (buildAbortedResponsesTerminalBytes).
func (s *Server) stream(w http.ResponseWriter, r *http.Request, resp *http.Response, cancelUpstream context.CancelFunc, sourceFormat, targetFormat relay.Format, body map[string]any, upstreamModel string, customToolNames map[string]bool, intent *cloak.ThinkingCfg) {
	defer cancelUpstream()
	// Retry/error/forced paths close explicitly; this covers the relay paths
	// (base.js consumes or cancels the body either way).
	defer func() { _ = resp.Body.Close() }()

	// Non-SSE upstream body (Cloudflare 5xx HTML page): return a clean JSON
	// error instead of piping garbage through the SSE path. JS builds the
	// message with formatProviderError — the one prefixed call site here.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "text/event-stream") && !strings.Contains(ct, "application/json") {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		short := shortHTMLMessage(string(buf[:n]), ct)
		writeError(w, resp.StatusCode, fmt.Sprintf("[%d]: %s", resp.StatusCode, short))
		return
	}

	corsHeaders(w.Header(), true)
	flusher, _ := w.(http.Flusher)
	out := &flushWriter{w: w, f: flusher}

	var streamRelay lineRelay
	if sourceFormat != targetFormat {
		tr := relay.NewTranslateRelay(out, body, upstreamModel, sourceFormat, "", intent)
		tr.SetCustomToolNames(customToolNames)
		streamRelay = tr
	} else {
		streamRelay = relay.NewPassthroughRelay(out, body, upstreamModel, sourceFormat, intent)
	}
	isResponsesPassthrough := sourceFormat == relay.FormatResponses && targetFormat == relay.FormatResponses

	// ctx cancellation releases the upstream connection on stall/teardown.
	lineErr := upstream.ScanLines(r.Context(), resp.Body, config.StreamStall, streamRelay.ProcessLine, streamRelay.ProcessTail)
	if lineErr != nil {
		// Stall, transport failure, or client disconnect mid-stream: a
		// Responses passthrough client still needs a parseable terminal.
		if isResponsesPassthrough {
			_, _ = out.WriteString(relay.FormatIncompleteResponsesFailure())
			_, _ = out.WriteString("data: [DONE]\n\n")
		}
		_ = resp.Body.Close()
		return
	}
	if err := streamRelay.Flush(); err != nil {
		_ = resp.Body.Close()
	}
}

// shortHTMLMessage sanitizes an upstream HTML error page into a short
// client-safe message (streamingHandler.js non-SSE guard).
func shortHTMLMessage(bodyText, ct string) string {
	collapse := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	if m := htmlTitleRe.FindStringSubmatch(bodyText); m != nil {
		sanitized := collapse(htmlTagRe.ReplaceAllString(m[1], ""))
		if sanitized != "" {
			return clamp160(sanitized)
		}
	}
	clean := collapse(htmlTagRe.ReplaceAllString(bodyText, ""))
	if clean != "" && len(clean) < 200 {
		return clamp160(clean)
	}
	return fmt.Sprintf("Upstream returned non-SSE response (%s)", ct)
}

func clamp160(s string) string {
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

// flushWriter is an io.StringWriter that flushes after every write so SSE
// frames reach the client immediately.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) WriteString(p string) (int, error) {
	n, err := fw.w.Write([]byte(p))
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
