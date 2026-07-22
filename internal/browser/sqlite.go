package browser

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
)

// A read-only SQLite reader, enough to query Chrome's own databases.
//
// # Why this exists
//
// Chrome keeps history, bookmarks and downloads in SQLite files. There is no
// DevTools API for any of it — the protocol can drive a page but cannot answer
// "where have I been". Reading the files directly is the only route.
//
// The project has no third-party dependencies, and that constraint is worth
// keeping for a component that reads the user's entire browsing history: the
// alternative is pulling in a CGo SQLite binding, which is a large amount of C
// running with full access to exactly the data one would least like to hand to
// an unaudited dependency.
//
// # What is deliberately missing
//
// This reads. It cannot write, and there is no code path that could. It
// understands table b-trees, records and overflow pages. It does not implement
// indexes, WAL, joins, expressions, or anything resembling a query planner —
// callers get whole tables and filter in Go. For tables of a few hundred
// thousand rows that is fast enough to be unnoticeable, and the absence of a
// SQL parser removes an entire class of injection concern.
//
// # Databases in use
//
// A live Chrome holds a lock. Every caller copies the file first and reads the
// copy; the data is a snapshot, which for history is the correct semantics
// anyway.

const sqliteMagic = "SQLite format 3\x00"

// db is an open database file.
type db struct {
	data     []byte
	pageSize int
	usable   int // page size less any reserved trailing bytes
	encoding int // 1 UTF-8, 2 UTF-16le, 3 UTF-16be
}

// openDB parses a database from bytes already in memory.
//
// Loading whole rather than seeking is deliberate: these files are single-digit
// megabytes, the caller has just copied one, and random access across a b-tree
// makes far more syscalls than one read.
func openDB(data []byte) (*db, error) {
	if len(data) < 100 || string(data[:16]) != sqliteMagic {
		return nil, fmt.Errorf("not a SQLite database")
	}

	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536 // the one value that does not fit the field
	}
	if pageSize < 512 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("implausible page size %d", pageSize)
	}

	reserved := int(data[20])
	enc := int(binary.BigEndian.Uint32(data[56:60]))
	if enc != 1 && enc != 2 && enc != 3 {
		enc = 1
	}

	return &db{
		data:     data,
		pageSize: pageSize,
		usable:   pageSize - reserved,
		encoding: enc,
	}, nil
}

// page returns the bytes of a one-based page number.
func (d *db) page(n int) ([]byte, error) {
	if n < 1 {
		return nil, fmt.Errorf("page %d out of range", n)
	}
	start := (n - 1) * d.pageSize
	if start+d.pageSize > len(d.data) {
		return nil, fmt.Errorf("page %d beyond end of file", n)
	}
	return d.data[start : start+d.pageSize], nil
}

// table describes one table in the schema.
type table struct {
	name     string
	rootPage int
	columns  []string
	// rowidCol is the index of an INTEGER PRIMARY KEY column, which is stored
	// as the rowid rather than in the record body. -1 when there is none.
	rowidCol int
}

// tables reads the schema from page 1.
func (d *db) tables() (map[string]table, error) {
	rows, err := d.readTree(1, 0)
	if err != nil {
		return nil, err
	}

	out := map[string]table{}
	for _, r := range rows {
		// sqlite_master: type, name, tbl_name, rootpage, sql
		if len(r.values) < 5 {
			continue
		}
		if asText(r.values[0]) != "table" {
			continue
		}
		name := asText(r.values[1])
		root, ok := r.values[3].(int64)
		if !ok || root <= 0 {
			continue
		}
		cols, pk := parseColumns(asText(r.values[4]))
		out[name] = table{name: name, rootPage: int(root), columns: cols, rowidCol: pk}
	}
	return out, nil
}

// row is one record, with its rowid.
type row struct {
	rowid  int64
	values []any
}

