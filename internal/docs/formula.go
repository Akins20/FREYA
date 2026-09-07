package docs

import (
	"strconv"
	"strings"
)

// Working out what a formula comes to, so the cell can carry its answer.
//
// # Why a spreadsheet writer has to do arithmetic
//
// A formula cell used to be written as <f>SUM(B2:B5)</f> and nothing else, on
// the reasoning that Excel and LibreOffice recalculate on open and a stale
// cached value is worse than none.
//
// That reasoning is right for a file being edited over time and wrong for one
// written here: these rows are built in one pass from data already in hand, so
// the value cannot be stale — it is computed from the very cells just emitted.
//
// The cost of leaving it out was measured. Of eighteen benchmark failures in the
// 7 September run, thirteen were a spreadsheet that had been created correctly
// and read back empty where the total should be: "a file matched
// workspace/*.xlsx but none contained all of: North, South, East, 600, 400,
// 1200". She had written the label and the formula. Nothing that reads the file
// without a recalculation engine could see the number — including her own
// docs.Extract, so she could not check her own work either, and any second step
// that read the sheet back got a blank.
//
// Excel writes both. So does this now: <f>SUM(B2:B5)</f><v>440</v>, which shows
// the right number in a viewer and still recalculates the moment anyone edits a
// cell it depends on.
//
// The rule when it cannot work something out is to write the formula alone, as
// before. A missing value is a gap; a wrong one is a lie, and this file will not
// guess.

// maxFormulaDepth bounds a formula that refers to another formula — a grand
// total over subtotals is ordinary, a cell that refers to itself is not.
const maxFormulaDepth = 8

// evalFormula computes what a formula comes to against the rows being written.
//
// Reports ok=false for anything it does not fully understand, which is the
// common case for the exotic and is meant to be: the caller then writes the
// formula with no cached value, exactly as it always did.
func evalFormula(expr string, rows [][]string, depth int) (float64, bool) {
	if depth > maxFormulaDepth {
		return 0, false
	}
	e := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expr), FormulaPrefix))
	if e == "" {
		return 0, false
	}

	// A named function over a range: SUM(B2:B5), AVERAGE(B2:B5).
	if open := strings.IndexByte(e, '('); open > 0 && strings.HasSuffix(e, ")") {
		name := strings.ToUpper(strings.TrimSpace(e[:open]))
		inner := e[open+1 : len(e)-1]
		if v, ok := evalCall(name, inner, rows, depth); ok {
			return v, true
		}
		return 0, false
	}

	return evalArith(e, rows, depth)
}

// evalCall applies one of the functions she actually writes.
func evalCall(name, args string, rows [][]string, depth int) (float64, bool) {
	// ROUND takes an expression rather than a range, so it is handled before the
	// gathering below — which resolves references and literals and would reject
	// "B2/7" out of hand.
	if name == "ROUND" {
		parts := splitArgs(args)
		if len(parts) != 2 {
			return 0, false
		}
		x, ok := evalFormula(parts[0], rows, depth+1)
		places, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if !ok || err != nil || places < 0 || places > 10 {
			return 0, false
		}
		p := 1.0
		for i := 0; i < places; i++ {
			p *= 10
		}
		return float64(int64(x*p+copySign(0.5, x))) / p, true
	}

	nums, count, ok := gather(args, rows, depth)
	if !ok {
		return 0, false
	}
	var sum float64
	for _, n := range nums {
		sum += n
	}
	switch name {
	case "SUM":
		return sum, true
	case "AVERAGE", "AVG", "MEAN":
		if len(nums) == 0 {
			return 0, false // a spreadsheet shows #DIV/0!; we decline to guess
		}
		return sum / float64(len(nums)), true
	case "MIN":
		if len(nums) == 0 {
			return 0, false
		}
		m := nums[0]
		for _, n := range nums[1:] {
			if n < m {
				m = n
			}
		}
		return m, true
	case "MAX":
		if len(nums) == 0 {
			return 0, false
		}
		m := nums[0]
		for _, n := range nums[1:] {
			if n > m {
				m = n
			}
		}
		return m, true
	case "COUNT":
		return float64(len(nums)), true
	case "COUNTA":
		return float64(count), true
	case "PRODUCT":
		if len(nums) == 0 {
			return 0, false
		}
		p := 1.0
		for _, n := range nums {
			p *= n
		}
		return p, true
	}
	return 0, false
}

func copySign(mag, sign float64) float64 {
	if sign < 0 {
		return -mag
	}
	return mag
}

// gather resolves an argument list into the numbers it names.
//
// count is how many referenced cells held anything at all, which is what COUNTA
// asks about and is not the same as how many held a number.
func gather(args string, rows [][]string, depth int) (nums []float64, count int, ok bool) {
	for _, part := range splitArgs(args) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Another sheet. The rows being written are one sheet, so a reference
		// across sheets cannot be resolved here — and must say so rather than
		// quietly contributing nothing.
		if strings.Contains(part, "!") {
			return nil, 0, false
		}
		if lo, hi, isRange := parseRange(part); isRange {
			cells := cellsIn(lo, hi)
			// A range whose ends do not parse is a range we failed to read, not an
			// empty one. Treating the two alike made =SUM(Sheet2!B2:B5) come out as
			// zero: no cell resolved, nothing summed, and a confident 0 written into
			// the file. Summing an empty-but-valid range really is 0, which is why
			// this distinction has to be made here rather than by counting numbers.
			if len(cells) == 0 {
				return nil, 0, false
			}
			for _, ref := range cells {
				raw, present := cellAt(ref, rows)
				if !present {
					continue
				}
				if strings.TrimSpace(raw) != "" {
					count++
				}
				if n, got := numberOf(raw, rows, depth); got {
					nums = append(nums, n)
				}
				// A non-numeric cell inside a SUM range is ignored, which is what
				// a spreadsheet does: SUM over a column with a header sums the
				// numbers under it.
			}
			continue
		}
		// A single reference or a literal.
		if n, got := numberOf(part, rows, depth); got {
			nums = append(nums, n)
			count++
			continue
		}
		if _, present := cellAt(part, rows); present {
			continue // an empty or textual cell named directly
		}
		return nil, 0, false // something we do not model
	}
	return nums, count, true
}

