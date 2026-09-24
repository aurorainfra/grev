# trv

**tr, but only where it makes sense.** trv edits text like `tr` (translate, delete, squeeze) or
`sed s///` (replace regex matches), but only where the model says your instruction applies to
each occurrence. Code finds the candidates: runs of `SET` characters, or matches of `-e REGEX`.
For each one, the model sees it marked `⟦like this⟧` in its line and answers yes or no, or picks
a replacement from your list. Everything else is copied byte for byte. The model never writes
text; replacements come from `SET2`, `-r` or `-o`.

## Example: quote marks, but not apostrophes

`tr "'" '"'` can't tell a quotation mark from the apostrophe in "don't". trv can:

```console
$ cat examples/trv-story.txt
She looked up and said 'don't go yet' before the train left.
It's John's car, and the dog's bowl is in the back.
The sign read 'closed for lunch' but the door wasn't locked.
$ trv -p "'" '"' 'is used as a quotation mark, not an apostrophe' < examples/trv-story.txt
She looked up and said "don't go yet" before the train left.
It's John's car, and the dog's bowl is in the back.
The sign read "closed for lunch" but the door wasn't locked.
trv: 9 q · 1 req · 1,080 tok · $0.000045 · 0.6s · jev-1.13.0
```

- **What happened:** each of the nine `'` characters became one question, all sent in one
  request. The four quotation marks were translated; the five apostrophes (don't, It's, John's,
  dog's, wasn't) were kept.

## More examples

Doubled commas. `-s` squeezes a run of a repeated character to one. It fixes the typos in prose
but leaves the CSV rows, where `,,` is an empty field. Single characters are never asked about:

```console
$ trv -s , 'the repeated comma is a typo in prose, not an empty field in a CSV row' < examples/trv-notes.txt
Meeting notes: we agreed on the budget, the timeline and the hires.
id,name,,email
7,Ada,,ada@example.com
Next steps: send the draft, then book the room.
```

Pick a replacement per match with `-o`: `St.` becomes Saint or Street depending on context. The
model may also answer `keep` when no option fits:

```console
$ trv -e '\bSt\.' -o 'Saint|Street' 'what St. abbreviates here' < examples/trv-addresses.txt
Saint Mary's Church, 14 High Street, Oxford
Services at Saint Paul's on Queen Street start at 10.
The Saint Louis office moved to 5th Street last spring.
```

Replace with fixed text (`$1` and `${name}` expand), only in prose and not in code. `--trace` shows
each decision:

```console
$ trv --trace -e '\bcolou?r\b' -r colour 'is in English prose, not in code' < examples/trv-colour.txt
trv: 1:11 ⟦colour⟧ p=0.61 → changed
trv: 1:57 ⟦color⟧ p=0.60 → changed
trv: 2:14 ⟦color⟧ p=0.28 → kept
Our brand colour is a deep teal; the logo uses the same colour in every region.
The CSS sets color: #0b6e6e on headings.
```

Tone it down where the tone calls for it:

```console
$ printf 'Thanks!!! The fix works!!!\nERROR!!! disk full!!!\n' | trv -s '!' 'is excessive punctuation in a friendly message'
Thanks! The fix works!
ERROR!!! disk full!!!
```

## Modes and options

| form | does |
|---|---|
| `trv SET1 SET2 INSTRUCTION` | translate the chosen occurrences character by character (a shorter SET2 repeats its last character, as in tr) |
| `trv -d SET INSTRUCTION` | delete the chosen runs of SET characters |
| `trv -s SET INSTRUCTION` | squeeze the chosen runs of one repeated character (`,,` → `,`) |
| `trv -e REGEX -r TEXT INSTRUCTION` | replace the chosen matches with TEXT (`$1`, `${name}`) |
| `trv -e REGEX -o 'A\|B' INSTRUCTION` | let the model pick A, B, … (or keep) per match |
| `trv -e REGEX -d INSTRUCTION` | delete the chosen matches |

| option | meaning |
|---|---|
| `-t P` | act when P(yes) ≥ P (default 0.5); with `-o`, the minimum confidence |
| `--about TEXT`, `-S FILE` | what the text is, or a style guide to follow |
| `-W N` | also show the model N neighbouring lines |
| `--context CHARS` | how much of the line to show around each occurrence (default 160) |
| `--trace` | print each occurrence, its answer and the decision on stderr |
| `-i FILE` | read FILE instead of stdin |

SET is tr-style: characters, ranges (`a-z`), escapes (`\n`, `\t`, `\\`) and classes (`[:punct:]`,
`[:space:]`, `[:digit:]`, `[:alpha:]`, `[:alnum:]`, `[:upper:]`, `[:lower:]`). See `man trv`.

**Phrasing.** An INSTRUCTION can be a condition on the marked text ("is a typo", "is used as a
quotation mark") or an imperative ("remove doubled commas"). Use `{}` to name the occurrence
explicitly.

## Exit status

0 something changed · 1 nothing changed · 2 error · 4 declined at `-Q` or over a budget.

## See also

- [grev](grev.md): find the lines, instead of editing inside them.
- [pickv](pickv.md): extract the one regex match that answers a question.
- [unwrap](unwrap.md): rejoin hard-wrapped lines.
- [DESIGN](../DESIGN.md): why the instruction goes first in the question.
