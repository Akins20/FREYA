package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Knowing what is downloading, what downloaded, and what went wrong.
//
// # What she could actually see before
//
// One tool, browser_downloads, which read the SQLite History file of the user's
// REAL Chrome profile. Three things wrong with that, and each on its own is
// enough to make it useless for the question she needs answered:
//
//   - It is the wrong profile. Her downloads happen in the automation profile;
//     the file she was reading belongs to the browser the user drives by hand.
//     In the Drive run it returned entries from three weeks earlier, which she
//     reasonably read as "nothing I did worked".
//   - It is the wrong source. Chrome flushes that database lazily, so a download
//     that is happening right now is not in it, and one that just finished may
//     not be either.
//   - It has no state. A row appears when it is over. There is no preparing, no
//     in-progress, no failed, no blocked.
//
// So "is it downloading?", "has it finished?", "did it fail?" and "why is
// nothing happening?" were all unanswerable — which matters more than it sounds,
// because a download is the one action that changes NOTHING about the page. With
// no state to consult, a working download and a broken one look identical, and
// the only available move is to click again.
//
// # What this is
//
// The live state, assembled from the browser's own events as they arrive. It
// tracks each download from the moment the browser commits to it, through bytes
// received, to completion, cancellation or failure — and it keeps finished ones
// for the session, because "did that work?" is usually asked a minute later.

// DownloadState is where a download has got to.
type DownloadState string

const (
	// Preparing: the browser has committed to the download but no bytes have
	// arrived. A server generating a zip can sit here for a long time, and it is
	// the state most often mistaken for failure.
	Preparing DownloadState = "preparing"
	// Downloading: bytes are arriving.
	Downloading DownloadState = "downloading"
	// Complete: the file is on disk.
	Complete DownloadState = "complete"
	// Cancelled: stopped, by us or by the browser.
	Cancelled DownloadState = "cancelled"
	// Failed: the browser gave up. Includes a download Chrome refused as unsafe.
	Failed DownloadState = "failed"
)

// Download is one file the browser is fetching or has fetched.
type Download struct {
	GUID     string
	Filename string
	URL      string
	State    DownloadState
	Received float64
	Total    float64
	Started  time.Time
	Ended    time.Time
	// Path is where it landed, once it has.
	Path string
}

// Percent is progress, or -1 when the total size is unknown — which is common,
// and which is why a bare percentage cannot be the only thing reported.
func (d Download) Percent() int {
	if d.Total <= 0 {
		return -1
	}
	p := int(d.Received / d.Total * 100)
	if p > 100 {
		p = 100
	}
	return p
}

// Describe renders one download the way a person would say it.
func (d Download) Describe() string {
	var b strings.Builder
	name := d.Filename
	if name == "" {
		name = d.URL
	}
	fmt.Fprintf(&b, "%s — %s", clipText(name, 70), d.State)

	switch d.State {
	case Preparing:
		b.WriteString(" (the server has not started sending yet; this can take a while for " +
			"something it has to build, like a zip — wait rather than clicking again)")
	case Downloading:
		if p := d.Percent(); p >= 0 {
			fmt.Fprintf(&b, ", %d%% of %s", p, humanBytes(d.Total))
		} else {
			fmt.Fprintf(&b, ", %s so far (total size unknown)", humanBytes(d.Received))
		}
		if age := time.Since(d.Started); age > time.Second {
			fmt.Fprintf(&b, ", running %s", age.Round(time.Second))
		}
	case Complete:
		fmt.Fprintf(&b, ", %s", humanBytes(d.Received))
		if d.Path != "" {
			fmt.Fprintf(&b, ", saved to %s", d.Path)
		}
	case Failed:
		b.WriteString(" — the browser gave up on it. It may have been blocked as unsafe, " +
			"or the link may need a session the download did not carry")
	}
	return b.String()
}

// downloads is the live record for one browser client.
type downloads struct {
	mu sync.Mutex
	// byGUID keeps every download of the session, finished ones included: "did
	// that work?" is usually asked a minute after the fact.
	byGUID map[string]*Download
	order  []string
}

func newDownloads() *downloads {
	return &downloads{byGUID: map[string]*Download{}}
}

func (d *downloads) begin(guid, filename, url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, seen := d.byGUID[guid]; !seen {
		d.order = append(d.order, guid)
	}
	d.byGUID[guid] = &Download{
		GUID: guid, Filename: filename, URL: url,
		State: Preparing, Started: time.Now(),
	}
}