// splitArgs splits on commas that are not inside parentheses.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// evalArith handles the flat arithmetic she writes: B2*C2, B2-C2, 100/4.
//
// Left to right, no precedence, and only when every term resolves. Precedence
// is deliberately not implemented rather than implemented badly: a formula this
// does not understand writes no value, which is safe, whereas one it
// misunderstands writes a wrong number into a file somebody trusts.
func evalArith(e string, rows [][]string, depth int) (float64, bool) {
	terms, ops := splitOps(e)
	if len(terms) == 0 || len(terms) != len(ops)+1 {
		return 0, false
	}
	if len(terms) > 1 && hasMixedPrecedence(ops) {
		return 0, false
	}
	acc, ok := numberOf(terms[0], rows, depth)
	if !ok {
		return 0, false
	}
	for i, op := range ops {
		rhs, got := numberOf(terms[i+1], rows, depth)
		if !got {
			return 0, false
		}
		switch op {
		case '+':
			acc += rhs
		case '-':
			acc -= rhs
		case '*':
			acc *= rhs
		case '/':
			if rhs == 0 {
				return 0, false
			}
			acc /= rhs
		default:
			return 0, false
		}
	}
	return acc, true
}

// hasMixedPrecedence reports whether left-to-right would give the wrong answer.
func hasMixedPrecedence(ops []byte) bool {
	var seenAdd, seenMul bool
	for _, o := range ops {
		if o == '+' || o == '-' {
			seenAdd = true
		} else {
			seenMul = true
		}
	}
	return seenAdd && seenMul
}

// splitOps breaks a flat expression into terms and the operators between them.
// A sign at the start, or straight after another operator, belongs to the term.
func splitOps(e string) ([]string, []byte) {
	var terms []string
	var ops []byte
	start := 0
	for i := 0; i < len(e); i++ {
		c := e[i]
		if c != '+' && c != '-' && c != '*' && c != '/' {
			continue
		}
		if i == start {
			continue // a leading sign
		}
		terms = append(terms, e[start:i])
		ops = append(ops, c)
		start = i + 1
	}
	if start > len(e) {
		return nil, nil
	}
	return append(terms, e[start:]), ops
}

// numberOf resolves a term: a literal, or a cell reference, or a cell that
// itself holds a formula.
func numberOf(term string, rows [][]string, depth int) (float64, bool) {
	t := strings.TrimSpace(term)
	if t == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(strings.TrimSuffix(t, "%"), 64); err == nil {
		if strings.HasSuffix(t, "%") {
			return n / 100, true
		}
		return n, true
	}
	raw, present := cellAt(t, rows)
	if !present {
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if isFormula(raw) {
		return evalFormula(raw, rows, depth+1)
	}
	if n, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64); err == nil {
		return n, true
	}
	return 0, false
}

// parseRange splits "B2:B5" into its ends.
func parseRange(s string) (lo, hi string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}

// cellsIn enumerates a rectangular range, inclusive.
func cellsIn(lo, hi string) []string {
	c1, r1, ok1 := refToIndex(lo)
	c2, r2, ok2 := refToIndex(hi)
	if !ok1 || !ok2 {
		return nil
	}
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	// Bounded: a whole-column range like B1:B1048576 is legal in a spreadsheet
	// and is not something to expand one cell at a time.
	if (c2-c1+1)*(r2-r1+1) > 100000 {
		return nil
	}
	var out []string
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			out = append(out, columnName(c)+strconv.Itoa(r+1))
		}
	}
	return out
}

// cellAt reads a cell by reference. present is false when the reference points
// outside the rows being written, which is a formula we cannot resolve rather
// than an empty cell.
func cellAt(ref string, rows [][]string) (string, bool) {
	c, r, ok := refToIndex(ref)
	if !ok || r < 0 || r >= len(rows) {
		return "", false
	}
	if c < 0 || c >= len(rows[r]) {
		return "", true // inside the sheet, past the end of a short row: empty
	}
	return rows[r][c], true
}

// refToIndex turns "B6" into column 1, row 5. Absolute markers are ignored:
// $B$6 addresses the same cell, and nothing here moves formulas about.
func refToIndex(ref string) (col, row int, ok bool) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(ref), "$", ""))
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		col = col*26 + int(s[i]-'A') + 1
		i++
	}
	if i == 0 || i == len(s) {
		return 0, 0, false
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return col - 1, n - 1, true
}

// formatValue renders a computed number the way a cached value should look:
// exact for whole numbers, trimmed of trailing zeros otherwise.
func formatValue(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
