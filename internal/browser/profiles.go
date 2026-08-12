package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The user's own Chrome profiles, which she had no concept of.
//
// # The gap
//
// A person with one Chrome profile and a person with five look identical to code
// that opens "the Chrome profile". Everything here read Default, or whichever
// directory happened to be found first: the history search, the saved usernames,
// and the sync that copies a session into the automation profile. So on a machine
// with a work profile, a personal one and three others, "my account" resolved to
// whichever Chrome had made Default, and the answer was confidently about the
// wrong person.
//
// # Where the names live
//
// Chrome keeps them in Local State, in the user data directory, under
// profile.info_cache: a map from directory name ("Default", "Profile 3") to a
// display name and the signed-in account. That is the only place the mapping
// exists. The directory names are meaningless on their own, which is exactly why
// reading them without the map produced "Profile 3" in an answer to a human.
//
// # It reads and never writes
//
// This is the user's live browser data. Nothing here opens it for writing, and
// the sync that copies out of it is one-directional by design.

// Profile is one of the user's Chrome profiles.
type Profile struct {
	// Dir is the directory name inside the user data directory: "Default",
	// "Profile 1". This is what --profile-directory takes.
	Dir string
	// Name is what the user called it, as shown in Chrome's profile menu.
	Name string
	// Account is the signed-in email, when there is one. Empty for a profile
	// nobody signed into, which is common for a throwaway.
	Account string
	// Active marks the profile Chrome used last, which is the best available
	// guess at "theirs" and is still only a guess.
	Active bool
}

// Label describes a profile the way a person would recognise it.
func (p Profile) Label() string {
	switch {
	case p.Name != "" && p.Account != "":
		return fmt.Sprintf("%s (%s)", p.Name, p.Account)
	case p.Name != "":
		return p.Name
	case p.Account != "":
		return p.Account
	default:
		return p.Dir
	}
}

// localState is the shape of the parts of Local State that matter here.
type localState struct {
	Profile struct {
		InfoCache map[string]struct {
			Name     string `json:"name"`
			UserName string `json:"user_name"`
			GAIAName string `json:"gaia_name"`
		} `json:"info_cache"`
		LastUsed string `json:"last_used"`
	} `json:"profile"`
}

// Profiles lists the user's real Chrome profiles, newest-used first.
//
// dir is the Chrome user data directory. Empty means look in the usual places,
// the same list HistoryFile searches, so that a machine with Chromium rather
// than Chrome answers rather than reporting nothing.
func Profiles(dir string) ([]Profile, error) {
	root := dir
	if root == "" {
		var err error
		root, err = userDataDir()
		if err != nil {
			return nil, err
		}
	}

	b, err := os.ReadFile(filepath.Join(root, "Local State"))
	if err != nil {
		return nil, fmt.Errorf("Chrome's profile list is in Local State under %s and it "+
			"could not be read: %w", root, err)
	}
	var st localState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("Chrome's Local State under %s would not parse: %w", root, err)
	}
	if len(st.Profile.InfoCache) == 0 {
		return nil, fmt.Errorf("Chrome's Local State under %s lists no profiles, which "+
			"means this is not a profile directory or Chrome has never run here", root)
	}

	out := make([]Profile, 0, len(st.Profile.InfoCache))
	for d, info := range st.Profile.InfoCache {
		name := info.Name
		if name == "" {
			name = info.GAIAName
		}
		// A directory that is listed and absent is stale bookkeeping rather than a
		// profile, and offering it would send the sync at nothing.
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			continue
		}
		out = append(out, Profile{
			Dir:     d,
			Name:    name,
			Account: info.UserName,
			Active:  d == st.Profile.LastUsed,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("every profile Chrome lists under %s is missing from disk", root)
	}
	// Last used first, then by name, so the most likely answer leads and the
	// order is stable rather than map order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return strings.ToLower(out[i].Label()) < strings.ToLower(out[j].Label())
	})
	return out, nil
}

// FindProfile resolves what a person said into one profile.
//
// # Why it refuses an ambiguous match rather than picking
//
// The whole point of this file is that picking the wrong profile answers
// confidently about the wrong person: the wrong inbox, the wrong calendar, the
// wrong saved logins. "work" matching two profiles is a question, not a
// preference, and a tool that guesses here is worse than one that asks.
//
// Matched against the display name, the account and the directory, because a
// person might say "personal", "me@gmail.com" or "Profile 2" and all three are
// reasonable ways to name the same thing.
func FindProfile(dir, want string) (Profile, error) {
	all, err := Profiles(dir)
	if err != nil {
		return Profile{}, err
	}
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		for _, p := range all {
			if p.Active {
				return p, nil
			}
		}
		return all[0], nil
	}

	var hits []Profile
	for _, p := range all {
		if strings.Contains(strings.ToLower(p.Name), want) ||
			strings.Contains(strings.ToLower(p.Account), want) ||
			strings.EqualFold(p.Dir, want) {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Profile{}, fmt.Errorf("no Chrome profile matches %q. There are %d: %s",
			want, len(all), describeProfiles(all))
	default:
		return Profile{}, fmt.Errorf("%q matches %d profiles (%s), and using the wrong one "+
			"answers about the wrong account. Name it more precisely",
			want, len(hits), describeProfiles(hits))
	}
}

func describeProfiles(ps []Profile) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Label())
	}
	return strings.Join(out, ", ")
}

// DescribeProfiles renders the list for a person to choose from.
func DescribeProfiles(ps []Profile) string {
	var b strings.Builder
	for _, p := range ps {
		mark := " "
		if p.Active {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-22s %-34s %s\n", mark, p.Dir, p.Name, p.Account)
	}
	return strings.TrimRight(b.String(), "\n")
}

// userDataDir finds the browser data directory that actually has profiles in it.
//
// The same candidate list HistoryFile searches, and for the same reason: a
// machine may have Chrome, Chromium, Brave and Edge installed, and the answer
// should be the one being used rather than the first one named. The signal is a
// Local State file that parses and lists profiles, because an installed browser
// that has never run leaves a directory with nothing useful in it.
func userDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var tried []string
	for _, base := range browserProfiles {
		root := filepath.Join(home, base)
		tried = append(tried, base)
		b, err := os.ReadFile(filepath.Join(root, "Local State"))
		if err != nil {
			continue
		}
		var st localState
		if err := json.Unmarshal(b, &st); err != nil {
			continue
		}
		if len(st.Profile.InfoCache) > 0 {
			return root, nil
		}
	}
	return "", fmt.Errorf("no Chromium-family browser with profiles was found — looked in %s",
		strings.Join(tried, ", "))
}

// HistoryFileFor locates the history database of one named profile.
//
// HistoryFile picks whichever profile was modified most recently, which is a
// good default and the wrong answer to "search my work profile". This takes the
// question literally.
func HistoryFileFor(want string) (string, Profile, error) {
	root, err := userDataDir()
	if err != nil {
		return "", Profile{}, err
	}
	p, err := FindProfile(root, want)
	if err != nil {
		return "", Profile{}, err
	}
	path := filepath.Join(root, p.Dir, "History")
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return "", p, fmt.Errorf("the %s profile has no history database at %s, so there is "+
			"nothing to search there", p.Label(), path)
	}
	return path, p, nil
}
