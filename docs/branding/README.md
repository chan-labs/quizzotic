# Quizzotic branding (draft)

> **Draft for the maintainer to choose from.** Three independent directions, none adopted. Nothing else in the repository references these files.

Each option contains:

- `logomark.svg` / `logomark-dark.svg`: square icon (64x64 viewBox), legible from 16 px to 512 px.
- `logo.svg` / `logo-dark.svg`: mark plus the "Quizzotic" wordmark (260x64 viewBox).

The `-dark` files are for dark backgrounds. All files are flat, hand-written SVG with a `viewBox` and a `<title>`, no external fonts or images.

**Wordmark note:** the text is live SVG text using the system font stack `Inter, 'Segoe UI', system-ui, -apple-system, Helvetica, Arial, sans-serif` (bold), with `textLength` pinned so the width stays stable across fonts. It is not converted to paths; do that (or pick a typeface) once an option is chosen.

## Options

### A. Question loop

A rounded indigo tile with a white question mark whose hook closes into a loop, an amber dot, and a dotted forgetting curve running behind it. Says "quiz" first, "recall over time" second.

Colors: indigo `#4338CA`, white `#FFFFFF`, amber `#FBBF24`, curve `#8B85F0`; wordmark `#1E1B4B`. Dark: tile `#E0E7FF`, mark `#312E81`, dot `#B45309`; wordmark `#E0E7FF`.

### B. Card stack

Three stacked flashcards with a teal check on the front card and an amber spark (the "-otic" quirk). Most literal, most playful; the dark variant swaps to a navy front card.

Colors: ink `#0F172A`, teal `#0F766E` / `#5EEAD4` / `#99F6E4`, spark `#F59E0B`; wordmark `#0F172A`. Dark: ink `#E2E8F0`, front card `#0F172A`, check `#5EEAD4`, spark `#FBBF24`; wordmark `#E2E8F0`.

### B-curve. Card stack with a forgetting-curve easter egg

Option B, unchanged in silhouette, plus a thin forgetting curve (steep exponential decay flattening out, the Ebbinghaus shape) drawn on the front card behind the check. It is a 1.6-unit line in a pale teal, so at 16-32 px it disappears into the card and the mark reads exactly as B; at larger sizes it shows up as a quiet nod to spaced repetition. Same colors as B, with the curve in `#5EEAD4` (light) and `#0F766E` (dark). Files live in `option-b-curve/`.

### C. Review Q

A bold monogram Q in a single color. The tail is an arrow, so the letter itself reads as "go again". Strongest as a favicon and app icon; one color keeps it easiest to reproduce.

Colors: burnt orange `#C2410C`; wordmark `#7C2D12`. Dark: `#FB923C`; wordmark `#FFEDD5`.

## Preview

| Option | Logomark | Logo | Logomark (dark) | Logo (dark) |
| --- | --- | --- | --- | --- |
| A | <img src="option-a/logomark.svg" width="64" alt="Option A logomark"> | <img src="option-a/logo.svg" width="220" alt="Option A logo"> | <img src="option-a/logomark-dark.svg" width="64" alt="Option A logomark, dark"> | <img src="option-a/logo-dark.svg" width="220" alt="Option A logo, dark"> |
| B | <img src="option-b/logomark.svg" width="64" alt="Option B logomark"> | <img src="option-b/logo.svg" width="220" alt="Option B logo"> | <img src="option-b/logomark-dark.svg" width="64" alt="Option B logomark, dark"> | <img src="option-b/logo-dark.svg" width="220" alt="Option B logo, dark"> |
| B-curve | <img src="option-b-curve/logomark.svg" width="64" alt="Option B-curve logomark"> | <img src="option-b-curve/logo.svg" width="220" alt="Option B-curve logo"> | <img src="option-b-curve/logomark-dark.svg" width="64" alt="Option B-curve logomark, dark"> | <img src="option-b-curve/logo-dark.svg" width="220" alt="Option B-curve logo, dark"> |
| C | <img src="option-c/logomark.svg" width="64" alt="Option C logomark"> | <img src="option-c/logo.svg" width="220" alt="Option C logo"> | <img src="option-c/logomark-dark.svg" width="64" alt="Option C logomark, dark"> | <img src="option-c/logo-dark.svg" width="220" alt="Option C logo, dark"> |

The dark variants have transparent backgrounds, so on a light page (such as GitHub's light theme) they appear washed out in this table; view them on a dark background.
