package term

import (
	"strings"
	"unicode/utf8"
)

// A minimal terminal emulator.
//
// # Why an emulator rather than stripping escapes
//
// Stripping control sequences with a regular expression works until it doesn't.
// OSC sequences terminate with either BEL or ST, shells emit their own
// integration codes, and — decisively — a full-screen program does not *stream*
// its output at all. It positions the cursor and paints. Run `top` or an
// interactive TUI through a regex stripper and you get every frame concatenated
// with the movement removed: an unreadable pile.
//
// So this keeps a screen. Text is placed where the program puts it, erasures
// erase, and reading the screen gives what a person would see. That is the only
// way to answer "what is on screen right now" for a program that repaints.
//
// This implements the subset that real programs actually rely on: cursor
// movement, erase, scrolling, and line wrap. Colour and styling are parsed and
// discarded, since a model reads text rather than attributes.

// Screen is a character grid with a cursor.
type Screen struct {
	rows, cols int
	cells      [][]rune
	curRow     int
	curCol     int

	// saved cursor, for the save/restore pair some programs use
	savedRow, savedCol int

	// parser state
	state   parseState
	params  []int
	current int
	hasPar  bool
	private bool
	oscBuf  strings.Builder
}

type parseState int

const (
	stateGround parseState = iota
	stateEscape
	stateCSI
	stateOSC
	stateOSCEscape
)

// NewScreen creates a blank screen.
func NewScreen(rows, cols int) *Screen {
	s := &Screen{rows: rows, cols: cols}
	s.cells = make([][]rune, rows)
	for i := range s.cells {
		s.cells[i] = blankRow(cols)
	}
	return s
}

func blankRow(cols int) []rune {
	row := make([]rune, cols)
	for i := range row {
		row[i] = ' '
	}
	return row
}

// Write feeds terminal output into the screen.
func (s *Screen) Write(data string) {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRuneInString(data[i:])
		i += size
		s.feed(r)
	}
}

func (s *Screen) feed(r rune) {
	switch s.state {
	case stateGround:
		s.ground(r)
	case stateEscape:
		s.escape(r)
	case stateCSI:
		s.csi(r)
	case stateOSC:
		s.osc(r)
	case stateOSCEscape:
		// An ESC inside an OSC string: ST (ESC \) ends it, anything else is
		// treated as part of the string and ignored.
		s.state = stateGround
		if r != '\\' {
			s.state = stateGround
		}
	}
}

func (s *Screen) ground(r rune) {
	switch r {
	case 0x1b: // ESC
		s.state = stateEscape
	case '\n':
		s.newline()
	case '\r':
		s.curCol = 0
	case '\t':
		s.curCol = min((s.curCol/8+1)*8, s.cols-1)
	case '\b':
		if s.curCol > 0 {
			s.curCol--
		}
	case 0x07: // BEL
	default:
		if r < 0x20 {
			return // other control characters have no visual effect here
		}
		s.put(r)
	}
}

func (s *Screen) escape(r rune) {
	switch r {
	case '[':
		s.state = stateCSI
		s.params = s.params[:0]
		s.current, s.hasPar, s.private = 0, false, false
	case ']':
		s.state = stateOSC
		s.oscBuf.Reset()
	case '7': // save cursor
		s.savedRow, s.savedCol = s.curRow, s.curCol
		s.state = stateGround
	case '8': // restore cursor
		s.curRow, s.curCol = s.savedRow, s.savedCol
		s.state = stateGround
	case 'M': // reverse index
		if s.curRow > 0 {
			s.curRow--
		}
		s.state = stateGround
	default:
		// ( ) # % and friends select character sets; consume and move on.
		s.state = stateGround
	}
}

func (s *Screen) csi(r rune) {
	switch {
	case r >= '0' && r <= '9':
		s.current = s.current*10 + int(r-'0')
		s.hasPar = true
		return
	case r == ';':
		s.params = append(s.params, s.current)
		s.current, s.hasPar = 0, false
		return
	case r == '?' || r == '<' || r == '=' || r == '>':
		s.private = true
		return
	case r == ' ' || r == '!' || r == '"' || r == '$' || r == '\'':
		return // intermediate bytes
	}

	if s.hasPar {
		s.params = append(s.params, s.current)
	}
	s.dispatchCSI(r)
	s.state = stateGround
}

// param returns parameter n, or def when absent or zero.
func (s *Screen) param(n, def int) int {
	if n >= len(s.params) || s.params[n] == 0 {
		return def
	}
	return s.params[n]
}

