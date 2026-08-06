package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Getting files in and out of the browser.
//
// # The two doors that were both shut
//
// Downloading and uploading are the two places a web page hands over to the
// operating system, and both were closed to her for the same reason: the window
// that opens is drawn by the OS, not the page. No selector reaches it, no click
// lands on it, and — worse — she could not even tell it was there. A click on
// Upload looked exactly like a click that did nothing, so the sensible next move
// was to click again, and now there are two dialogs.
//
// Neither needs the dialog to exist at all.
//
// A download is redirected to a known folder by telling the browser where to put
// things (see Watch), so the file simply appears and an event says when.
//
// An upload is set directly on the input element. The file chooser exists to
// discover which path the user meant; when the path is already known there is
// nothing to discover, and CDP will set it without ever opening the window.
// That is not a trick — it is the same API the browser's own automation uses,
// and it is the only route that works at all.

// UploadFiles attaches local files to a file input without opening a chooser.
//
// The selector must be the <input type=file> itself, which is very often
// invisible: sites hide it behind a styled label or a drop zone. That is fine —
// this does not click it, so visibility does not matter, which is exactly why it
// works where clicking the pretty button does not.
func (c *Client) UploadFiles(ctx context.Context, selector string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("give at least one file to upload")
	}
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		full, err := filepath.Abs(strings.TrimSpace(p))
		if err != nil {
			return fmt.Errorf("resolve %q: %w", p, err)
		}
		// Checked here rather than left to the browser, which reports a missing
		// file as a generic failure that says nothing about which one.
		if _, err := os.Stat(full); err != nil {
			return fmt.Errorf("cannot upload %s: %w", full, err)
		}
		abs = append(abs, full)
	}

	if _, err := c.Call(ctx, "DOM.enable", nil); err != nil {
		return err
	}
	doc, err := c.Call(ctx, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		return err
	}
	var root struct {
		Root struct {
			NodeID float64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(doc, &root); err != nil {
		return err
	}

	found, err := c.Call(ctx, "DOM.querySelector", map[string]any{
		"nodeId": root.Root.NodeID, "selector": selector,
	})
	if err != nil {
		return err
	}
	var node struct {
		NodeID float64 `json:"nodeId"`
	}
	if err := json.Unmarshal(found, &node); err != nil || node.NodeID == 0 {
		return fmt.Errorf("no file input matches %q. It is often hidden behind a styled "+
			"button — inspect the page for input[type=file] and use that selector, not the "+
			"button's", selector)
	}

	if _, err := c.Call(ctx, "DOM.setFileInputFiles", map[string]any{
		"files": abs, "nodeId": node.NodeID,
	}); err != nil {
		return fmt.Errorf("attaching files to %q: %w", selector, err)
	}
	return nil
}

// ReadClipboard returns what is on the system clipboard.
//
// Clipboards are how a person carries something between two places that have no
// other connection — copy a share link here, paste it into a message there. She
// had no way to do either, so any task shaped like that ended at the first step.
//
// Reading needs both permission and focus, and the page must be the active one;
// the error says which is missing rather than returning empty and letting the
// caller assume the clipboard was bare.
func (c *Client) ReadClipboard(ctx context.Context) (string, error) {
	if _, err := c.Call(ctx, "Page.bringToFront", nil); err != nil {
		return "", err
	}
	raw, err := c.EvalString(ctx, `navigator.clipboard.readText()`)
	if err != nil {
		return "", fmt.Errorf("cannot read the clipboard: %w — the page must be focused "+
			"and clipboard-read permission granted", err)
	}
	return raw, nil
}

// WriteClipboard puts text on the system clipboard.
func (c *Client) WriteClipboard(ctx context.Context, text string) error {
	if _, err := c.Call(ctx, "Page.bringToFront", nil); err != nil {
		return err
	}
	expr := fmt.Sprintf(`navigator.clipboard.writeText(%q).then(() => 'ok')`, text)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return fmt.Errorf("cannot write the clipboard: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("the clipboard write did not take effect")
	}
	return nil
}

// GrantPermissions pre-approves the things a page would otherwise stop and ask
// for.
//
// A permission prompt is Chrome's own UI — not page content — so it cannot be
// clicked, and a page waiting behind one simply never finishes loading. She would
// read a half-built page and draw conclusions from it. Granting up front turns a
// dead end into a non-event.
//
// Clipboard access is included because that is what makes ReadClipboard work at
// all; the rest are the ones that commonly gate an application's main flow.
func (c *Client) GrantPermissions(ctx context.Context, origin string) error {
	_, err := c.Call(ctx, "Browser.grantPermissions", map[string]any{
		"origin": origin,
		"permissions": []string{
			"clipboardReadWrite", "clipboardSanitizedWrite",
			"notifications", "geolocation", "midi", "backgroundSync",
		},
	})
	return err
}
