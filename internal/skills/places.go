package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
)

// Named places: "my assignment folder", "the dissertation", "work".
//
// # Why this exists
//
// Without it, "put this in my assignment folder" has three bad outcomes: search
// the whole disk, guess, or ask and then forget the answer by tomorrow. All
// three are worse than a person, who is told once and remembers.
//
// So a place is learned on first use and persisted. The resolution order is
// deliberate — a real path always wins, so naming a folder "documents" can
// never hijack an actual ./documents directory.

const placesFile = "places.json"

// Place is a remembered location.
type Place struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Learned time.Time `json:"learned"`
	Used    int       `json:"used"`
	// LastUsed drives tie-breaking when several places match loosely.
	LastUsed time.Time `json:"last_used"`
}

// PlaceBook stores named locations.
type PlaceBook struct {
	mu     sync.Mutex
	path   string
	Places map[string]Place
}

// OpenPlaces loads the place book from a data directory.
func OpenPlaces(dir string) (*PlaceBook, error) {
	pb := &PlaceBook{
		path:   filepath.Join(dir, placesFile),
		Places: map[string]Place{},
	}
	b, err := os.ReadFile(pb.path)
	if err != nil {
		if os.IsNotExist(err) {
			return pb, nil
		}
		return nil, fmt.Errorf("places: read: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &pb.Places); err != nil {
			return nil, fmt.Errorf("places: parse: %w", err)
		}
	}
	return pb, nil
}

func (pb *PlaceBook) save() error {
	b, err := json.MarshalIndent(pb.Places, "", "  ")
	if err != nil {
		return err
	}
	tmp := pb.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, pb.path)
}

// normalisePlaceName strips the words people naturally wrap a place in, so
// "my assignment folder", "the assignments dir" and "assignment" all land on
// the same key.
func normalisePlaceName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, filler := range []string{"my ", "the ", "our "} {
		s = strings.TrimPrefix(s, filler)
	}
	for _, suffix := range []string{" folder", " directory", " dir", " location"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

// Remember stores a place, verifying the path exists first.
//
// Refusing to remember a path that is not there is the point: a place book full
// of stale entries is worse than an empty one, because it answers confidently
// and wrongly.
func (pb *PlaceBook) Remember(ctx context.Context, name, path string) (Place, error) {
	key := normalisePlaceName(name)
	if key == "" {
		return Place{}, fmt.Errorf("a place needs a name")
	}
	resolved := expandIn(ctx, path)
	info, err := os.Stat(resolved)
	if err != nil {
		return Place{}, fmt.Errorf("cannot remember %q: %s does not exist", name, resolved)
	}
	if !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		abs = resolved
	}

	pb.mu.Lock()
	defer pb.mu.Unlock()
	p := Place{Name: key, Path: abs, Learned: time.Now(), LastUsed: time.Now()}
	if existing, ok := pb.Places[key]; ok {
		p.Learned = existing.Learned
		p.Used = existing.Used
	}
	pb.Places[key] = p
	return p, pb.save()
}

// Resolve turns a name into a path, recording the use.
//
// Matching is exact first, then prefix, then substring — narrowest to widest,
// so "uni" never shadows an exactly-named "university" place.
func (pb *PlaceBook) Resolve(name string) (Place, bool) {
	key := normalisePlaceName(name)
	if key == "" {
		return Place{}, false
	}

	pb.mu.Lock()
	defer pb.mu.Unlock()

	if p, ok := pb.Places[key]; ok {
		return pb.touch(p), true
	}

	var candidates []Place
	for k, p := range pb.Places {
		if strings.HasPrefix(k, key) || strings.HasPrefix(key, k) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		for k, p := range pb.Places {
			if strings.Contains(k, key) || strings.Contains(key, k) {
				candidates = append(candidates, p)
			}
		}
	}
	if len(candidates) == 0 {
		return Place{}, false
	}
	// Most-used wins, then most-recent: habit is the best tie-breaker available.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Used != candidates[j].Used {
			return candidates[i].Used > candidates[j].Used
		}
		return candidates[i].LastUsed.After(candidates[j].LastUsed)
	})
	return pb.touch(candidates[0]), true
}

// touch records a use. Callers must hold the lock.
func (pb *PlaceBook) touch(p Place) Place {
	p.Used++
	p.LastUsed = time.Now()
	pb.Places[p.Name] = p
	_ = pb.save()
	return p
}

// All returns every place, most-used first.
func (pb *PlaceBook) All() []Place {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	out := make([]Place, 0, len(pb.Places))
	for _, p := range pb.Places {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Used > out[j].Used })
	return out
}

// Forget removes a place.
func (pb *PlaceBook) Forget(name string) (bool, error) {
	key := normalisePlaceName(name)
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if _, ok := pb.Places[key]; !ok {
		return false, nil
	}
	delete(pb.Places, key)
	return true, pb.save()
}

// RegisterPlaces adds the place skills and returns the book so file skills can
// resolve names through it.
func RegisterPlaces(r *Registry, dir string) (*PlaceBook, error) {
	pb, err := OpenPlaces(dir)
	if err != nil {
		return nil, err
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "place_remember",
			Description: "Learn where a named location is, so the user never has to say " +
				"the full path again. Do this the moment they tell you — 'my assignment " +
				"folder is ~/Uni/CS401' should be remembered without being asked.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "What they call it, e.g. 'assignment folder'."},
				"path": {Type: "string", Description: "The actual path. Must exist."},
			}, "name", "path"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			p, err := pb.Remember(ctx, argString(args, "name"), argString(args, "path"))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Noted — %q is %s.", p.Name, p.Path), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "place_resolve",
			Description: "Look up where a named location is. Use this BEFORE searching the " +
				"disk for somewhere the user referred to by name. If it returns nothing, " +
				"ask them where it is rather than guessing or searching everywhere.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "The name they used."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			p, ok := pb.Resolve(name)
			if !ok {
				known := pb.All()
				if len(known) == 0 {
					return fmt.Sprintf("I don't know where %q is, and I have no places "+
						"learned yet. Ask the user for the path.", name), nil
				}
				var names []string
				for _, k := range known {
					names = append(names, k.Name)
				}
				return fmt.Sprintf("I don't know %q. Places I do know: %s. "+
					"Ask the user rather than searching the disk.",
					name, strings.Join(names, ", ")), nil
			}
			// Say which place answered when it is not the one asked for. The lookup
			// is deliberately forgiving — prefix and substring, either direction —
			// so "cs401 assignment folder" can resolve to a place stored under
			// "assignments" from an entirely different course. Returning the bare
			// path hid that, and every later file and shell call in the thread then
			// happened somewhere she never chose.
			if !strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(name)) {
				return fmt.Sprintf("%s\n(That is the place saved as %q — the closest match to %q, not an exact one.)",
					p.Path, p.Name, name), nil
			}
			return p.Path, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "place_list",
			Description: "List every remembered location.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			places := pb.All()
			if len(places) == 0 {
				return "No places learned yet.", nil
			}
			var sb strings.Builder
			for _, p := range places {
				fmt.Fprintf(&sb, "- %s → %s (used %d times)\n", p.Name, p.Path, p.Used)
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "place_forget",
			Description: "Forget a remembered location, for when it moves or was wrong.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "The place to forget."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			removed, err := pb.Forget(name)
			if err != nil {
				return "", err
			}
			if !removed {
				return fmt.Sprintf("I wasn't holding a place called %q.", name), nil
			}
			return fmt.Sprintf("Forgotten where %q was.", name), nil
		},
	})

	return pb, nil
}
