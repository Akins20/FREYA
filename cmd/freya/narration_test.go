package main

import (
	"os"
	"strings"
	"testing"
)

// The lines she actually read aloud, verbatim from the run that prompted this.
//
// One request — rearrange my Downloads and rename some files — and the user was
// spoken the model's entire working-out: tool names, self-argument, a numbered
// plan, thirty lines of it addressed to nobody.
func TestTheLinesSheReadAloudAreSilencedNow(t *testing.T) {
	for _, said := range []string{
		"Wait, let's check if there are other images or files. Let's list all files in ~/Downloads again or check if there's any other un-named or UUID-named file.",
		"Let's use `file_move` to rename `1000842027.jpg` to `Lobby_Event_Photo.jpg`, and remove the stale files.",
		"Now let's remove the duplicate UUID PDF (`7fb2d01e-a47d-4af8-a51e-ccd081286d0e`) and the stale `.crdownload` files using `file_delete`.",
		"Let's check the folder list output again:",
		"1. Rename `1000842027.jpg` to `Lobby_Event_Photo.jpg` (or `Elijah_Google_Drive_Photo_1.jpg`).",
		"- `12476f2f-5776-43ff-be29-d254c4b637df.crdownload` (incomplete download)",
		"Let's do this cleanly using `file_move` or `run_shell`.",
		"I need to figure out what tools I have at my disposal.",
		"Okay, so the user wants me to clean up their Downloads folder.",
	} {
		if line, ok := speakableNarration(said); ok {
			t.Errorf("still spoken: %q", line)
		}
	}
}

// And the thing the channel exists for still gets said, or voice mode goes back
// to silence while she works and the fix has cost more than it saved.
func TestARealAsideIsStillSpoken(t *testing.T) {
	for _, said := range []string{
		"On it, opening your portal.",
		"Give me a second, this one is slow.",
		"Looking that up now.",
		"Found it — reading the page.",
	} {
		if _, ok := speakableNarration(said); !ok {
			t.Errorf("silenced a genuine aside: %q", said)
		}
	}
}

// Silence is the default for anything it cannot vouch for: a spoken line cannot
// be taken back, and the cost of missing one is a few seconds of quiet.
func TestTheDoubtfulCaseIsSilence(t *testing.T) {
	for _, edge := range []string{"", "   ", "\n", "a\nb", strings.Repeat("x", 200)} {
		if _, ok := speakableNarration(edge); ok {
			t.Errorf("spoke a doubtful line: %q", edge)
		}
	}
}

// Both voice paths must use the filter. There were two wirings of OnInterim that
// spoke, and fixing one would leave the other reading her notes out.
func TestBothVoicePathsFilterWhatTheySpeak(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if n := strings.Count(body, "speakableNarration(text)"); n != 2 {
		t.Errorf("%d of the 2 voice paths filter what they speak", n)
	}
	// Scoped to the OnInterim wirings. d.Speak hands the synthesiser a
	// notification the daemon composed itself — deliberate outbound speech, and
	// correctly raw — so matching every Speak call in the file would flag the one
	// place that should not be filtered.
	for _, block := range strings.Split(body, "a.OnInterim = func(text string) {")[1:] {
		body := block[:strings.Index(block, "\n\t\t}")]
		if strings.Contains(body, "Speak(context.Background(), text)") {
			t.Errorf("an OnInterim wiring still speaks the raw text:\n%s", body)
		}
	}
}
