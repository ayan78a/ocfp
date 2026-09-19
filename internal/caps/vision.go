package caps

import (
	"regexp"
	"strings"
)

// Name-based vision detection, ported verbatim from
// open-sse/providers/visionPatterns.js — the last resort when neither an
// exact row nor a pattern row knows a model. Vendors put the modality in the
// id ("qwen3-vl-plus", "glm-4.6v", "deepseek-v4-flash-vision-exp"), so a
// custom or freshly released model still gets image input instead of silently
// dropping it.
//
// Only ever turns vision ON. Never used to turn a declared capability off
// (visionPatterns.js:1-6).

// sepClass is visionPatterns.js SEP (:8) — the vendor separator set.
const sepClass = `[-_/:.]`

// notVisionRe ports visionPatterns.js NOT_VISION (:12-21): image GENERATION,
// video generation and non-chat models also carry these words but take no
// image input — checked first so they can never match.
var notVisionRe = regexp.MustCompile(
	`(^|` + sepClass + `)(image|img)(` + sepClass + `|$)` +
		`|stable-image|gen[0-9]_image|nanobanana|imagine` +
		`|t2v|i2v|flux|dall|sdxl|diffusion` +
		`|embed|rerank|guard|moderation` +
		`|tts|stt|whisper|voice|speech|audio`)

// visionNameRe ports visionPatterns.js VISION_NAME (:26-34): explicit modality
// words plus the "<digit>v" suffix vendors use for vision variants
// (glm-4.6v, glm-5v-turbo). The digit-v branch requires a dotted version so
// the never-shipped `gpt-4v` cannot match.
var visionNameRe = regexp.MustCompile(
	`(^|` + sepClass + `)(vision|vl|vlm|multimodal|omni|visual)(` + sepClass + `|$)` +
		`|[0-9]\.[0-9]+v(` + sepClass + `|$)` +
		`|(^|` + sepClass + `)glm-[0-9]+v(` + sepClass + `|$)` +
		`|(^|` + sepClass + `)(llava|pixtral|internvl|cogvlm|minicpm-v|moondream|idefics|fuyu)`)

// LooksLikeVisionModel ports visionPatterns.js looksLikeVisionModel (:37-42).
// Name signal only.
func LooksLikeVisionModel(modelID string) bool {
	if modelID == "" {
		return false // visionPatterns.js:38
	}
	id := strings.ToLower(modelID) // visionPatterns.js:39 (JS pairs it with the "i" flag)
	if notVisionRe.MatchString(id) {
		return false // visionPatterns.js:40
	}
	return visionNameRe.MatchString(id) // visionPatterns.js:41
}
