package tblx

import (
	"fmt"
	"strconv"
	"strings"
)

// ConvertCSV turns raw CSV string data into typed, column-major data
// suitable for NewWriter.
//
// headers and colTypes must have the same length. Every cell is trimmed
// of surrounding whitespace and then converted to the column's type:
//
//   - DTypeInt    via strconv.ParseInt(v, 10, 64)
//   - DTypeFloat  via strconv.ParseFloat(v, 64)
//   - DTypeString kept as the trimmed string
//
// TBLX has no NULL: empty cells become 0, 0.0 or "" respectively.
// Errors carry the offending column name and CSV line number.
func ConvertCSV(headers []string, rows [][]string, colTypes []DataType) ([][]any, error) {
	if len(headers) != len(colTypes) {
		return nil, fmt.Errorf("tblx: %d headers but %d column types", len(headers), len(colTypes))
	}

	out := make([][]any, len(colTypes))
	for c, dt := range colTypes {
		if err := dt.Validate(); err != nil {
			return nil, fmt.Errorf("tblx: column %q: %w", headers[c], err)
		}

		col := make([]any, len(rows))
		for ri, row := range rows {
			raw := ""
			if c < len(row) {
				raw = strings.TrimSpace(row[c])
			}

			switch dt {
			case DTypeInt:
				if raw == "" {
					col[ri] = int64(0) // TBLX missing value
					continue
				}
				v, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("tblx: column %q, line %d: cannot parse %q as int: %w",
						headers[c], ri+2, raw, err) // +2: header line + 1-based index
				}
				col[ri] = v
			case DTypeFloat:
				if raw == "" {
					col[ri] = float64(0) // TBLX missing value
					continue
				}
				v, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return nil, fmt.Errorf("tblx: column %q, line %d: cannot parse %q as float: %w",
						headers[c], ri+2, raw, err)
				}
				col[ri] = v
			case DTypeString:
				col[ri] = raw
			}
		}
		out[c] = col
	}
	return out, nil
}

// GuessTypes infers the narrowest DataType for each column from
// column-major string samples (as read from CSV). A column becomes int
// when every non-empty value parses as int64, float when every non-empty
// value parses as float64, and string otherwise. Columns whose samples
// are all empty default to string.
func GuessTypes(cols [][]string) []DataType {
	out := make([]DataType, len(cols))
	for c, col := range cols {
		sawInt, sawFloat, nonEmpty := true, true, false
		for _, raw := range col {
			v := strings.TrimSpace(raw)
			if v == "" {
				continue
			}
			nonEmpty = true
			if sawInt {
				if _, err := strconv.ParseInt(v, 10, 64); err != nil {
					sawInt = false
				}
			}
			if sawFloat {
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					sawFloat = false
				}
			}
		}
		switch {
		case nonEmpty && sawInt:
			out[c] = DTypeInt
		case nonEmpty && sawFloat:
			out[c] = DTypeFloat
		default:
			out[c] = DTypeString
		}
	}
	return out
}
