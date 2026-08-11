package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The question she could not answer: is it downloading, did it finish, did it
// fail? The old tool read the user's real Chrome History file — wrong profile,
// written lazily, and containing only finished transfers. In the Drive run it
// returned entries from three weeks earlier.
func TestADownloadHasALifecycleSheCanSee(t *testing.T) {
	c := &Client{downloads: newDownloads(), events: &eventLog{}}

	// A real completion has a file behind it. This used to be left out, and the
	// code obliged by reporting DownloadDir()/suggestedFilename whether or not
	// anything was ever written there — so the test passed on a path that had
	// never existed. See settle.
	dir := t.TempDir()
	t.Setenv("FREYA_DOWNLOAD_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "g1"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	c.downloadEvent("Browser.downloadWillBegin", json.RawMessage(
		`{"guid":"g1","suggestedFilename":"IMG_1.jpg","url":"https://drive.google.com/x"}`))

	// Committed but nothing sent yet — the state most often mistaken for failure.
	d := c.Downloads()
	if len(d) != 1 || d[0].State != Preparing {
		t.Fatalf("a started download is not visible as preparing: %+v", d)
	}
	if !strings.Contains(d[0].Describe(), "wait rather than clicking again") {
		t.Errorf("preparing does not tell her not to retry: %q", d[0].Describe())
	}
	if c.ActiveDownloads() != 1 {
		t.Error("a preparing download is not counted as active")
	}

	c.downloadEvent("Browser.downloadProgress", json.RawMessage(
		`{"guid":"g1","state":"inProgress","receivedBytes":500000,"totalBytes":2000000}`))
	d = c.Downloads()
	if d[0].State != Downloading || d[0].Percent() != 25 {
		t.Fatalf("progress is not tracked: state=%s pct=%d", d[0].State, d[0].Percent())
	}
	if !strings.Contains(d[0].Describe(), "25%") {
		t.Errorf("progress is not reported: %q", d[0].Describe())
	}

	c.downloadEvent("Browser.downloadProgress", json.RawMessage(
		`{"guid":"g1","state":"completed","receivedBytes":2000000,"totalBytes":2000000}`))
	d = c.Downloads()
	if d[0].State != Complete {
		t.Fatalf("completion is not tracked: %+v", d[0])
	}
	if d[0].Path == "" || !strings.Contains(d[0].Describe(), "saved to") {
		t.Errorf("she is not told where it landed: %q", d[0].Describe())
	}
	// And the path she is told has to be one that exists, under the name the
	// page suggested rather than the browser's GUID.
	if want := filepath.Join(dir, "IMG_1.jpg"); d[0].Path != want {
		t.Errorf("path is %q, want %q", d[0].Path, want)
	}
	if _, err := os.Stat(d[0].Path); err != nil {
		t.Errorf("the path she was given does not exist: %v", err)
	}
	if c.ActiveDownloads() != 0 {
		t.Error("a finished download is still counted as active")
	}
}

// A cancelled download must not read as finished, or she reports success.
func TestACancelledDownloadIsNotSuccess(t *testing.T) {
	c := &Client{downloads: newDownloads(), events: &eventLog{}}
	c.downloadEvent("Browser.downloadWillBegin", json.RawMessage(
		`{"guid":"g2","suggestedFilename":"big.zip"}`))
	c.downloadEvent("Browser.downloadProgress", json.RawMessage(
		`{"guid":"g2","state":"canceled","receivedBytes":10}`))

	d := c.Downloads()
	if d[0].State != Cancelled {
		t.Fatalf("state = %s, want cancelled", d[0].State)
	}
	if strings.Contains(d[0].Describe(), "saved to") {
		t.Errorf("a cancelled download claims a file: %q", d[0].Describe())
	}
}

