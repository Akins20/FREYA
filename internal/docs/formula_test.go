package docs

import (
	"strings"
	"testing"
	"time"
)

// The rows a totals formula is normally written against.
func sales() [][]string {
	return [][]string{
		{"region", "units", "price"},
		{"North", "120", "2.5"},
		{"South", "90", "2.5"},
		{"East", "150", "2.5"},
		{"West", "80", "2.5"},
		{"Total", "=SUM(B2:B5)", ""},
	}
}

func TestTheFormulasSheActuallyWrites(t *testing.T) {
	rows := sales()
	for _, c := range []struct {
		expr string
		want float64
	}{
		{"=SUM(B2:B5)", 440},
		{"=SUM(B2:B5,100)", 540},
		{"=AVERAGE(B2:B5)", 110},
		{"=AVG(B2:B5)", 110},
		{"=MIN(B2:B5)", 80},
		{"=MAX(B2:B5)", 150},
		{"=COUNT(B2:B5)", 4},
		{"=COUNTA(A2:A5)", 4},
		{"=PRODUCT(C2:C3)", 6.25},
		{"=B2*C2", 300},
		{"=B2+B3", 210},
		{"=B4-B5", 70},
		{"=B2/4", 30},
		{"=ROUND(B2/7,2)", 17.14},
		{"=B6", 440},   // a formula cell resolved through another formula
		{"=B6/4", 110}, // and then used in arithmetic
		// A range that takes in the header sums the numbers under it, exactly as
		// a spreadsheet does, rather than refusing because one cell is a word.
		{"=SUM(B1:B5)", 440},
	} {
		got, ok := evalFormula(c.expr, rows, 0)
		if !ok {
			t.Errorf("%s could not be evaluated", c.expr)
			continue
		}
		if diff := got - c.want; diff > 0.0001 || diff < -0.0001 {
			t.Errorf("%s = %v, want %v", c.expr, got, c.want)
		}
	}
}

// A missing value is a gap; a wrong one is a lie. Anything this does not fully
// understand must decline, and the caller then writes the formula alone.
func TestItDeclinesRatherThanGuesses(t *testing.T) {
	rows := sales()
	for _, expr := range []string{
		"=VLOOKUP(A2,Sheet2!A:B,2,FALSE)", // another sheet
		"=IF(B2>100,\"big\",\"small\")",   // a conditional
		"=B2+B3*C2",                       // mixed precedence, deliberately not implemented
		"=SUM(Sheet2!B2:B5)",              // a range on another sheet
		"=NONSENSE(B2:B5)",                // a function we do not model
		"=B99+1",                          // outside the rows being written
		"=B2/0",                           // a spreadsheet shows #DIV/0!
		"=",                               // nothing at all
		"=AVERAGE(A2:A5)",                 // no numbers in the range
	} {
		if v, ok := evalFormula(expr, rows, 0); ok {
			t.Errorf("%s was evaluated to %v; it should have declined", expr, v)
		}
	}
}

// A formula that refers to itself must stop rather than recurse.
func TestASelfReferentialFormulaTerminates(t *testing.T) {
	rows := [][]string{
		{"a", "=B1"},
		{"b", "=B1"},
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := evalFormula("=B1", rows, 0)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("a cycle produced a value")
		}
	case <-timeoutAfterASecond():
		t.Fatal("a self-referential formula did not terminate")
	}
}

// The whole point, end to end: the number is in the file.
func TestATotalIsReadableWithoutASpreadsheet(t *testing.T) {
	path := t.TempDir() + "/regions.xlsx"
	if err := WriteXLSX(path, []Sheet{{Name: "Sheet1", Rows: sales()}}); err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	// This is the benchmark's check, in miniature: it reads the text of the file
	// and looks for the numbers. Before the cached value, 440 was simply absent.
	for _, want := range []string{"North", "South", "East", "West", "120", "90", "150", "80", "440"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("the sheet reads back without %q:\n%s", want, doc.Text)
		}
	}
}

// A whole-column range is legal and must not be expanded a million cells at a
// time; declining is the right answer.
func TestAWholeColumnRangeIsNotExpanded(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		evalFormula("=SUM(B1:B1048576)", sales(), 0)
	}()
	select {
	case <-done:
	case <-timeoutAfterASecond():
		t.Fatal("a whole-column range was expanded cell by cell")
	}
}

func timeoutAfterASecond() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		time.Sleep(time.Second)
		close(ch)
	}()
	return ch
}

// A range that could not be read is not an empty range.
//
// The distinction is the whole safety property of this file. =SUM(Sheet2!B2:B5)
// used to come out as 0: parseRange split it, neither end parsed as a cell
// reference, cellsIn returned nothing, and summing nothing gave a confident zero
// that was then written into the file as the answer. Summing a genuinely empty
// range IS 0, which is exactly why "no cells" and "no numbers" have to be told
// apart rather than both falling out as the same total.
func TestAnUnreadableRangeIsNotAnEmptyOne(t *testing.T) {
	rows := sales()
	for _, expr := range []string{
		"=SUM(Sheet2!B2:B5)",
		"=SUM('Other Sheet'.B2:B5)",
		"=SUM(B2:Sheet2!B5)",
		"=SUM(notarange:alsonot)",
	} {
		if v, ok := evalFormula(expr, rows, 0); ok {
			t.Errorf("%s evaluated to %v; a range it cannot read must decline, or a "+
				"number nobody computed ends up in the file", expr, v)
		}
	}

	// And the other side of the line: a real range of blank cells sums to zero,
	// which is correct and must not be swept up by the check above.
	blank := [][]string{
		{"label", "value"},
		{"a", ""},
		{"b", ""},
		{"total", "=SUM(B2:B3)"},
	}
	if v, ok := evalFormula("=SUM(B2:B3)", blank, 0); !ok || v != 0 {
		t.Errorf("SUM over blank cells = %v ok=%v, want 0 true", v, ok)
	}
}