// maxRows bounds a single table read.
//
// A corrupt or hostile page pointer could otherwise walk a cycle forever. The
// limit is far above any real Chrome table and exists purely as a backstop.
const maxRows = 2_000_000

// readTree walks a table b-tree and returns its rows.
func (d *db) readTree(pageNum int, depth int) ([]row, error) {
	// Depth is bounded because a b-tree with a cycle in its interior pointers
	// would otherwise recurse until the stack gives out. Real trees are a
	// handful of levels deep.
	if depth > 32 {
		return nil, fmt.Errorf("b-tree nested too deeply — file is probably corrupt")
	}

	pg, err := d.page(pageNum)
	if err != nil {
		return nil, err
	}

	// Page 1 carries the 100-byte file header before its b-tree header.
	offset := 0
	if pageNum == 1 {
		offset = 100
	}
	if offset+8 > len(pg) {
		return nil, fmt.Errorf("truncated page %d", pageNum)
	}

	pageType := pg[offset]
	cellCount := int(binary.BigEndian.Uint16(pg[offset+3 : offset+5]))

	headerLen := 8
	if pageType == 0x05 || pageType == 0x02 {
		headerLen = 12 // interior pages carry a right-most pointer
	}
	cellPointers := offset + headerLen

	var rows []row

	switch pageType {
	case 0x0D: // leaf table
		for i := range cellCount {
			p := cellPointers + i*2
			if p+2 > len(pg) {
				break
			}
			cellStart := int(binary.BigEndian.Uint16(pg[p : p+2]))
			if cellStart <= 0 || cellStart >= len(pg) {
				continue
			}
			r, err := d.readLeafCell(pg[cellStart:])
			if err != nil {
				continue // one unreadable row should not lose the table
			}
			rows = append(rows, r)
			if len(rows) > maxRows {
				return rows, nil
			}
		}

	case 0x05: // interior table
		for i := range cellCount {
			p := cellPointers + i*2
			if p+2 > len(pg) {
				break
			}
			cellStart := int(binary.BigEndian.Uint16(pg[p : p+2]))
			if cellStart < 0 || cellStart+4 > len(pg) {
				continue
			}
			child := int(binary.BigEndian.Uint32(pg[cellStart : cellStart+4]))
			sub, err := d.readTree(child, depth+1)
			if err != nil {
				continue
			}
			rows = append(rows, sub...)
			if len(rows) > maxRows {
				return rows, nil
			}
		}
		// The right-most child holds keys greater than every cell above.
		if offset+12 <= len(pg) {
			right := int(binary.BigEndian.Uint32(pg[offset+8 : offset+12]))
			if right > 0 {
				if sub, err := d.readTree(right, depth+1); err == nil {
					rows = append(rows, sub...)
				}
			}
		}

	default:
		// Index pages (0x02, 0x0A) hold no table rows. Reaching one means the
		// root page given was for an index, which is a caller error, not a
		// corrupt file — either way there is nothing to return.
		return nil, nil
	}

	return rows, nil
}

// readLeafCell parses one table-leaf cell.
func (d *db) readLeafCell(cell []byte) (row, error) {
	payloadLen, n := readVarint(cell)
	if n == 0 {
		return row{}, fmt.Errorf("bad payload length")
	}
	cell = cell[n:]

	rowid, n := readVarint(cell)
	if n == 0 {
		return row{}, fmt.Errorf("bad rowid")
	}
	cell = cell[n:]

	payload, err := d.readPayload(cell, int(payloadLen))
	if err != nil {
		return row{}, err
	}

	values, err := d.parseRecord(payload)
	if err != nil {
		return row{}, err
	}
	return row{rowid: rowid, values: values}, nil
}

