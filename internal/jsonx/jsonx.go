// Package jsonx provides small helpers for working with arbitrary JSON
// request/response bodies as map[string]any — the Go equivalent of the
// mutate-in-place object handling the upstream 9router JS engine performs.
package jsonx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AsObj returns v as a JSON object, or nil when v is not one.
func AsObj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// AsArr returns v as a JSON array, or nil when v is not one.
func AsArr(v any) []any {
	a, _ := v.([]any)
	return a
}

// AsStr returns v as a string, or "" when v is not a string.
func AsStr(v any) string {
	s, _ := v.(string)
	return s
}

// AsF64 returns v as a float64 (the encoding/json number type), or 0.
func AsF64(v any) float64 {
	f, _ := v.(float64)
	return f
}

// Truthy reports JS truthiness for a decoded JSON value — the `||` operand
// gate (e.g. utils/error.js:80 `json.error?.message || json.message ||
// json.error || bodyText`). Falsy: nil (null), false, 0, "" — everything
// else is truthy, including empty objects/arrays, "0" and any other number.
// JSON numbers decode as float64.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	default:
		// Objects, arrays and any other decoded shape are truthy in JS.
		return true
	}
}

// NumCoerce mirrors JS Number(v): numbers pass, bools map to 1/0, numeric
// strings parse, everything else (null, objects, garbage) yields 0 — the
// `Number(x) || 0` idiom.
func NumCoerce(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// Get returns m[key] when m is a JSON object.
func Get(m any, key string) any {
	if o := AsObj(m); o != nil {
		return o[key]
	}
	return nil
}

// Set sets m[key]=v when m is a JSON object.
func Set(m any, key string, v any) {
	if o := AsObj(m); o != nil {
		o[key] = v
	}
}

// Delete removes keys from m when m is a JSON object.
func Delete(m any, keys ...string) {
	if o := AsObj(m); o != nil {
		for _, k := range keys {
			delete(o, k)
		}
	}
}

// Has reports whether m is an object containing key.
func Has(m any, key string) bool {
	o := AsObj(m)
	if o == nil {
		return false
	}
	_, ok := o[key]
	return ok
}

// Clone deep-copies v through a JSON round-trip. Non-JSON values (channels,
// funcs) become null, mirroring JSON.stringify semantics.
func Clone(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// ObjOf builds {"k": v, ...} preserving call order.
func ObjOf(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// ArrOf builds an array preserving call order.
func ArrOf(items ...any) []any {
	if items == nil {
		return []any{}
	}
	return items
}
