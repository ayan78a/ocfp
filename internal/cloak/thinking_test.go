// Thinking normalization tests — ported from 9router
// open-sse/translator/concerns/thinkingUnified.js (parseSuffix, extractThinking,
// toLevel, normalizeOpenAILevel, stripAll, applyThinking),
// open-sse/translator/concerns/thinking.js (LEVEL_TO_BUDGET, budgetToLevel) and
// open-sse/executors/opencode.js normalizeOpencodeReasoning (golden vectors in
// tests/unit/opencode-muse-spark-thinking.test.js).
package cloak

import (
	"reflect"
	"testing"

	"opencode-free-proxy/internal/jsonx"
)

func levelCfg(l string) *ThinkingCfg { return &ThinkingCfg{Mode: "level", Level: l} }
func budgetCfg(b float64) *ThinkingCfg {
	return &ThinkingCfg{Mode: "budget", Budget: b}
}

// ---------------------------------------------------------------------------
// parseSuffix
// ---------------------------------------------------------------------------

func TestParseSuffix(t *testing.T) {
	cases := []struct {
		model string
		clean string
		want  *ThinkingCfg
		note  string
	}{
		{free12 + "(high)", free12, levelCfg("high"), "level suffix"},
		{"model(8192)", "model", budgetCfg(8192), "numeric suffix is a budget"},
		{"model(0)", "model", budgetCfg(0), "zero budget"},
		{"model(none)", "model", &ThinkingCfg{Mode: "none"}, "none"},
		{"model(off)", "model", &ThinkingCfg{Mode: "none"}, "off alias"},
		{"model(auto)", "model", &ThinkingCfg{Mode: "auto"}, "auto"},
		{"model(ultra)", "model", levelCfg("ultra"), "ultra is its own level"},
		{"model(max)", "model", levelCfg("max"), "max is a known level"},
		{"model(HIGH)", "model", levelCfg("high"), "lowercased"},
		{"model( high )", "model", levelCfg("high"), "trimmed inner whitespace"},
		{"model(bogus)", "model", nil, "unknown value → no override"},
		{"model", "model", nil, "no suffix"},
		{"model()", "model()", nil, "empty group is not a suffix"},
		{"m(x)(high)", "m(x)", levelCfg("high"), "greedy capture keeps the LAST group"},
		{"m(x)(y)(medium)", "m(x)(y)", levelCfg("medium"), "only the last group goes"},
		{"m(x)(y)", "m(x)", nil, "last group wins but an unknown value yields no override"},
		{free13 + "(high)  ", free13, levelCfg("high"), "trailing spaces allowed"},
		{"model(8192", "model(8192", nil, "unclosed group"},
	}
	for _, tc := range cases {
		clean, cfg := ParseSuffix(tc.model)
		if clean != tc.clean {
			t.Errorf("[%s] ParseSuffix(%q) clean = %q, want %q", tc.note, tc.model, clean, tc.clean)
		}
		if tc.want == nil {
			if cfg != nil {
				t.Errorf("[%s] ParseSuffix(%q) cfg = %+v, want nil", tc.note, tc.model, cfg)
			}
			continue
		}
		if cfg == nil {
			t.Errorf("[%s] ParseSuffix(%q) cfg = nil, want %+v", tc.note, tc.model, tc.want)
			continue
		}
		if !reflect.DeepEqual(cfg, tc.want) {
			t.Errorf("[%s] ParseSuffix(%q) cfg = %+v, want %+v", tc.note, tc.model, cfg, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// budget ↔ level maps (concerns/thinking.js)
// ---------------------------------------------------------------------------

func TestBudgetToLevelBoundaries(t *testing.T) {
	cases := []struct {
		budget float64
		want   string
	}{
		{-1, ""}, {0, ""}, {1, "minimal"},
		{767, "minimal"}, {768, "minimal"}, {769, "low"},
		{4096, "low"}, {4097, "medium"},
		{16384, "medium"}, {16385, "high"},
		{28672, "high"}, {28673, "xhigh"},
		{128000, "xhigh"},
	}
	for _, tc := range cases {
		if got := BudgetToLevel(tc.budget); got != tc.want {
			t.Errorf("BudgetToLevel(%v) = %q, want %q", tc.budget, got, tc.want)
		}
	}
}

func TestLevelToBudget(t *testing.T) {
	want := map[string]float64{
		"none": 0, "minimal": 512, "low": 1024, "medium": 8192,
		"high": 24576, "xhigh": 32768, "max": 128000,
	}
	if !reflect.DeepEqual(LevelToBudget, want) {
		t.Errorf("LevelToBudget = %v, want %v", LevelToBudget, want)
	}
}

// Golden: the muse-spark capability ladder is exactly these levels.
func TestMuseSparkLevels(t *testing.T) {
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh"}
	if !reflect.DeepEqual(MuseSparkLevels, want) {
		t.Errorf("MuseSparkLevels = %v, want %v", MuseSparkLevels, want)
	}
}

// ---------------------------------------------------------------------------
// extractThinking
// ---------------------------------------------------------------------------

func TestExtractThinking(t *testing.T) {
	cases := []struct {
		note string
		body map[string]any
		want *ThinkingCfg
	}{
		{"nil body", nil, nil},
		{"empty body", map[string]any{}, nil},
		{"reasoning_effort level", map[string]any{"reasoning_effort": "HIGH"}, levelCfg("high")},
		{"reasoning_effort none", map[string]any{"reasoning_effort": "none"}, &ThinkingCfg{Mode: "none"}},
		{"reasoning_effort off alias", map[string]any{"reasoning_effort": "OFF"}, &ThinkingCfg{Mode: "none"}},
		{"reasoning_effort auto", map[string]any{"reasoning_effort": "Auto"}, &ThinkingCfg{Mode: "auto"}},
		{"reasoning.effort object", map[string]any{"reasoning": map[string]any{"effort": "Medium"}}, levelCfg("medium")},
		{
			"output_config.effort wins first",
			map[string]any{"output_config": map[string]any{"effort": "low"}, "reasoning_effort": "high"},
			levelCfg("low"),
		},
		{
			"non-string output_config.effort is skipped",
			map[string]any{"output_config": map[string]any{"effort": float64(3)}, "reasoning_effort": "high"},
			levelCfg("high"),
		},
		{"thinking enabled with budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4096)}}, budgetCfg(4096)},
		{"thinking adaptive without budget", map[string]any{"thinking": map[string]any{"type": "adaptive"}}, &ThinkingCfg{Mode: "auto"}},
		{"thinking disabled", map[string]any{"thinking": map[string]any{"type": "disabled"}}, &ThinkingCfg{Mode: "none"}},
		{"thinking unknown type falls through", map[string]any{"thinking": map[string]any{"type": "effort"}}, nil},
		{"thinkingLevel", map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "LOW"}}, levelCfg("low")},
		{"thinkingBudget zero disables", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": float64(0)}}, &ThinkingCfg{Mode: "none"}},
		{"thinkingBudget negative is auto", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": float64(-1)}}, &ThinkingCfg{Mode: "auto"}},
		{"thinkingBudget positive is a budget", map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": float64(8192)}}, budgetCfg(8192)},
		{
			"generationConfig.thinkingConfig",
			map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": float64(2048)}}},
			budgetCfg(2048),
		},
		{
			"request.generationConfig.thinkingConfig envelope",
			map[string]any{"request": map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "high"}}}},
			levelCfg("high"),
		},
		{"enable_thinking false", map[string]any{"enable_thinking": false}, &ThinkingCfg{Mode: "none"}},
		{"enable_thinking true", map[string]any{"enable_thinking": true}, &ThinkingCfg{Mode: "auto"}},
		{
			"qwen budget",
			map[string]any{"enable_thinking": true, "thinking_budget": float64(2048)},
			budgetCfg(2048),
		},
		{"thinking_budget alone is not an intent", map[string]any{"thinking_budget": float64(2048)}, nil},
		{"non-bool enable_thinking is ignored", map[string]any{"enable_thinking": "true"}, nil},
	}
	for _, tc := range cases {
		got := ExtractThinking(tc.body)
		if tc.want == nil {
			if got != nil {
				t.Errorf("[%s] ExtractThinking() = %+v, want nil", tc.note, got)
			}
			continue
		}
		if got == nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("[%s] ExtractThinking() = %+v, want %+v", tc.note, got, tc.want)
		}
	}
}

// PARITY BUG repro (expected to FAIL until fixed — see task report):
// thinkingUnified.js reads the effort with `body.reasoning_effort ?? (reasoning
// object ? reasoning.effort : null)`, so an EMPTY-STRING reasoning_effort does
// NOT fall through to reasoning.effort ("" is not nullish) — the body resolves
// to no thinking intent. internal/cloak/thinking.go:152 treats "" as absent and
// consults reasoning.effort anyway, inventing an intent the JS router never sees.
func TestExtractThinkingEmptyEffortFallbackBUG(t *testing.T) {
	body := map[string]any{
		"reasoning_effort": "",
		"reasoning":        map[string]any{"effort": "high"},
	}
	if got := ExtractThinking(body); got != nil {
		t.Errorf("JS `??` keeps the empty reasoning_effort and ignores reasoning.effort (want nil), Go returned %+v", got)
	}
}

// ---------------------------------------------------------------------------
// applyThinking (openai wire format → reasoning_effort)
// ---------------------------------------------------------------------------

func TestApplyThinking(t *testing.T) {
	t.Run("non-reasoning model strips every knob", func(t *testing.T) {
		body := map[string]any{
			"model":            "deepseek-v4-flash-free",
			"temperature":      float64(1),
			"reasoning_effort": "high",
			"reasoning":        map[string]any{"effort": "high", "summary": "auto"},
			"thinking":         map[string]any{"type": "enabled"},
			"thinkingConfig":   map[string]any{"thinkingBudget": float64(1024)},
			"enable_thinking":  true,
			"thinking_budget":  float64(128),
			"output_config":    map[string]any{"effort": "high"},
			"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": float64(1)}, "temperature": float64(2)},
			"request":          map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"x": true}}},
		}
		ApplyThinking(body, "deepseek-v4-flash-free", levelCfg("high"))
		for _, knob := range []string{"reasoning_effort", "reasoning", "thinking", "thinkingConfig", "enable_thinking", "thinking_budget", "output_config"} {
			if _, has := body[knob]; has {
				t.Errorf("knob %q must be stripped from a non-reasoning model", knob)
			}
		}
		if gc, _ := body["generationConfig"].(map[string]any); gc == nil || len(gc) != 1 || gc["temperature"] != float64(2) {
			t.Errorf("generationConfig survives minus its thinkingConfig child, got %v", body["generationConfig"])
		}
		if req, _ := body["request"].(map[string]any); req == nil {
			t.Error("request envelope survives StripAll")
		} else if gc, _ := req["generationConfig"].(map[string]any); gc == nil || len(gc) != 0 {
			t.Errorf("request.generationConfig loses only thinkingConfig, got %v", req["generationConfig"])
		}
		if body["temperature"] != float64(1) {
			t.Error("unrelated fields must survive StripAll")
		}
	})

	t.Run("muse-spark maps intent levels onto reasoning_effort", func(t *testing.T) {
		cases := []struct {
			note   string
			model  string
			intent *ThinkingCfg
			want   any
		}{
			{"level high", free12, levelCfg("high"), "high"},
			{"suffix override beats intent", free12 + "(low)", levelCfg("high"), "low"},
			{"suffix none", free13 + "(none)", levelCfg("high"), "none"},
			{"suffix max clamps to xhigh", free12 + "(max)", levelCfg("high"), "xhigh"},
			{"intent max clamps", free13, levelCfg("max"), "xhigh"},
			{"intent ultra clamps", free13, levelCfg("ultra"), "xhigh"},
			{"intent auto passes through", free12, &ThinkingCfg{Mode: "auto"}, "auto"},
			{"intent none", free12, &ThinkingCfg{Mode: "none"}, "none"},
			{"budget 8192 → medium", free12, budgetCfg(8192), "medium"},
			{"budget 100 → minimal", free12, budgetCfg(100), "minimal"},
		}
		for _, tc := range cases {
			body := map[string]any{"input": []any{}, "reasoning_effort": "high"}
			ApplyThinking(body, tc.model, tc.intent)
			if body["reasoning_effort"] != tc.want {
				t.Errorf("[%s] reasoning_effort = %v, want %q", tc.note, body["reasoning_effort"], tc.want)
			}
			if _, has := body["reasoning"]; has {
				t.Errorf("[%s] StripAll must remove the reasoning object before re-applying", tc.note)
			}
		}
	})

	t.Run("no intent leaves a reasoning body untouched", func(t *testing.T) {
		body := map[string]any{
			"input":  []any{map[string]any{"type": "message", "role": "user"}},
			"stream": true,
			"model":  free12,
		}
		before := map[string]any{"input": body["input"], "stream": true, "model": free12}
		ApplyThinking(body, free12, nil)
		if !reflect.DeepEqual(body, before) {
			t.Errorf("body changed without any thinking intent: %v", body)
		}
		if _, has := body["reasoning_effort"]; has {
			t.Error("no reasoning_effort may appear without intent")
		}
	})

	t.Run("body-carried intent is used when no override/intent", func(t *testing.T) {
		body := map[string]any{"reasoning_effort": "high", "reasoning": map[string]any{"effort": "high"}}
		ApplyThinking(body, free12, nil)
		if body["reasoning_effort"] != "high" {
			t.Errorf("reasoning_effort = %v, want \"high\"", body["reasoning_effort"])
		}
		if _, has := body["reasoning"]; has {
			t.Error("the reasoning object must be stripped once folded into reasoning_effort")
		}
	})
}

