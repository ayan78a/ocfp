package caps

// Expectations derived by hand from open-sse/providers/capabilities.js and
// visionPatterns.js (JS indices in the comments), not from the Go code.

import "testing"

func TestResolveExactRows(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  Modality
	}{
		// MODEL_CAPABILITIES exact hit (capabilities.js:132) — vision only.
		{"muse spark 1.2", "muse-spark-1.2-contributor-free", Modality{Vision: true}},
		{"muse spark 1.3", "muse-spark-1.3-contributor-free", Modality{Vision: true}},
		// Exact beats the *glm* pattern row, which carries no vision flags
		// (capabilities.js:112 vs :344).
		{"glm-5.3-flash exact", "glm-5.3-flash", Modality{Vision: true, VideoInput: true, PDF: true}},
		{"glm-4.6v exact", "glm-4.6v", Modality{Vision: true, VideoInput: true}},
		// baseModel "/" strip resolves the exact row (capabilities.js:457).
		{"vendor prefixed muse", "acme/muse-spark-1.2-contributor-free", Modality{Vision: true}},
		{"vendor prefixed glm", "z-ai/glm-4.6v", Modality{Vision: true, VideoInput: true}},
		// Zero-modality exact rows still short-circuit patterns + heuristic
		// (capabilities.js:467-468 return before refine).
		{"gpt-image-1 is generation only", "gpt-image-1", Modality{}},
		{"coder-model alias", "coder-model", Modality{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.model); got != tc.want {
				t.Fatalf("Resolve(%q) = %+v, want %+v", tc.model, got, tc.want)
			}
		})
	}
}

func TestResolvePatternRows(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  Modality
	}{
		// *qwen*coder* (capabilities.js:324) matches before *qwen* — vision
		// stays false even though the name would survive the heuristic.
		{"qwen coder", "qwen3-coder-free", Modality{}},
		// *qwen*vl* (capabilities.js:322) turns vision on, refined (no-op).
		{"qwen vl", "qwen3-vl-free", Modality{Vision: true}},
		// *gemini-2.5* carries vision+audio+video (capabilities.js:284).
		{"gemini 2.5", "gemini-2.5-flash-free", Modality{Vision: true, AudioInput: true, VideoInput: true}},
		// *gemini* alone is vision only (capabilities.js:286).
		{"gemini generic", "gemini-1.9-flash", Modality{Vision: true}},
		// *muse*spark* (capabilities.js:396) — non-exact spelling still hits.
		{"muse pattern", "muse-spark-9.9-contributor-free", Modality{Vision: true}},
		// *claude* (capabilities.js:276) — vision on, audio/pdf stay off.
		{"claude generic", "claude-sonnet-9-free", Modality{Vision: true}},
		// *o1-mini* (capabilities.js:305) shields the vision-bearing *o1* row.
		{"o1-mini exception", "o1-mini-free", Modality{}},
		// Pattern rows are anchored and case-insensitive (pricing.js:357 "i").
		{"anchored no substring", "notqwen-coder", Modality{}},
		{"case insensitive", "QWEN3-VL-FREE", Modality{Vision: true}},
		// Pattern match on the RAW model too (capabilities.js:472).
		{"raw model match", "qwen/coder-free", Modality{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.model); got != tc.want {
				t.Fatalf("Resolve(%q) = %+v, want %+v", tc.model, got, tc.want)
			}
		})
	}
}

func TestResolveFloorAndVisionHeuristic(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  Modality
	}{
		// Unknown model, no vision-looking name → DEFAULT floor.
		{"unknown text model", "zzz-unknown-model-free", Modality{}},
		// looksLikeVisionModel turns vision ON for an uncatalogued id
		// (capabilities.js:448 + visionPatterns.js:31 pixtral).
		{"heuristic pixtral", "zzz-pixtral-free", Modality{Vision: true}},
		// NOT_VISION wins over the vision words (visionPatterns.js:12-21).
		{"image generation shielded", "zzz-image-gen-free", Modality{}},
		{"embedding shielded", "zzz-vl-embed-free", Modality{}},
		{"tts shielded", "zzz-vision-tts-free", Modality{}},
		// digit-v requires a dotted version — gpt-4v cannot match
		// (visionPatterns.js:23-25).
		{"gpt-4v never matches", "gpt-4v", Modality{}},
		// dotted-v heuristic on a model no pattern row covers.
		{"dotted v matches", "acme3.5v-turbo-free", Modality{Vision: true}},
		// qwen3.5 hits the *qwen3.5* PATTERN row first (capabilities.js:326:
		// vision+videoInput), so the heuristic is not what answers here.
		{"qwen3.5 pattern row", "qwen3.5-v-turbo-free", Modality{Vision: true, VideoInput: true}},
		// Empty model → floor, no heuristic (capabilities.js:454).
		{"empty model", "", Modality{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.model); got != tc.want {
				t.Fatalf("Resolve(%q) = %+v, want %+v", tc.model, got, tc.want)
			}
		})
	}
}

func TestLooksLikeVisionModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"qwen3-vl-plus", true},                // visionPatterns.js:2
		{"glm-4.6v", true},                     // :2
		{"deepseek-v4-flash-vision-exp", true}, // :3
		{"minicpm-v-latest", true},             // :31
		{"llava-13b", true},                    // :31
		{"claude-sonnet-5", false},             // no modality words
		{"stable-image-ultra", false},          // :15
		{"whisper-large", false},               // :18
		{"nova-speech", false},                 // :18
		{"moderation-latest", false},           // :17
		{"", false},                            // :38
	}
	for _, tc := range cases {
		if got := LooksLikeVisionModel(tc.model); got != tc.want {
			t.Fatalf("LooksLikeVisionModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
