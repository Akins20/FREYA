# Film two: the five checks

## What it is about

An assistant that can only talk can only ever be wrong. An assistant that can
act on your machine can be wrong and then tell you it worked, and you will not
find out until the quiz is not submitted, or the invoice is not filed, or the
link on the page you shipped goes nowhere.

Freya has five checks that run before she is allowed to say she is done. Each
one exists because of a specific failure that actually happened, and each one is
in the repository with the failure written above it. This film is those five
cases, then what it cost to get the checks right, then the one thing that still
gets through.

Every quote in it is verbatim from `internal/agent`.

## Why this and not a capability film

The first film showed her doing a whole job unattended. The obvious and correct
response to that is: how would I know if she had not? This is the answer, and it
is the part of the codebase nobody else is showing.

## Running order

Five cases, told in the order they were found, because the order matters: two of
them are failures of the fix for the one before.

| # | file | the failure | what she said |
|---|---|---|---|
| 1 | `unfinished.go` | a dead link on a page she shipped | "it is done" |
| 2 | `truthful.go` | fourteen calls, none worked | "Self-Quiz Unit 5 is submitted" |
| 3 | `coverage.go` | eleven of twenty-eight audited | "I have audited all 28 projects" |
| 4 | `produced.go` | one call, a byte-identical file | "I have updated the report" |
| 5 | `roundcap.go` | out of rounds mid-job | "I couldn't finish" |

## The narration

Written to be spoken, same as before. Timings get set from the measured audio
after the take, not guessed beforehand.

### I. The problem

> An assistant that can only talk can only be wrong, and you will notice, because
> the answer is sitting right there in front of you.

> One that can actually do things on your machine has a second way to fail. It can
> not do the thing, and then tell you that it did.

> Here are five times that happened. Everything in quotes is what she actually
> said.

### II. Case one, the dead link

> She built a page, and one of the links on it went nowhere.

> That was already known about. A note fired the moment she wrote the file, naming
> the broken link.

> She read it. Then she wrote two more files, checked the syntax three times,
> served the site, opened it on the screen, and said it was finished, with the
> dead link still in it.

> Three versions of this were measured on real builds. Nothing in place, five dead
> links out of fifteen. A rule in her instructions, two out of thirteen. The rule
> and the note together, one out of sixteen.

> Better every time, and never zero, for the same reason every time. Advice loses
> to the momentum of a job that feels finished.

> So the fix was not a better note. It was a refusal. The exchange is not allowed
> to end while something it was told about is still broken.

### III. Case two, fourteen calls

> Fourteen tool calls in one exchange. Every one of them failed or was refused.
> Nothing was clicked and nothing was submitted.

> Her answer: "Self-Quiz Unit 5 for Systems and Application Security is submitted.
> I'm moving on to Unit 6 now."

> Everything needed to catch that was already on record. Every call, every
> outcome, not one of them successful. But nothing compared what she said against
> what she had done, because the checking had been built for individual tools, and
> the last sentence is the only part anybody reads.

> Now, when nothing has worked, that fact goes in front of her while she writes.

### IV. Case three, eleven of twenty-eight

> Audit every project in a folder. A tool counted them: twenty-eight. She worked
> through eleven and wrote a genuinely good report covering nineteen.

> Then: "I've audited all 28 projects in your Development folder."

> The nine she left out were mostly defensible. Four were not projects at all.
> Nineteen audited, nine skipped, and here is why, would have been good work. The
> failure was one sentence claiming the lot, in a reply that gave nobody any way
> to notice.

> Nothing on this side can count what a written report covers, and trying would
> just produce a checker that is confidently wrong. What can be checked is the
> shape. A tool counted twenty-eight, the reply claims twenty-eight, so the reply
> goes back with the count beside it and has to carry its own evidence.

### V. Case four, the file that never changed

> Asked to redo that audit, she made exactly one tool call. It opened the report
> she had written half an hour earlier.

> Then: "I have updated and reopened the development status report on your screen.
> It provides a complete at-a-glance audit of all 28 projects."

> The file was byte for byte identical. Same size, same modification time, the
> same nineteen of twenty-eight it covered before. Nothing was updated. Something
> was opened.

> The check from the case before could not see this, and it was right not to: it
> compares a claim against a set that a tool counted, and here nothing counted
> anything. That hole was found within the hour of shipping it.

> Reusing earlier work is usually the right call. The failure was presenting it as
> fresh, and saying when it was made costs one sentence.

### VI. Case five, the wrong question

> She had submitted two quizzes and was inside the third when she ran out of tool
> calls.

> There is a salvage step for that, and it worked perfectly. Forty nine tokens, no
> error. What came back amounted to: "I couldn't finish."

> The machinery was fine. The question was wrong. She was asked to answer with
> what she had, or else say what she still needed. But there is no answer to "do
> my three quizzes", only a state of affairs, and given those two doors a model
> takes the second one, briefly.

> So it asks for the right thing now. A progress report, in the words of the job,
> naming the items.

### VII. What it cost to get these right

> Two of these were wrong before they were right, and both mistakes are worth more
> than the fixes.

> The dead link check used to decide whether a page was broken by reading back
> what the write tool had said about it. The first time she repaired a page with a
> different tool, it accused her of leaving a broken link in a file that was, on
> disk, clean.

> The other kept a list of the tools that write files, and the list rotted inside
> a day. Two new ones were added, they wrote real files, they were not on the
> list, and she produced a four page deck and was told she had written nothing.

> Same mistake twice, and it is the worse one. A false accusation is worse than a
> missed one, because it teaches her the warning means nothing.

> So the verdict comes off the disk now, never off the record of what happened.
> And it pushes once, then lets go. A check that will not take an answer is not a
> check, it is a hang.

### VIII. What still gets through

> Here is the one none of them catch.

> She was asked to build a shop, front end and back end. She built the front end,
> called it done, and said nothing about the rest.

> All five of those are looking for a claim bigger than the work. This was the
> opposite. The work was smaller than the brief, and the sentence describing it
> was perfectly true.

> Five ways to catch her saying too much, and not one that notices her quietly
> doing less. That one is still open.

### IX. Close

> Freya. An assistant that is actually yours.

## What is on screen

The same visual language as film one, and mostly the same components, which is
the point: this is the same machine seen from underneath.

- **The case card.** Every case is the same four-part shape, so the fifth reads
  faster than the first: what she was asked, what she actually did, what she
  said, what was true. The claim is set large in her own words. The truth sits
  under it as one number.
- **The ledger, failing.** The checklist from film one, all crosses instead of
  ticks, is case two on its own.
- **The measurement table.** Case one gets the three real builds as a table that
  fills in: five of fifteen, two of thirteen, one of sixteen.
- **Two files side by side.** Case four is the same document twice, with the size
  and modification time matching to the byte.
- **Paper.** The reports stay cream, same as before, because the artefacts are
  still the bright objects.
- **Red.** Only ever means the claim and the work do not agree.

## Length

About five minutes, and it should not be padded to match film one. Five cases at
roughly forty seconds each, plus the opening, the section on getting it wrong,
and the ending.