// readPayload assembles a record, following the overflow chain when the record
// is too large to sit on one page.
func (d *db) readPayload(cell []byte, total int) ([]byte, error) {
	if total < 0 || total > 1<<30 {
		return nil, fmt.Errorf("implausible payload size %d", total)
	}

	// How much lives on this page, per the format's spill rules. The constants
	// are not arbitrary: they keep at least four cells on every page so the
	// tree stays shallow.
	u := d.usable
	maxLocal := u - 35
	local := total

	if total > maxLocal {
		minLocal := ((u-12)*32/255 - 23)
		k := minLocal + (total-minLocal)%(u-4)
		if k <= maxLocal {
			local = k
		} else {
			local = minLocal
		}
	}
	if local < 0 || local > len(cell) {
		return nil, fmt.Errorf("payload does not fit its cell")
	}

	buf := make([]byte, 0, total)
	buf = append(buf, cell[:local]...)
	if local == total {
		return buf, nil
	}

	// The four bytes after the local part point at the first overflow page.
	if local+4 > len(cell) {
		return nil, fmt.Errorf("missing overflow pointer")
	}
	next := int(binary.BigEndian.Uint32(cell[local : local+4]))

	// Each overflow page is a 4-byte next-pointer followed by content. The
	// visited set guards against a chain that loops back on itself.
	seen := map[int]bool{}
	for next != 0 && len(buf) < total {
		if seen[next] {
			return nil, fmt.Errorf("overflow chain loops at page %d", next)
		}
		seen[next] = true

		pg, err := d.page(next)
		if err != nil {
			return nil, err
		}
		next = int(binary.BigEndian.Uint32(pg[0:4]))

		take := d.usable - 4
		if remaining := total - len(buf); take > remaining {
			take = remaining
		}
		if 4+take > len(pg) {
			return nil, fmt.Errorf("overflow page truncated")
		}
		buf = append(buf, pg[4:4+take]...)
	}

	if len(buf) != total {
		return nil, fmt.Errorf("payload short: got %d of %d", len(buf), total)
	}
	return buf, nil
}

// parseRecord decodes a record body into typed values.
func (d *db) parseRecord(payload []byte) ([]any, error) {
	headerLen, n := readVarint(payload)
	if n == 0 || int(headerLen) > len(payload) || headerLen < 1 {
		return nil, fmt.Errorf("bad record header")
	}

	var serials []int64
	pos := n
	for pos < int(headerLen) {
		s, m := readVarint(payload[pos:])
		if m == 0 {
			break
		}
		serials = append(serials, s)
		pos += m
	}

	body := payload[headerLen:]
	values := make([]any, 0, len(serials))
	at := 0

	for _, s := range serials {
		size := serialSize(s)
		if at+size > len(body) {
			// Truncated body: return what parsed rather than nothing.
			values = append(values, nil)
			continue
		}
		values = append(values, d.decodeValue(s, body[at:at+size]))
		at += size
	}
	return values, nil
}

// serialSize is the byte width of a serial type.
func serialSize(s int64) int {
	switch {
	case s == 0, s == 8, s == 9:
		return 0 // NULL, and the literals 0 and 1, occupy no space
	case s >= 1 && s <= 4:
		return int(s)
	case s == 5:
		return 6
	case s == 6, s == 7:
		return 8
	case s >= 12:
		if s%2 == 0 {
			return int((s - 12) / 2) // blob
		}
		return int((s - 13) / 2) // text
	default:
		return 0 // 10 and 11 are reserved
	}
}

// decodeValue turns raw bytes into a Go value.
func (d *db) decodeValue(s int64, raw []byte) any {
	switch {
	case s == 0:
		return nil
	case s == 8:
		return int64(0)
	case s == 9:
		return int64(1)
	case s >= 1 && s <= 6:
		return beInt(raw)
	case s == 7:
		return math.Float64frombits(binary.BigEndian.Uint64(raw))
	case s >= 12 && s%2 == 0:
		return append([]byte(nil), raw...)
	case s >= 13:
		return d.decodeText(raw)
	default:
		return nil
	}
}