// stripAll alone (generationConfig/request child handling).
func TestStripAll(t *testing.T) {
	body := map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"generationConfig": map[string]any{"thinkingConfig": map[string]any{"a": 1}, "b": 2},
		"request":          map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"a": 1}}},
	}
	StripAll(body)
	if _, has := body["thinking"]; has {
		t.Error("thinking must be deleted")
	}
	if gc, _ := body["generationConfig"].(map[string]any); len(gc) != 1 {
		t.Errorf("generationConfig keeps non-thinking children, got %v", gc)
	}
	req := body["request"].(map[string]any)
	if gc, _ := req["generationConfig"].(map[string]any); len(gc) != 0 {
		t.Errorf("request.generationConfig keeps no thinkingConfig, got %v", gc)
	}
	StripAll(nil) // must not panic
}

// ---------------------------------------------------------------------------
// normalizeOpencodeReasoning (executors/opencode.js)
// ---------------------------------------------------------------------------

// Golden: "clamps max to xhigh and emits the Responses reasoning shape".
func TestNormalizeOpencodeReasoning(t *testing.T) {
	cases := []struct {
		note   string
		body   map[string]any
		want   map[string]any // expected body.reasoning
		effort any            // expected remaining body.reasoning_effort (nil = absent)
	}{
		{
			note: "effort folded, max demoted, summary defaulted",
			body: map[string]any{"reasoning": map[string]any{"effort": "MAX"}},
			want: map[string]any{"effort": "xhigh", "summary": "auto"},
		},
		{
			note: "ultra demotes to xhigh (no max on the muse-spark ladder)",
			body: map[string]any{"reasoning_effort": "ultra"},
			want: map[string]any{"effort": "xhigh", "summary": "auto"},
		},
		{
			note: "caps and surrounding whitespace normalized",
			body: map[string]any{"reasoning_effort": "HIGH "},
			want: map[string]any{"effort": "high", "summary": "auto"},
		},
		{
			note: "reasoning.effort path",
			body: map[string]any{"reasoning": map[string]any{"effort": "Low"}},
			want: map[string]any{"effort": "low", "summary": "auto"},
		},
		{
			note: "existing summary kept, other keys preserved",
			body: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "concise", "tone": "x"}},
			want: map[string]any{"effort": "high", "summary": "concise", "tone": "x"},
		},
		{
			note: "reasoning_effort wins over reasoning.effort",
			body: map[string]any{"reasoning_effort": "low", "reasoning": map[string]any{"effort": "high"}},
			want: map[string]any{"effort": "low", "summary": "auto"},
		},
		{
			note: "empty-string effort still folds (JS typeof check)",
			body: map[string]any{"reasoning_effort": ""},
			want: map[string]any{"effort": "", "summary": "auto"},
		},
	}
	for _, tc := range cases {
		NormalizeOpencodeReasoning(tc.body)
		if !reflect.DeepEqual(tc.body["reasoning"], tc.want) {
			t.Errorf("[%s] reasoning = %v, want %v", tc.note, tc.body["reasoning"], tc.want)
		}
		if _, has := tc.body["reasoning_effort"]; has {
			t.Errorf("[%s] reasoning_effort must be deleted after folding", tc.note)
		}
	}

	t.Run("no intent anywhere is a no-op", func(t *testing.T) {
		body := map[string]any{"input": []any{}, "stream": true}
		before := map[string]any{"input": body["input"], "stream": true}
		NormalizeOpencodeReasoning(body)
		if !reflect.DeepEqual(body, before) {
			t.Errorf("body mutated without any reasoning field: %v", body)
		}
	})

	t.Run("non-string efforts are no-ops", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"reasoning_effort": float64(3)},
			{"reasoning": map[string]any{"effort": float64(3)}},
			{"reasoning": map[string]any{}},
			{"reasoning": []any{"not an object"}},
		} {
			before := jsonx.Clone(body).(map[string]any)
			NormalizeOpencodeReasoning(body)
			if !reflect.DeepEqual(body, before) {
				t.Errorf("non-string effort must leave the body untouched, got %v (was %v)", body, before)
			}
		}
	})

	NormalizeOpencodeReasoning(nil) // must not panic
}

// Golden shape check: the muse-spark reasoning envelope emitted for
// reasoning:{effort:"max"} is exactly {effort:"xhigh", summary:"auto"} and the
// chat-side reasoning_effort never survives (golden
// opencode-muse-spark-thinking.test.js "clamps max to xhigh").
func TestNormalizeOpencodeReasoningGoldenShape(t *testing.T) {
	body := map[string]any{
		"input":            []any{},
		"reasoning":        map[string]any{"effort": "max"},
		"reasoning_effort": "max",
		"max_tokens":       float64(131072),
	}
	NormalizeOpencodeReasoning(body)
	want := map[string]any{"effort": "xhigh", "summary": "auto"}
	if !reflect.DeepEqual(body["reasoning"], want) {
		t.Errorf("reasoning = %v, want %v", body["reasoning"], want)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Error("reasoning_effort must be gone")
	}
	if body["max_tokens"] != float64(131072) {
		t.Error("unrelated fields survive")
	}
}
