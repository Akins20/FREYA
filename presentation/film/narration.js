/* The voice track.
 *
 * Written to be spoken by a person who is watching this with you, not to be read
 * off a card. That means whole sentences that carry a thought across a clause or
 * two, the occasional aside, and connective tissue between the ideas. Clipped
 * three-word fragments stacked for rhythm read as advertising copy, and they are
 * the fastest way to make a film sound like it was written by a machine.
 *
 * `at` is milliseconds from the start of that scene, and the scene durations in
 * scenes.js are set to fit these lines rather than the other way round. A line
 * runs at roughly two and a half words a second when it is read at a documentary
 * pace, which is what the timings assume.
 *
 * narrate.py reads this file, synthesises each line with Gemini and lays them out
 * on the same clock, so the subtitle and the voice cannot drift apart.
 */

const NARRATION = [
  /* I. the ask */
  { scene: 0, at:  800, text: "This is a Tuesday morning, and what you're looking at is the whole of the instruction." },
  { scene: 0, at: 7800, text: "One sentence, typed the way you'd type it yourself, and after that nobody touches the keyboard." },

  /* II. the errand */
  { scene: 1, at:  600, text: "The first thing she does is ask a question, because there are five Chrome profiles on this machine and two of them have mail signed in." },

  { scene: 2, at:  600, text: "She opens the one you meant and works inside it, signed in exactly as you already are, and from here on everything she does goes onto that list on the left." },

  { scene: 3, at:  600, text: "Your mail is just a website, so she opens it the way you would, and finds the one that came in at twenty to eight with two files hanging off it." },

  { scene: 4, at:  600, text: "Those come down onto your own disk, into your downloads folder, where you can open them yourself. Nothing has gone into anybody's cloud." },

  { scene: 5, at:  700, text: "Then she reads the statement, and I mean actually reads it, line by line, rather than making a decent guess at what a supplier invoice usually says." },

  { scene: 6, at:  700, text: "This is the part that earns its keep. She lays their numbers next to ours, and on line four the two don't agree." },
  { scene: 6, at: 9600, text: "It's two hundred and forty pounds more than we signed off, which you would otherwise have found on Thursday, halfway through the call." },

  { scene: 7, at:  700, text: "So she writes it up as a Word document, with a heading and a table and the bad line marked in red, because a wall of plain text would only put the work back on you." },
  { scene: 7, at: 10200, text: "It could just as easily have been a spreadsheet, or a PDF, if that's what the job needed." },

  { scene: 8, at:  700, text: "Then it goes into the shared drive where the rest of the team looks, uploaded from the same browser and the same account, with no copy of it left lying anywhere else." },

  { scene: 9, at:  700, text: "And the call goes in the diary for Thursday at four, with the summary attached to the invitation, so nobody has to go hunting for it on the day." },

  { scene: 10, at:  700, text: "Then she tells you what she did, in the same window you asked in, and she tells you about the line she couldn't settle rather than quietly rounding it off and hoping." },
  { scene: 10, at: 11400, text: "Eleven minutes, start to finish, and you weren't at the desk for any of it." },

  /* III. and it holds up on its own */
  { scene: 12, at:  700, text: "None of it rests on a good day, either. The browsing was tested on its own, fifteen jobs across eight real websites, and the awkward ones were left in rather than swapped for something easier." },
  { scene: 12, at: 10600, text: "All fifteen came back with an answer, and not one of them got stuck or gave up halfway." },

  { scene: 13, at:  700, text: "She isn't only useful inside a browser. Every program on your machine is built differently underneath, and she learned to work all of them." },
  { scene: 13, at: 8800, text: "And we didn't take her word for that. We asked the programs, and each of them wrote down what she had done to it." },

  /* IV. why you can leave it alone */
  { scene: 14, at:  700, text: "She remembers, too, and not in the way a chat window remembers for ten minutes. Months of it, and she can go back and find the part that matters." },

  { scene: 15, at:  700, text: "And when you're nowhere near the desk, she's still going. Quietly, mostly, because an assistant that tells you everything gets muted inside a day." },

  { scene: 16, at:  700, text: "There's no account to make and no subscription to cancel, and nobody else is holding your files while you work." },
  { scene: 16, at: 7000, text: "It's one program, on your own machine, written and run on a laptop from twenty fourteen." },

  { scene: 17, at: 1400, text: "Freya. An assistant that's actually yours." },
];
