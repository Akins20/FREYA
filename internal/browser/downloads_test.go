package browser

import (
	"encoding/json"
	"strings"
	"testing"
)

// The question she could not answer: is it downloading, did it finish, did it
// fail? The old tool read the user's real Chrome History file — wrong profile,
// written lazily, and containing only finished transfers. In the Drive run it
// returned entries from three weeks earlier.
func TestADownloadHasALifecycleSheCanSee(t *testing.T) {
	c := &Client{downloads: newDownloads(), events: &eventLog{}}

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
