# Design

## The model

TypeSafe's System One models (Jev) take a **state** (text or JSON) and a map of typed
**questions**, and return one typed answer per question:

| question | answer | notes |
|---|---|---|
| Noul | P(yes) in 0..1 | absolute: can be low for every candidate |
| Choice | one of ≤255 options, probabilities that sum to 1, confidence | relative: some option always wins |
| Score | a position along 2–10 ordered levels, probabilities, confidence | the position is already probability-weighted |

Questions in one request are evaluated independently and in parallel, so packing many questions
into a request changes cost and latency, not answers. A request carries about 262 tokens of framing
plus about 7 per question, plus the text. Input costs $0.042 per million tokens and output is free.

Jev can't generate text. The grev tools lean into that and act like classic filters. They
select, reorder, split, route or annotate input, so **output is input**. Model labels and
probabilities appear only as explicit columns. There are two deliberate normalizations: CRLF comes
out as LF, and `--para` separates paragraphs with one blank line. `cutv` re-encodes CSV quoting,
while TSV passes through byte for byte.

## How the tools ask

- **Independent judgments put the record inside its question** (`grev`, `tagv`, `rank`,
  `uniqv`, `probev --pack`). Each question is `{"text": …, "question": "Is it true that \`text\` …?"}`
  and the state holds only shared context (`--about`, `-S FILE`). On labelled sets this beat a
  state array addressed as `` `lines[i]` ``, 94% vs 91% accuracy (see
  [`eval/RESULTS.md`](../eval/RESULTS.md)).
- **Naming the domain is the biggest accuracy lever.** `--about 'git commit messages'`, or a full
  `{}` question ("Does the commit message {} describe a bug fix?"), adds 3–5 points.
- **Relational judgments put a line-tagged document in the state** (`unwrap`, `seg`, `pickv`).
  The state is a window of `L0001| …` lines and the questions point at ids, because the model needs
  the surroundings. In an A/B on a memo, heading/body breaks scored 0.53 and 0.36 as pair-only
  questions (wrongly joined), and 0.13 and 0.05 in document form.
- **Choice is relative and Noul is absolute.** `pickv` pairs its Choice with an existence Noul
  ("does any line answer this at all?"). `seek` offers `(none)` at every level. Otherwise a Choice
  always crowns some winner.
- **Code does what the model is bad at**: counting (`grev -c`), negation (`-v` is 1 − P, never a
  negated question; P(q) ≠ 1 − P(¬q)), arithmetic and ordering (`rank`, `probev` columns for
  awk), dates, and structure (`unwrap` never sends list markers, fences or blank lines to the
  model).
- **Multi-round tools** (`seek`, `pickv` over 255 lines, `tagv --tree`) quote only their first
  round with `-Q`. Budgets and caps are enforced across all rounds.

## Engine

`internal/jev` packs consecutive questions that share a state into requests, within the
context budgets:
- the whole request ≤ 64k tokens
- the state plus its longest question ≤ 32k

A 15% margin and a per-request question cap (128) keep requests well inside those.

A scheduler gates requests:
- **`-J N`** is a fixed number of requests in flight.
- **`-Jmax`** is adaptive. It starts at 2, grows by one per completion while latency holds (which
  doubles it every round trip), then a gradient controller takes over with a 1.5× latency
  tolerance, as in Netflix's gradient2. A 429 halves the limit and pauses everyone for
  `Retry-After`.
- Both modes respect token buckets for the published limits: 1,200 requests/min and 250k tokens/s.

Answers come back in input order, whatever order the requests finish in. Oversize records fail
on their own without failing the run. A 422 context-length error splits the request in half and
retries.

## Safeguards

In order:
1. The per-run budget (`--max-cost` or `defaults.maxCost`) and the daily/monthly caps
   (`limits.*`, tracked in a local ledger) refuse a run whose quote doesn't fit.
2. `-Q`, or a quote above `confirmAbove` ($1 built in), asks on the terminal. Without a terminal
   it refuses with exit 4.
3. An explicit `--max-cost` covering the quote counts as the answer.
4. During the run, every request is checked against what's left, so streaming and multi-round
   runs stop at the limit as well.

Closing stdout (`| head`) cancels the run.

The ledger (`~/.local/state/grev/spend`) keeps one line per day and tool. Writers use a portable
lock file. Parallel invocations can overshoot a cap by at most what they already have in flight.

## Conventions

- **Flags:** the common flags on every tool are `-p`, `-Q`, `-J`, `-M`, `--max-cost` and
  `--confirm-above`. Record tools share `-s` (leading score column), `-t`
  (threshold), `-m` (max outputs), `--about`, `-W` (neighbours the model sees), `-d`
  (fields `{1}` `{2}`), `-z`/`--para` and `--line-buffered`.
- **Flags that deliberately differ,** each following the classic tool it mirrors:
  - `-d` means *repeated* in `uniqv` (as uniq) and *directories* in `seek` (as `find -type d`).
  - `-f` is a labels file (`oneof`, `tagv`), a questions JSON file (`probev`), or *files only*
    (`seek`).
  - `-q` is quiet in `grev` (as grep) and the question elsewhere.
  - `-s` prints the mapping on stderr in `cutv`, and per-line join probabilities in `unwrap`.
  - `-t` in `pickv` is the existence threshold (`--min-conf` gates the choice).
- **Exit codes:**

  | code | meaning |
  |---|---|
  | 0 | yes / match / ok |
  | 1 | no / none |
  | 2 | error |
  | 3 | only uncertain answers |
  | 4 | declined, or over a budget or cap |
  | 130 | interrupted |

- **Names:**
  - `grev` is grep + Jev.
  - A `v` suffix marks a semantic variant of a classic tool: `uniqv`, `cutv`, `isv`, `tagv`,
    `pickv`, `probev`, and `lookv`, which binary-searches like look(1).
  - `is`, `tag`, `pick` and `probe` were renamed after a survey of Debian, Arch/AUR, Fedora,
    Alpine and Homebrew binaries. `is` clashes with Microsoft's inshellisense, `tag` with
    Homebrew's `tag`, `pick` with mptre/pick and nmh (Debian policy §10.1), and `probe` with
    probelabs/probe.

## Layout

```
cmd/<tool>/        one small main per tool
internal/cli/      getopt, common flags, config defaults, safeguards and ledger, records, progress, man/completions
internal/config/   ~/.grevconfig: git-config parser/writer, schema
internal/jev/      API types and client, credentials, pricing, packing, scheduler, engine
internal/jevtest/  fake API for offline tests
test/cli/          CLI tests against the fake API  (make test)
test/live/         coarse live tests with your key, capped (make test-live)
eval/              labelled fixtures that set templates and thresholds (make eval)
packaging/         Arch PKGBUILDs; .goreleaser.yaml builds deb/rpm/apk/arch/archives
```
