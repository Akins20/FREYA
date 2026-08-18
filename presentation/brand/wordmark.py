#!/usr/bin/env python3
"""Draw the Freya wordmark and mark, as geometry rather than as type.

    python presentation/brand/wordmark.py

# How it is built

Every letter is a set of strokes, not a hand-traced outline. A stroke is a bar of
fixed thickness between two points, pushed out half a thickness at each end so
that where two bars meet the corner fills square. Letters are unions of
overlapping bars under the nonzero fill rule.

That matters because the first attempt traced each outline by hand and the A came
out inside out. Bars cannot do that: a letter is a description of where the pen
went, and the counters are simply where it did not go.

# The cut

Thin and confident. One hairline weight all the way through, every terminal cut
square, wide open tracking, and no decoration at all. The presence comes from the
spacing and the size rather than from the weight, which is why it holds up set
large on a title card and still reads set small in a corner.

Two earlier attempts are worth recording because they are the obvious traps. A
heavier weight with tight tracking read as a games console logo. Chamfered corners
with stencil breaks between the arms and the stems read as a damaged font rather
than a modern one, because breaks fragment a letter faster than they modernise it.

The one structural idea left is that every diagonal in the word runs at the same
slope: the Y, the leg of the R, and the legs of the A. Nobody names that when they
look at it, but it is why five letters read as one word.

The A has a flat apex, which keeps the top line of the word unbroken.

# The mark

Fehu is the rune for F: a stem with two arms rising to the right. It is the first
letter of her name and it has been an F for about two thousand years, so it reads
as one without needing to be explained. Same weight, same slope.

Nothing here references a font, so nothing has to be installed, and the files
render identically in a browser, in a README and in the film.
"""

import math
from pathlib import Path

OUT = Path(__file__).resolve().parent

TOP, BASE = 0.0, 100.0
H = BASE - TOP

INK = "#12100c"
PAPER = "#ece7df"
AMBER = "#f0a437"

LEAN = 0.30          # horizontal travel per unit of height, for every diagonal


def f(v):
    return ("%.2f" % v).rstrip("0").rstrip(".")


def bar(p0, p1, t):
    """One stroke of thickness t, with both ends pushed out half a thickness.

    The push is what makes a corner between two bars fill square instead of
    leaving a notch on the outside of the turn.
    """
    (x0, y0), (x1, y1) = p0, p1
    dx, dy = x1 - x0, y1 - y0
    L = math.hypot(dx, dy)
    ux, uy = dx / L, dy / L
    x0, y0 = x0 - ux * t / 2, y0 - uy * t / 2
    x1, y1 = x1 + ux * t / 2, y1 + uy * t / 2
    nx, ny = -uy * t / 2, ux * t / 2
    pts = [(x0 + nx, y0 + ny), (x1 + nx, y1 + ny), (x1 - nx, y1 - ny), (x0 - nx, y0 - ny)]
    return "M" + " L".join(f(x) + "," + f(y) for x, y in pts) + " Z"


class Pen:
    def __init__(self, t):
        self.t = t
        self.h = t / 2

    def draw(self, *segs):
        return " ".join(bar(a, b, self.t) for a, b in segs)


# --- the letters ------------------------------------------------------------
#
# Coordinates are centre lines, so a change of weight grows each letter outward
# evenly rather than eating its own counters.

def letter_F(p):
    w = 58 + p.t
    return w, p.draw(
        ((p.h, p.h), (p.h, BASE - p.h)),                 # stem
        ((p.h, p.h), (w - p.h, p.h)),                    # top arm
        ((p.h, H * 0.44), (w - p.h - 13, H * 0.44)))     # middle arm, held short


def letter_R(p):
    w = 62 + p.t
    bowl = H * 0.46
    return w, p.draw(
        ((p.h, p.h), (p.h, BASE - p.h)),
        ((p.h, p.h), (w - p.h, p.h)),
        ((w - p.h, p.h), (w - p.h, bowl)),               # right side of the bowl
        ((p.h, bowl), (w - p.h, bowl)),                  # the bowl closes
        ((w - p.h - (BASE - p.t - bowl) * LEAN, bowl), (w - p.h, BASE - p.h)))


def letter_E(p):
    w = 54 + p.t
    return w, p.draw(
        ((p.h, p.h), (p.h, BASE - p.h)),
        ((p.h, p.h), (w - p.h, p.h)),
        ((p.h, H * 0.44), (w - p.h - 13, H * 0.44)),
        ((p.h, BASE - p.h), (w - p.h, BASE - p.h)))


def letter_Y(p):
    w = 66 + p.t
    cx = w / 2
    join = H * 0.56
    return w, p.draw(
        ((cx - (join - p.h) * LEAN * 1.75, p.h), (cx, join)),
        ((cx + (join - p.h) * LEAN * 1.75, p.h), (cx, join)),
        ((cx, join), (cx, BASE - p.h)))


