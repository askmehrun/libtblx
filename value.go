package tblx

import (
	"fmt"
	"strconv"
)

// FormatValue renders a single decoded cell the way every tblx tool
// displays it: ints in decimal, floats in their shortest round-trip
// form, strings verbatim. Keeping this in the library means `info`,
// `view`, `export` and the language bindings can never drift apart.
func FormatValue(v any) string {
	switch t := v.(type) {
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}
