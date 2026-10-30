package tblx

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// ConvertCSV turns raw CSV string data into typed, column-major data
// suitable for NewWriter. It converts columns in parallel using one
// goroutine per column for speed on wide tables.
func ConvertCSV(headers []string, rows [][]string, colTypes []DataType) ([][]any, error) {
	if len(headers) != len(colTypes) {
		return nil, fmt.Errorf("tblx: %d headers but %d column types", len(headers), len(colTypes))
	}

	out := make([][]any, len(colTypes))
	var wg sync.WaitGroup
	errCh := make(chan error, len(colTypes))

	for c, dt := range colTypes {
		if err := dt.Validate(); err != nil {
			return nil, fmt.Errorf("tblx: column %q: %w", headers[c], err)
		}
		wg.Add(1)
		go func(c int, dt DataType) {
			defer wg.Done()
			col := make([]any, len(rows))
			for ri, row := range rows {
				raw := ""
				if c < len(row) {
					raw = strings.TrimSpace(row[c])
				}
				switch dt {
				case DTypeInt:
					if raw == "" {
						col[ri] = int64(0)
						continue
					}
					v, err := strconv.ParseInt(raw, 10, 64)
					if err != nil {
						errCh <- fmt.Errorf("tblx: column %q, line %d: cannot parse %q as int: %w",
							headers[c], ri+2, raw, err)
						return
					}
					col[ri] = v
				case DTypeFloat:
					if raw == "" {
						col[ri] = float64(0)
						continue
					}
					v, err := strconv.ParseFloat(raw, 64)
					if err != nil {
						errCh <- fmt.Errorf("tblx: column %q, line %d: cannot parse %q as float: %w",
							headers[c], ri+2, raw, err)
						return
					}
					col[ri] = v
				case DTypeString:
					col[ri] = raw
				}
			}
			out[c] = col
		}(c, dt)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		return nil, err
	}
	return out, nil
}

// GuessTypes infers the narrowest DataType for each column from
// column-major string data. It scans every value (not just a sample),
// so the result is correct for the full dataset: int if all non-empty
// values parse as int64, float if all parse as float64, string otherwise.
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
			if !sawInt && !sawFloat {
				break // can't get narrower than string
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
