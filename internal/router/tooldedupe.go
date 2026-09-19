package router

import (
	"net/http"
	"regexp"
	"strings"

	"opencode-free-proxy/internal/jsonx"
)

// Claude-client tool dedupe, ported from open-sse/utils/toolDeduper.js via its
// chatCore.js call site (:216-223): strip built-in/duplicate tools when an
// equivalent MCP tool is present, to cut tool-definition token bloat for
// Claude clients. Runs on the translated body's tools BEFORE the executor
// adds the opencode fingerprint quartet, so only client tools are stripped.

// dedupMatcher is one entry of a rule's triggers/strip lists — either a plain
// string (exact equality, toolDeduper.js:29) or an anchored RegExp
// (toolDeduper.js:30).
type dedupMatcher struct {
	literal string
	re      *regexp.Regexp
}

func (m dedupMatcher) matches(name string) bool {
	if m.re != nil {
		return m.re.MatchString(name)
	}
	return name == m.literal
}

func lit(s string) dedupMatcher { return dedupMatcher{literal: s} }

func rx(pattern string) dedupMatcher {
	return dedupMatcher{re: regexp.MustCompile(pattern)}
}

// dedupRules ports DEDUP_RULES (toolDeduper.js:6-22).
var dedupRules = []struct {
	triggers []dedupMatcher
	strip    []dedupMatcher
}{
	{
		// Exa MCP present → drop built-in web tools (Exa is preferred).
		triggers: []dedupMatcher{lit("mcp__exa__web_search_exa"), lit("mcp__exa__web_fetch_exa")},
		strip:    []dedupMatcher{lit("WebSearch"), lit("WebFetch"), lit("mcp__workspace__web_fetch")},
	},
	{
		// Tavily MCP present → drop built-in web tools.
		triggers: []dedupMatcher{lit("mcp__tavily__tavily_search"), lit("mcp__tavily__tavily_extract")},
		strip:    []dedupMatcher{lit("WebSearch"), lit("WebFetch"), lit("mcp__workspace__web_fetch")},
	},
	{
		// Browser MCP present → drop Cowork's duplicate Claude_in_Chrome connector.
		triggers: []dedupMatcher{rx(`^mcp__browsermcp__`)},
		strip:    []dedupMatcher{rx(`^mcp__Claude_in_Chrome__`)},
	},
}

// dedupeToolName ports getToolName (toolDeduper.js:24-26):
// `t?.name || t?.function?.name || ""` — the chat nested shape and the
// responses flat shape.
func dedupeToolName(tool any) string {
	o := jsonx.AsObj(tool)
	if o == nil {
		return ""
	}
	if n := jsonx.AsStr(o["name"]); n != "" {
		return n
	}
	return jsonx.AsStr(jsonx.Get(o["function"], "name"))
}

// dedupeTools ports dedupeTools (toolDeduper.js:33-47): collect tool names,
// and for every rule whose TRIGGER matches at least one name, strip the tools
// whose name matches any of that rule's strip patterns. stripped carries the
// removed names in first-collect order (the JS Array.from(Set) of :46, used
// only for logging).
func dedupeTools(tools []any) (out []any, stripped []string) {
	if len(tools) == 0 {
		return tools, nil // toolDeduper.js:34
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = dedupeToolName(t)
	}
	toStrip := map[string]bool{}
	for _, rule := range dedupRules {
		triggered := false
		for _, n := range names {
			for _, t := range rule.triggers {
				if t.matches(n) { // toolDeduper.js:38 names.some(...)
					triggered = true
					break
				}
			}
			if triggered {
				break
			}
		}
		if !triggered {
			continue // toolDeduper.js:39
		}
		for _, n := range names {
			if toStrip[n] {
				continue
			}
			for _, p := range rule.strip {
				if p.matches(n) { // toolDeduper.js:41
					toStrip[n] = true
					stripped = append(stripped, n)
					break
				}
			}
		}
	}
	if len(toStrip) == 0 {
		return tools, nil // toolDeduper.js:44
	}
	out = make([]any, 0, len(tools))
	for _, t := range tools {
		if toStrip[dedupeToolName(t)] { // toolDeduper.js:45
			continue
		}
		out = append(out, t)
	}
	return out, stripped
}

// isClaudeClientTool ports the minimal detectClientTool check used by the
// chatCore.js:217 gate (`clientTool === "claude"`): the UA (lowercased)
// contains "claude-cli" or "claude-code", or the x-app header (lowercased)
// equals "cli" (open-sse/utils/clientDetector.js:36, signature :20-25).
// The earlier antigravity / github-copilot branches (clientDetector.js:28-34)
// are not ported — they only outrank the claude branch for requests that also
// carry those body/header signals, which no claude-cli client sends, and this
// proxy's single provider is opencode (isNativePassthrough is always false
// here).
func isClaudeClientTool(header http.Header) bool {
	ua := strings.ToLower(header.Get("User-Agent"))
	if strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code") {
		return true
	}
	return strings.ToLower(header.Get("X-App")) == "cli"
}