// Total size is frequently unknown, and a bare percentage would then be a lie.
func TestUnknownSizeIsReportedHonestly(t *testing.T) {
	c := &Client{downloads: newDownloads(), events: &eventLog{}}
	c.downloadEvent("Browser.downloadWillBegin", json.RawMessage(`{"guid":"g3","suggestedFilename":"s.csv"}`))
	c.downloadEvent("Browser.downloadProgress", json.RawMessage(
		`{"guid":"g3","state":"inProgress","receivedBytes":4096,"totalBytes":0}`))

	d := c.Downloads()[0]
	if d.Percent() != -1 {
		t.Errorf("percent = %d; an unknown total must not become a number", d.Percent())
	}
	if !strings.Contains(d.Describe(), "total size unknown") {
		t.Errorf("the description invents certainty: %q", d.Describe())
	}
}

// Progress for a transfer we never saw begin still has to be recorded — a
// download nobody can account for is exactly the thing worth mentioning.
func TestAnUnannouncedDownloadIsStillTracked(t *testing.T) {
	c := &Client{downloads: newDownloads(), events: &eventLog{}}
	c.downloadEvent("Browser.downloadProgress", json.RawMessage(
		`{"guid":"ghost","state":"inProgress","receivedBytes":128}`))
	if len(c.Downloads()) != 1 {
		t.Error("a download that began before we were watching was dropped")
	}
}

// A client with no tracker must not panic — the tools call these on any tab.
func TestDownloadAccessorsAreNilSafe(t *testing.T) {
	var c *Client
	if c.Downloads() != nil || c.ActiveDownloads() != 0 {
		t.Error("nil client did not answer safely")
	}
	c2 := &Client{}
	if c2.Downloads() != nil || c2.ActiveDownloads() != 0 {
		t.Error("a client with no tracker did not answer safely")
	}
}

// A finished download must be reported at a path that exists.
//
// Chrome, driven through setDownloadBehavior, writes the file under its download
// GUID and never under the name the page suggested. Completion then set Path to
// DownloadDir()/Filename regardless, so browser_downloads reported "saved to
// .../report.txt" for a file that was on disk as
// 5b7c2769-9fdf-42a2-aece-967ba2824d3a. Measured on a live run: she was told one
// path and had to list the folder to find the other.
func TestAFinishedDownloadGetsItsRealNameBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FREYA_DOWNLOAD_DIR", dir)

	const guid = "5b7c2769-9fdf-42a2-aece-967ba2824d3a"
	if err := os.WriteFile(filepath.Join(dir, guid), []byte("DOWNLOAD-CANARY\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := settle(guid, "report.txt")
	want := filepath.Join(dir, "report.txt")
	if got != want {
		t.Fatalf("path is %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("the reported path does not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, guid)); err == nil {
		t.Error("the GUID file is still there, so the rename did not happen")
	}
}

// A download must never quietly replace something already there. This is the one
// place the name comes from the page rather than from the user.
func TestASecondDownloadDoesNotOverwriteTheFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FREYA_DOWNLOAD_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("THE ORIGINAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guid-2"), []byte("THE NEW ONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := settle("guid-2", "report.txt")
	if got == filepath.Join(dir, "report.txt") {
		t.Fatal("the new download took the existing file's name")
	}
	original, err := os.ReadFile(filepath.Join(dir, "report.txt"))
	if err != nil || string(original) != "THE ORIGINAL\n" {
		t.Errorf("the original was destroyed: %q %v", original, err)
	}
	if body, err := os.ReadFile(got); err != nil || string(body) != "THE NEW ONE\n" {
		t.Errorf("the new download is not at %q: %q %v", got, body, err)
	}
}

// When there is no file under the GUID, report nothing rather than a path that
// was never checked. An empty Path suppresses the "saved to" line entirely.
func TestNoFileMeansNoPathIsClaimed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FREYA_DOWNLOAD_DIR", dir)
	if got := settle("missing-guid", "report.txt"); got != "" {
		t.Errorf("claimed %q for a download that is not on disk", got)
	}
}
