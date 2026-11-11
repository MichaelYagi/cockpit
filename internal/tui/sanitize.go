package tui

import (
	"reflect"
	"strings"
	"unicode"
)

// sanitize replaces control characters in every string reachable from v
// (a pointer to an API response or event). Strings from the API can come
// from other users or devices (process names, USB product strings,
// service descriptions); printed raw, escape sequences in them could
// rewrite or abuse the user's terminal.
func sanitize(v any) {
	walk(reflect.ValueOf(v))
}

func walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() || f.Kind() != reflect.String {
				walk(f)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i))
		}
	case reflect.Map:
		if v.Type().Elem().Kind() == reflect.String && v.Type().Key().Kind() == reflect.String {
			// Keys come from the API too (labels, evidence), so both are cleaned.
			for _, k := range v.MapKeys() {
				val := cleanText(v.MapIndex(k).String())
				ck := cleanText(k.String())
				if ck != k.String() {
					v.SetMapIndex(k, reflect.Value{})
				}
				v.SetMapIndex(reflect.ValueOf(ck).Convert(v.Type().Key()), reflect.ValueOf(val).Convert(v.Type().Elem()))
			}
			return
		}
		for _, k := range v.MapKeys() {
			walk(v.MapIndex(k))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(cleanText(v.String()))
		}
	}
}

// cleanText replaces C0 and C1 control characters (including ESC) and
// invalid UTF-8 with U+FFFD.
func cleanText(s string) string {
	clean := true
	for _, r := range s {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}
