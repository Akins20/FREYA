package memory

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Retrieval pulls older material back into context when the current query
// touches something that has aged out of the verbatim working set.
//
// Scoring is BM25 over the archive. Embeddings would rank better, but they
// would cost a network round-trip per turn and a vector index to maintain;
// BM25 is pure Go, runs offline in microseconds, and for "what did we decide
// about the NTFS drive" — where the user reuses the same nouns the transcript
// contains — lexical matching is close to ideal.
//
// The Retriever interface leaves room to swap in embeddings later without
// touching the context builder.

// BM25 tuning. k1 controls term-frequency saturation, b the strength of
// document-length normalisation. These are the standard defaults.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Hit is a scored retrieval result.
type Hit struct {
	ID    string
	Kind  string // turn | episode | fact
	Text  string
	Score float64
}

// Retriever finds archived material relevant to a query.
type Retriever interface {
	Search(query string, limit int) []Hit
}

type indexedDoc struct {
	id    string
	kind  string
	text  string
	terms map[string]int
	dlen  int
}

// Index is an in-memory BM25 index over archived memory.
type Index struct {
	mu    sync.RWMutex
	docs  []indexedDoc
	df    map[string]int
	total int
}

// NewIndex builds an empty index.
func NewIndex() *Index {
	return &Index{df: map[string]int{}}
}

// BuildIndex indexes every turn, episode and fact in a store.
func BuildIndex(s *Store) *Index {
	idx := NewIndex()
	for _, t := range s.Turns() {
		idx.Add(t.ID, "turn", t.Text)
	}
	for _, e := range s.Episodes() {
		idx.Add(e.ID, "episode", e.Summary+" "+strings.Join(e.Topics, " "))
	}
	for _, f := range s.Facts() {
		idx.Add(f.ID, "fact", f.Key+" "+f.Text)
	}
	return idx
}

// Add indexes one document. Empty documents are ignored.
func (ix *Index) Add(id, kind, text string) {
	terms := tokenize(text)
	if len(terms) == 0 {
		return
	}

	counts := make(map[string]int, len(terms))
	for _, t := range terms {
		counts[t]++
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	for term := range counts {
		ix.df[term]++
	}
	ix.docs = append(ix.docs, indexedDoc{
		id: id, kind: kind, text: text, terms: counts, dlen: len(terms),
	})
	ix.total += len(terms)
}

// Search returns the top scoring documents for a query.
func (ix *Index) Search(query string, limit int) []Hit {
	qterms := tokenize(query)
	if len(qterms) == 0 || limit <= 0 {
		return nil
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := len(ix.docs)
	if n == 0 {
		return nil
	}
	avgdl := float64(ix.total) / float64(n)

	// Precompute IDF per unique query term.
	idf := make(map[string]float64, len(qterms))
	for _, t := range qterms {
		if _, done := idf[t]; done {
			continue
		}
		df := float64(ix.df[t])
		// Robertson/Sparck-Jones IDF with the +1 guard against negatives.
		idf[t] = math.Log(1 + (float64(n)-df+0.5)/(df+0.5))
	}

	hits := make([]Hit, 0, 32)
	for _, d := range ix.docs {
		var score float64
		for term, w := range idf {
			f := float64(d.terms[term])
			if f == 0 {
				continue
			}
			norm := 1 - bm25B + bm25B*float64(d.dlen)/avgdl
			score += w * (f * (bm25K1 + 1)) / (f + bm25K1*norm)
		}
		if score > 0 {
			hits = append(hits, Hit{ID: d.id, Kind: d.kind, Text: d.text, Score: score})
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Size reports how many documents are indexed.
func (ix *Index) Size() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// stopwords are dropped before indexing: they appear in nearly every turn, so
// they carry no discriminative signal and only dilute scores.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "that": true,
	"this": true, "with": true, "was": true, "are": true, "but": true,
	"not": true, "have": true, "has": true, "can": true, "will": true,
	"would": true, "there": true, "their": true, "what": true, "about": true,
	"which": true, "when": true, "your": true, "from": true, "they": true,
	"been": true, "were": true, "into": true, "than": true, "then": true,
	"them": true, "some": true, "just": true, "like": true, "how": true,
	"its": true, "our": true, "out": true,
}

// tokenize lowercases, splits on non-alphanumerics, and drops stopwords and
// single characters.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}