// decodeText honours the database's declared encoding.
func (d *db) decodeText(raw []byte) string {
	switch d.encoding {
	case 2, 3:
		if len(raw)%2 != 0 {
			raw = raw[:len(raw)-1]
		}
		runes := make([]rune, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			var u uint16
			if d.encoding == 2 {
				u = binary.LittleEndian.Uint16(raw[i : i+2])
			} else {
				u = binary.BigEndian.Uint16(raw[i : i+2])
			}
			runes = append(runes, rune(u))
		}
		return string(runes)
	default:
		return string(raw)
	}
}

// beInt reads a big-endian two's-complement integer of 1 to 8 bytes.
func beInt(raw []byte) int64 {
	if len(raw) == 0 {
		return 0
	}
	var v int64
	// Sign-extend from the top bit of the first byte.
	if raw[0]&0x80 != 0 {
		v = -1
	}
	for _, b := range raw {
		v = v<<8 | int64(b)
	}
	return v
}

// readVarint decodes SQLite's big-endian variable-length integer.
//
// Up to nine bytes: the first eight contribute seven bits each, and a ninth
// contributes all eight.
func readVarint(b []byte) (int64, int) {
	var v uint64
	for i := range 8 {
		if i >= len(b) {
			return 0, 0
		}
		v = v<<7 | uint64(b[i]&0x7F)
		if b[i]&0x80 == 0 {
			return int64(v), i + 1
		}
	}
	if len(b) < 9 {
		return 0, 0
	}
	v = v<<8 | uint64(b[8])
	return int64(v), 9
}

// parseColumns extracts column names from a CREATE TABLE statement, and the
// index of any INTEGER PRIMARY KEY.
//
// This is not a SQL parser and does not try to be. It splits the column list on
// commas at bracket depth zero and takes the first identifier of each part,
// which covers every table Chrome creates.
func parseColumns(sql string) ([]string, int) {
	open := strings.Index(sql, "(")
	closeAt := strings.LastIndex(sql, ")")
	if open < 0 || closeAt <= open {
		return nil, -1
	}
	inner := sql[open+1 : closeAt]

	var parts []string
	depth, start := 0, 0
	for i, c := range inner {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, inner[start:])

	var cols []string
	rowidCol := -1
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		name := strings.Trim(fields[0], "`\"[]")

		// Table-level constraints are not columns.
		switch strings.ToUpper(name) {
		case "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT":
			continue
		}

		upper := strings.ToUpper(p)
		if strings.Contains(upper, "INTEGER PRIMARY KEY") {
			rowidCol = len(cols)
		}
		cols = append(cols, name)
	}
	return cols, rowidCol
}

// asText renders a value as a string.
func asText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%g", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// asInt renders a value as an integer where that is meaningful.
func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	default:
		return 0
	}
}

// queryTable reads a whole table as name-keyed maps.
func (d *db) queryTable(name string) ([]map[string]any, error) {
	schema, err := d.tables()
	if err != nil {
		return nil, err
	}
	t, ok := schema[name]
	if !ok {
		var have []string
		for n := range schema {
			have = append(have, n)
		}
		return nil, fmt.Errorf("no table %q (found: %s)", name, strings.Join(have, ", "))
	}

	rows, err := d.readTree(t.rootPage, 0)
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		m := make(map[string]any, len(t.columns))
		for i, col := range t.columns {
			var v any
			if i < len(r.values) {
				v = r.values[i]
			}
			// An INTEGER PRIMARY KEY is an alias for the rowid and is stored as
			// NULL in the record itself.
			if i == t.rowidCol && v == nil {
				v = r.rowid
			}
			m[col] = v
		}
		out = append(out, m)
	}
	return out, nil
}

// openSnapshot copies a database and opens the copy.
//
// Reading a file a live Chrome holds open risks a torn read, and on some
// platforms an outright lock. Copying costs a few milliseconds for a file of
// this size and removes the whole category of problem.
func openSnapshot(path string) (*db, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return openDB(data)
}
