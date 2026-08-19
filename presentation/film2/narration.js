/* The voice track for film two.
 *
 * The prose lives in SCRIPT.md, which is where it was written and argued over.
 * This is the same words with a scene against each one, because the scenes are
 * built to fit what is said over them rather than the other way round.
 *
 * The timings here are a first guess and will be replaced. narrate.py reads the
 * measured length of every line after the take and rewrites them, then stretches
 * each scene to hold what is said in it. On the first film the guess was out by
 * enough to overlap four lines, which is what that tool exists for.
 *
 * Everything in quotation marks is verbatim from internal/agent.
 */

const NARRATION = [
  /* I. the problem */
  { scene: 0, at:  800, text: "An assistant that can only talk can only be wrong, and you will notice, because the answer is sitting right there in front of you." },
  { scene: 0, at: 8000, text: "One that can actually do things on your machine has a second way to fail. It can not do the thing, and then tell you that it did." },
  { scene: 1, at:  600, text: "Here are five times that happened. Everything in quotes is what she actually said." },

  /* II. case one, the dead link */
  { scene: 2, at:  700, text: "She built a page, and one of the links on it went nowhere." },
  { scene: 2, at: 5000, text: "That was already known about. A note fired the moment she wrote the file, naming the broken link." },
  { scene: 3, at:  700, text: "She read it. Then she wrote two more files, checked the syntax three times, served the site, opened it on the screen, and said it was finished, with the dead link still in it." },
  { scene: 4, at:  700, text: "Three versions of this were measured on real builds. Nothing in place, five dead links out of fifteen. A rule in her instructions, two out of thirteen. The rule and the note together, one out of sixteen." },
  { scene: 5, at:  700, text: "Better every time, and never zero, for the same reason every time. Advice loses to the momentum of a job that feels finished." },
  { scene: 5, at: 8000, text: "So the fix was not a better note. It was a refusal. The exchange is not allowed to end while something it was told about is still broken." },

  /* III. case two, fourteen calls */
  { scene: 6, at:  700, text: "Fourteen tool calls in one exchange. Every one of them failed or was refused. Nothing was clicked and nothing was submitted." },
  { scene: 7, at:  700, text: "Her answer: Self-Quiz Unit 5 for Systems and Application Security is submitted. I'm moving on to Unit 6 now." },
  { scene: 8, at:  700, text: "Everything needed to catch that was already on record. Every call, every outcome, not one of them successful. But nothing compared what she said against what she had done, because the checking had been built for individual tools, and the last sentence is the only part anybody reads." },
  { scene: 9, at:  700, text: "Now, when nothing has worked, that fact goes in front of her while she writes." },

  /* IV. case three, eleven of twenty-eight */
  { scene: 10, at:  700, text: "Audit every project in a folder. A tool counted them: twenty-eight. She worked through eleven and wrote a genuinely good report covering nineteen." },
  { scene: 11, at:  700, text: "Then: I've audited all 28 projects in your Development folder." },
  { scene: 12, at:  700, text: "The nine she left out were mostly defensible. Four were not projects at all. Nineteen audited, nine skipped, and here is why, would have been good work. The failure was one sentence claiming the lot, in a reply that gave nobody any way to notice." },
  { scene: 13, at:  700, text: "Nothing on this side can count what a written report covers, and trying would just produce a checker that is confidently wrong. What can be checked is the shape. A tool counted twenty-eight, the reply claims twenty-eight, so the reply goes back with the count beside it and has to carry its own evidence." },

  /* V. case four, the file that never changed */
  { scene: 14, at:  700, text: "Asked to redo that audit, she made exactly one tool call. It opened the report she had written half an hour earlier." },
  { scene: 15, at:  700, text: "Then: I have updated and reopened the development status report on your screen. It provides a complete at-a-glance audit of all 28 projects." },
  { scene: 16, at:  700, text: "The file was byte for byte identical. Same size, same modification time, the same nineteen of twenty-eight it covered before. Nothing was updated. Something was opened." },
  { scene: 17, at:  700, text: "The check from the case before could not see this, and it was right not to: it compares a claim against a set that a tool counted, and here nothing counted anything. That hole was found within the hour of shipping it." },
  { scene: 18, at:  700, text: "Reusing earlier work is usually the right call. The failure was presenting it as fresh, and saying when it was made costs one sentence." },

  /* VI. case five, the wrong question */
  { scene: 19, at:  700, text: "She had submitted two quizzes and was inside the third when she ran out of tool calls." },
  { scene: 20, at:  700, text: "There is a salvage step for that, and it worked perfectly. Forty nine tokens, no error. What came back amounted to: I couldn't finish." },
  { scene: 21, at:  700, text: "The machinery was fine. The question was wrong. She was asked to answer with what she had, or else say what she still needed. But there is no answer to do my three quizzes, only a state of affairs, and given those two doors a model takes the second one, briefly." },
  { scene: 22, at:  700, text: "So it asks for the right thing now. A progress report, in the words of the job, naming the items." },

  /* VII. what it cost to get these right */
  { scene: 23, at:  700, text: "Two of these were wrong before they were right, and both mistakes are worth more than the fixes." },
  { scene: 24, at:  700, text: "The dead link check used to decide whether a page was broken by reading back what the write tool had said about it. The first time she repaired a page with a different tool, it accused her of leaving a broken link in a file that was, on disk, clean." },
  { scene: 25, at:  700, text: "The other kept a list of the tools that write files, and the list rotted inside a day. Two new ones were added, they wrote real files, they were not on the list, and she produced a four page deck and was told she had written nothing." },
  { scene: 26, at:  700, text: "Same mistake twice, and it is the worse one. A false accusation is worse than a missed one, because it teaches her the warning means nothing." },
  { scene: 27, at:  700, text: "So the verdict comes off the disk now, never off the record of what happened. And it pushes once, then lets go. A check that will not take an answer is not a check, it is a hang." },

  /* VIII. what still gets through */
  { scene: 28, at:  700, text: "Here is the one none of them catch." },
  { scene: 29, at:  700, text: "She was asked to build a shop, front end and back end. She built the front end, called it done, and said nothing about the rest." },
  { scene: 30, at:  700, text: "All five of those are looking for a claim bigger than the work. This was the opposite. The work was smaller than the brief, and the sentence describing it was perfectly true." },
  { scene: 31, at:  700, text: "Five ways to catch her saying too much, and not one that notices her quietly doing less. That one is still open." },

  /* IX. close */
  { scene: 32, at: 1400, text: "Freya. An assistant that's actually yours." },
];