def letter_A(p):
    w = 66 + p.t
    spread = (BASE - p.t) * LEAN * 0.66
    apexL, apexR = w / 2 - spread / 2, w / 2 + spread / 2
    bar_y = H * 0.72

    def at(x_top, x_bot):
        """Where a leg's centre line sits at the crossbar height."""
        return x_top + (x_bot - x_top) * ((bar_y - p.h) / (BASE - p.t))

    return w, p.draw(
        ((apexL, p.h), (p.h, BASE - p.h)),               # left leg
        ((apexR, p.h), (w - p.h, BASE - p.h)),           # right leg
        ((apexL, p.h), (apexR, p.h)),                    # the flat apex
        ((at(apexL, p.h), bar_y), (at(apexR, w - p.h), bar_y)))


LETTERS = [letter_F, letter_R, letter_E, letter_Y, letter_A]
NAMES = "FREYA"

# Optical spacing. The R ends on a diagonal and the Y is open on both sides, so
# metric spacing leaves holes in the word; these are the corrections, in units.
KERN = {("F", "R"): 0, ("R", "E"): -2, ("E", "Y"): -6, ("Y", "A"): -12}


def wordmark(p, track):
    parts, x = [], 0.0
    for i, make in enumerate(LETTERS):
        lw, d = make(p)
        parts.append('<path d="%s" transform="translate(%s,0)"/>' % (d, f(x)))
        x += lw
        if i < len(LETTERS) - 1:
            x += track + KERN.get((NAMES[i], NAMES[i + 1]), 0)
    return "".join(parts), x


def mark(p):
    """Fehu, on the same slope as the diagonals in the word."""
    rise, run = 30.0, 30.0
    d = p.draw(
        ((p.h, p.h), (p.h, BASE - p.h)),
        ((p.h, rise), (p.h + run, p.h)),
        ((p.h, rise + 30), (p.h + run, 30)))
    return '<path d="%s"/>' % d, run + p.t


def svg(body, w, h, pad=12.0):
    return ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s %s %s %s" '
            'width="%s" height="%s">%s</svg>\n'
            % (f(-pad), f(-pad), f(w + pad * 2), f(h + pad * 2),
               f(w + pad * 2), f(h + pad * 2), body))


def build(t, track, ink):
    p = Pen(t)
    word, ww = wordmark(p, track)
    mk, mw = mark(p)
    gap = 40 + t * 1.2
    lock = ('<g fill="%s">%s</g>' % (AMBER, mk) +
            '<g fill="%s" transform="translate(%s,0)">%s</g>' % (ink, f(mw + gap), word))
    return (svg(lock, mw + gap + ww, BASE),
            svg('<g fill="%s">%s</g>' % (ink, word), ww, BASE),
            svg('<g fill="%s">%s</g>' % (AMBER, mk), mw, BASE),
            ww)


# Thin is the cut. The second is there only because a hairline disappears below
# about fifteen pixels, and something has to survive a favicon.
CUTS = {"thin": (7.0, 30.0), "small": (10.0, 24.0)}


def main():
    for name, (t, track) in CUTS.items():
        lock, word, mk, ww = build(t, track, PAPER)
        (OUT / ("freya-lockup-%s.svg" % name)).write_text(lock, encoding="utf-8")
        (OUT / ("freya-wordmark-%s.svg" % name)).write_text(word, encoding="utf-8")
        (OUT / ("freya-mark-%s.svg" % name)).write_text(mk, encoding="utf-8")
        lock_l, word_l, _, _ = build(t, track, INK)
        (OUT / ("freya-lockup-%s-light.svg" % name)).write_text(lock_l, encoding="utf-8")
        (OUT / ("freya-wordmark-%s-light.svg" % name)).write_text(word_l, encoding="utf-8")
        print("%-6s stroke %4.1f  track %4.1f  word %.0f wide" % (name, t, track, ww))
    emit_for_film()


def emit_for_film():
    """The same geometry, as JavaScript, for the film to draw inline.

    The film needs the mark in every frame and the word on the title card.
    Linking to the SVG files works in a browser and then fails the moment the page
    is opened from disk, and copying the paths across by hand would let the logo in
    the film drift away from the logo in this folder. So it is generated.
    """
    thin = Pen(CUTS["thin"][0])
    small = Pen(CUTS["small"][0])
    word, ww = wordmark(thin, CUTS["thin"][1])
    mk, mw = mark(small)
    lines = [
        "/* Generated by presentation/brand/wordmark.py. Do not edit by hand:",
        "   change the geometry there and run it, or the logo in the film and the",
        "   logo in the brand folder will quietly stop agreeing with each other. */",
        "",
        "const BRAND = {",
        "  word: { d: %s, w: %s, h: %s }," % (js_str(word), f(ww), f(BASE)),
        "  mark: { d: %s, w: %s, h: %s }," % (js_str(mk), f(mw), f(BASE)),
        "  amber: %s, ink: %s," % (js_str(AMBER), js_str(PAPER)),
        "};",
        "",
    ]
    (OUT.parent / "film" / "brand.js").write_text("\n".join(lines), encoding="utf-8")
    print("       film/brand.js written")


def js_str(v):
    return chr(34) + v.replace(chr(34), chr(92) + chr(34)) + chr(34)


if __name__ == "__main__":
    main()