func (s *Screen) dispatchCSI(cmd rune) {
	switch cmd {
	case 'A': // cursor up
		s.curRow = max(0, s.curRow-s.param(0, 1))
	case 'B': // cursor down
		s.curRow = min(s.rows-1, s.curRow+s.param(0, 1))
	case 'C': // cursor forward
		s.curCol = min(s.cols-1, s.curCol+s.param(0, 1))
	case 'D': // cursor back
		s.curCol = max(0, s.curCol-s.param(0, 1))
	case 'E': // next line
		s.curRow = min(s.rows-1, s.curRow+s.param(0, 1))
		s.curCol = 0
	case 'F': // previous line
		s.curRow = max(0, s.curRow-s.param(0, 1))
		s.curCol = 0
	case 'G', '`': // column absolute
		s.curCol = clampInt(s.param(0, 1)-1, 0, s.cols-1)
	case 'd': // row absolute
		s.curRow = clampInt(s.param(0, 1)-1, 0, s.rows-1)
	case 'H', 'f': // cursor position
		s.curRow = clampInt(s.param(0, 1)-1, 0, s.rows-1)
		s.curCol = clampInt(s.param(1, 1)-1, 0, s.cols-1)
	case 'J': // erase in display
		s.eraseDisplay(s.paramRaw(0))
	case 'K': // erase in line
		s.eraseLine(s.paramRaw(0))
	case 'L': // insert lines
		s.insertLines(s.param(0, 1))
	case 'M': // delete lines
		s.deleteLines(s.param(0, 1))
	case 'P': // delete characters
		s.deleteChars(s.param(0, 1))
	case 'X': // erase characters
		n := s.param(0, 1)
		for i := 0; i < n && s.curCol+i < s.cols; i++ {
			s.cells[s.curRow][s.curCol+i] = ' '
		}
	case 's':
		s.savedRow, s.savedCol = s.curRow, s.curCol
	case 'u':
		s.curRow, s.curCol = s.savedRow, s.savedCol
	}
	// 'm' (colour and style) and private modes are parsed and deliberately
	// dropped: a model reads text, not attributes.
}

// paramRaw returns parameter n with zero preserved, since erase commands
// distinguish 0, 1 and 2.
func (s *Screen) paramRaw(n int) int {
	if n >= len(s.params) {
		return 0
	}
	return s.params[n]
}

func (s *Screen) osc(r rune) {
	switch r {
	case 0x07: // BEL terminates
		s.state = stateGround
	case 0x1b: // possible ST
		s.state = stateOSCEscape
	default:
		s.oscBuf.WriteRune(r)
	}
}

func (s *Screen) put(r rune) {
	if s.curCol >= s.cols {
		// Wrap rather than truncate: a wrapped line is still readable.
		s.curCol = 0
		s.newline()
	}
	s.cells[s.curRow][s.curCol] = r
	s.curCol++
}

func (s *Screen) newline() {
	s.curRow++
	if s.curRow >= s.rows {
		s.scrollUp()
		s.curRow = s.rows - 1
	}
}

func (s *Screen) scrollUp() {
	copy(s.cells, s.cells[1:])
	s.cells[s.rows-1] = blankRow(s.cols)
}

func (s *Screen) eraseDisplay(mode int) {
	switch mode {
	case 0: // cursor to end
		s.eraseLine(0)
		for r := s.curRow + 1; r < s.rows; r++ {
			s.cells[r] = blankRow(s.cols)
		}
	case 1: // start to cursor
		for r := 0; r < s.curRow; r++ {
			s.cells[r] = blankRow(s.cols)
		}
		s.eraseLine(1)
	default: // whole display
		for r := range s.cells {
			s.cells[r] = blankRow(s.cols)
		}
	}
}

func (s *Screen) eraseLine(mode int) {
	row := s.cells[s.curRow]
	switch mode {
	case 0:
		for c := s.curCol; c < s.cols; c++ {
			row[c] = ' '
		}
	case 1:
		for c := 0; c <= s.curCol && c < s.cols; c++ {
			row[c] = ' '
		}
	default:
		s.cells[s.curRow] = blankRow(s.cols)
	}
}

func (s *Screen) insertLines(n int) {
	for range n {
		copy(s.cells[s.curRow+1:], s.cells[s.curRow:])
		s.cells[s.curRow] = blankRow(s.cols)
	}
}

func (s *Screen) deleteLines(n int) {
	for range n {
		copy(s.cells[s.curRow:], s.cells[s.curRow+1:])
		s.cells[s.rows-1] = blankRow(s.cols)
	}
}

func (s *Screen) deleteChars(n int) {
	row := s.cells[s.curRow]
	for i := s.curCol; i < s.cols; i++ {
		if i+n < s.cols {
			row[i] = row[i+n]
		} else {
			row[i] = ' '
		}
	}
}

// Text renders the screen as a person would see it, with trailing blank space
// removed.
func (s *Screen) Text() string {
	lines := make([]string, 0, s.rows)
	for _, row := range s.cells {
		lines = append(lines, strings.TrimRight(string(row), " "))
	}
	// Drop the empty rows below the content.
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

// Cursor reports the current position, for callers that need to know whether a
// program is waiting at a prompt.
func (s *Screen) Cursor() (row, col int) { return s.curRow, s.curCol }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
