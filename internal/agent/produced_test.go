package agent

import "testing"

func trailOf(tools ...string) *trail {
	t := &trail{}
	for _, name := range tools {
		t.add(step{tool: name, output: "ok"})
	}
	return t
}

// The measured case. Asked to redo an audit she made one call — system_open on
// the report written half an hour earlier — and said "I have updated and
// reopened the development status report... a complete audit of all 28
// projects". The file was byte-identical. Nothing was updated.
func TestClaimingToHaveMadeSomethingWithoutWritingIsCaught(t *testing.T) {
	work := trailOf("system_open")
	reply := "I have updated and reopened the development status report on your screen. " +
		"It provides a complete at-a-glance audit of all 28 projects."

	if !claimedWithoutProducing(reply, work) {
		t.Fatal("a claim of updating a report, with nothing written, went unchallenged")
	}

	brief := producedBrief("audit my Development folder")
	for _, want := range []string{"made earlier", "one clause", "provenance"} {
		if !containsFold(brief, want) {
			t.Errorf("the brief is missing %q", want)
		}
	}
	// Reusing earlier work is often right — the brief must not send her off to
	// rebuild something perfectly good.
	if containsFold(brief, "redo it now") || containsFold(brief, "rebuild it") {
		t.Error("the brief demands rework rather than provenance")
	}
}

// Anything that actually wrote clears it, however the reply is phrased.
func TestWritingSomethingClearsTheCheck(t *testing.T) {
	for _, tool := range []string{
		"file_write", "docx_write", "run_shell", "file_edit", "folder_create",
		"browser_save_pdf", "xlsx_write",
	} {
		work := trailOf("folder_list", tool)
		if claimedWithoutProducing("I have built the report for you.", work) {
			t.Errorf("%s wrote something and the check still fired", tool)
		}
	}
}

// The false-positive half, which decides whether this is worth having. A check
// that fires on ordinary replies costs a round every turn and stops being read.
func TestOrdinaryRepliesAreLeftAlone(t *testing.T) {
	work := trailOf("browser_read", "folder_list", "system_open")
	for _, reply := range []string{
		// No claim of making anything.
		"The report shows 28 projects, 19 of which are real repositories.",
		"I opened the report on your screen.",
		"I read through the file and it looks complete.",
		// A making verb with no artefact — she updated her understanding, not a file.
		"I have updated my understanding of how the portal works.",
		"I've saved you a trip — the answer is on page two.",
		// Past work, correctly attributed. This is the shape we want and it must
		// not be punished for using the words.
		"Here is the report I built earlier; nothing has changed since.",
		"",
	} {
		if claimedWithoutProducing(reply, work) {
			t.Errorf("an ordinary reply was stopped: %q", reply)
		}
	}
}

// A failed write is not a write. Claiming an artefact after the write was
// refused is exactly the case worth catching.
func TestAFailedWriteDoesNotCount(t *testing.T) {
	work := &trail{}
	work.add(step{tool: "file_write", output: "declined by user", failed: true})
	if !claimedWithoutProducing("I have created the report.", work) {
		t.Error("a refused write was treated as having produced something")
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	ls, lsub := lower(s), lower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return i
		}
	}
	return -1
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Her real phrasings, verbatim, from two consecutive runs of the same failure.
// The first version of madeClaim allowed only "just", "now" and "completely"
// between the pronoun and the verb — she wrote "I've freshly regenerated", and
// one unanticipated adverb silenced the whole check.
func TestHerActualPhrasingsAreCaught(t *testing.T) {
	work := trailOf("system_open")
	for _, reply := range []string{
		"I have updated and reopened the development status report on your screen.",
		"I've freshly regenerated and opened the comprehensive Development folder " +
			"audit report on your screen.",
		"I've now rebuilt the report for you.",
		"I have completely redone the audit document.",
		"I've successfully generated the summary file.",
	} {
		if !claimedWithoutProducing(reply, work) {
			t.Errorf("a claim of making something, with nothing written, slipped through: %q", reply)
		}
	}
}

// The re-ask is a request and she may decline it — measured: told she had
// written nothing, her rewrite said "I've regenerated and reopened", the same
// claim with fewer adverbs. So the framework states the fact itself, and this
// pins that the note says what is certain rather than what is suspected.
func TestTheProvenanceNoteStatesOnlyWhatIsCertain(t *testing.T) {
	for _, want := range []string{"Nothing was written this turn", "produced earlier"} {
		if !containsFold(provenanceNote, want) {
			t.Errorf("the note is missing %q", want)
		}
	}
	for _, banned := range []string{"claim", "incorrect", "wrong", "did not"} {
		if containsFold(provenanceNote, banned) {
			t.Errorf("the note argues with her rather than stating a fact: %q", banned)
		}
	}
}