func (d *downloads) progress(guid, state string, received, total float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.byGUID[guid]
	if !ok {
		// Progress for something we never saw begin. Recording it anyway is
		// better than dropping it: a download nobody can account for is exactly
		// the thing worth mentioning.
		rec = &Download{GUID: guid, State: Preparing, Started: time.Now()}
		d.byGUID[guid] = rec
		d.order = append(d.order, guid)
	}
	rec.Received, rec.Total = received, total

	switch state {
	case "inProgress":
		if received > 0 {
			rec.State = Downloading
		}
	case "completed":
		rec.State = Complete
		rec.Ended = time.Now()
		if rec.Filename != "" {
			rec.Path = filepath.Join(DownloadDir(), rec.Filename)
		}
	case "canceled":
		rec.State = Cancelled
		rec.Ended = time.Now()
	}
}

// list returns every download of this session, newest first.
func (d *downloads) list() []Download {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Download, 0, len(d.order))
	for _, g := range d.order {
		if rec, ok := d.byGUID[g]; ok {
			out = append(out, *rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// active reports how many are still going.
func (d *downloads) active() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, rec := range d.byGUID {
		if rec.State == Preparing || rec.State == Downloading {
			n++
		}
	}
	return n
}

// Downloads returns what this browser has fetched or is fetching.
func (c *Client) Downloads() []Download {
	if c == nil || c.downloads == nil {
		return nil
	}
	return c.downloads.list()
}

// ActiveDownloads reports how many are in flight.
func (c *Client) ActiveDownloads() int {
	if c == nil || c.downloads == nil {
		return 0
	}
	return c.downloads.active()
}

// VerifyOnDisk checks that a completed download is actually there.
//
// The browser saying "completed" and a file existing are different claims, and
// the gap between them is where a download that was blocked, quarantined or
// renamed disappears. She reports to a person, so the report should rest on the
// file rather than on the promise of one.
func VerifyOnDisk(d Download) (int64, bool) {
	if d.Path == "" {
		return 0, false
	}
	info, err := os.Stat(d.Path)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// RecentFiles lists what has appeared in the download directory lately.
//
// The backstop for everything above: whatever the browser said, and whether or
// not any event arrived, a file either exists or it does not. This is what makes
// "did it actually download?" answerable even when the event stream missed it —
// a redirect handled by the page, a download begun before the tab was watched.
func RecentFiles(within time.Duration, limit int) ([]string, error) {
	dir := DownloadDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type f struct {
		name string
		mod  time.Time
		size int64
	}
	var recent []f
	cutoff := time.Now().Add(-within)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() || info.ModTime().Before(cutoff) {
			continue
		}
		recent = append(recent, f{e.Name(), info.ModTime(), info.Size()})
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].mod.After(recent[j].mod) })

	out := make([]string, 0, len(recent))
	for i, r := range recent {
		if limit > 0 && i >= limit {
			break
		}
		suffix := ""
		// A .crdownload is Chrome's partial file. Seeing one means the download is
		// still running, which is the answer to "is anything happening?".
		if strings.HasSuffix(r.name, ".crdownload") {
			suffix = "  (still downloading)"
		}
		out = append(out, fmt.Sprintf("%s  %s  %s%s",
			r.name, humanBytes(float64(r.size)), r.mod.Format("15:04:05"), suffix))
	}
	return out, nil
}

// downloadEvent feeds the tracker from the CDP stream.
func (c *Client) downloadEvent(method string, params json.RawMessage) {
	if c.downloads == nil {
		return
	}
	switch method {
	case "Browser.downloadWillBegin":
		var p struct {
			GUID              string `json:"guid"`
			SuggestedFilename string `json:"suggestedFilename"`
			URL               string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		c.downloads.begin(p.GUID, p.SuggestedFilename, p.URL)
	case "Browser.downloadProgress":
		var p struct {
			GUID          string  `json:"guid"`
			State         string  `json:"state"`
			ReceivedBytes float64 `json:"receivedBytes"`
			TotalBytes    float64 `json:"totalBytes"`
		}
		_ = json.Unmarshal(params, &p)
		c.downloads.progress(p.GUID, p.State, p.ReceivedBytes, p.TotalBytes)
	}
}
