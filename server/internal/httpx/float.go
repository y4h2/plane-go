package httpx

import (
	"strconv"
	"strings"
)

// Float marshals a float64 that always carries a decimal point (e.g. 65535 ->
// "65535.0"). Go's default float marshaling drops the decimal for whole values,
// which JSON parsers (and the contract's type checks) then read as an int.
// Django/DRF always renders these as floats, so we match that.
type Float float64

func (f Float) MarshalJSON() ([]byte, error) {
	s := strconv.FormatFloat(float64(f), 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return []byte(s), nil
}
