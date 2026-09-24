# lookv

**look(1) and git bisect, for questions.** lookv searches an *ordered* input (a log, a build
history, anything chronological or sorted) for the first record where the model's yes/no answer
flips. It assumes the answer is no up to some point and yes from there on, and finds that point
without reading everything:
- Each round asks about K evenly spaced records (16 by default) in one request.
- It then narrows to the gap where the answer changes, so 600 lines take 3 requests and a million
  lines about 5.
- The first round always checks the first and the last record, so "never flips" and "yes from the
  start" are caught at once.

## Example: when did the cache start failing?

A health-check log with one line per minute for ten hours. Somewhere in there, a dependency
stopped answering:

```console
$ wc -l examples/lookv-health.log
600 examples/lookv-health.log
$ head -2 examples/lookv-health.log
2026-09-24T09:00:00Z health api=ok db=ok cache=ok queue=ok p95=38ms
2026-09-24T09:01:00Z health api=ok db=ok cache=ok queue=ok p95=19ms
$ lookv -p -n --about 'health checks, one per minute' 'reports a failing dependency' examples/lookv-health.log
458:2026-09-24T16:37:00Z health api=ok db=ok cache=timeout queue=ok p95=182ms
lookv: 33 q · 3 req · 3,197 tok · $0.0001 · 1.0s · jev-1.13.0
```

- **What happened:** lookv asked about 33 of the 600 lines in 3 rounds and printed the first one
  that reports a failing dependency (`cache=timeout`).
- **Why it's cheap:** `grev` would have asked all 600, which is fine here but not on a day of logs.
  lookv's cost grows with the number of rounds, not with the size of the file.

## More examples

`-v` finds where a yes *stops*: the first build that no longer passes, with the last good one as
context (`-B1`):

```console
$ lookv -v -n -B1 --about 'CI build history, oldest first' 'all tests passed' examples/lookv-builds.txt
222-#1621 fe3fa6b main: 817 tests, all passed
223:#1622 ad17408 main: 817 tests, 3 failed (TestLedgerConcurrentWriters, TestSpendCaps, TestJevSpend)
```

`--trace` shows the search. With `-k 4` it takes more rounds, each with fewer questions:

```console
$ lookv --trace -k 4 --about 'CI build history, oldest first' 'the build has failing tests' examples/lookv-builds.txt
lookv: round 1: asked 1:0.02 64:0.02 128:0.02 320:0.97 → flip in (128, 320]
lookv: round 2: asked 166:0.02 205:0.02 243:0.97 282:0.97 → flip in (205, 243]
lookv: round 3: asked 213:0.02 220:0.02 228:0.97 235:0.97 → flip in (220, 228]
lookv: round 4: asked 222:0.02 223:0.97 225:0.97 226:0.97 → flip in (222, 223]
#1622 ad17408 main: 817 tests, 3 failed (TestLedgerConcurrentWriters, TestSpendCaps, TestJevSpend)
```

If the answer never flips, nothing is printed and the exit status is 1, like grep:

```console
$ lookv --about 'health checks, one per minute' 'reports the database is down' examples/lookv-health.log
$ echo $?
1
```

On real data, check the answer before acting on it. `--verify` asks again about the two records
around the flip, with more context. `git log --reverse` puts history in the order lookv needs:

```console
$ lookv --verify -n --about 'CI build history, oldest first' 'the build has failing tests' examples/lookv-builds.txt
223:#1622 ad17408 main: 817 tests, 3 failed (TestLedgerConcurrentWriters, TestSpendCaps, TestJevSpend)
$ git log --reverse --oneline | lookv --about 'commit subjects, oldest first' 'mentions the new config file'
```

## When the answers aren't monotonic

lookv trusts the assumption "no, no, …, yes, yes". If a probe says no *after* an earlier yes,
lookv warns (`the answers are not monotonic`) and reports the first flip it found, as git bisect
would. To make the assumption hold:
- **Ask about a state, not an event.** "reports a failing dependency" holds for every line of an
  outage; "is an error" doesn't hold for the info lines in between.
- **Sort or filter first.** `grep health`, or `sort`, before lookv.
- **Give the model context.** `-W N` shows each record's neighbours.

## Options worth knowing

| option | meaning |
|---|---|
| `-v` | find the first record where the answer stops being yes |
| `-n`, `-B N`, `-A N`, `-C N` | record numbers and context, as in grep |
| `-s` | print P(yes) before each printed record (`-` where it wasn't asked) |
| `-k K` | records asked about per round (default 16); lower K means more rounds with fewer questions each |
| `--verify` | ask again about the two records around the flip, with more context; exit 3 if the answers disagree |
| `--band LO:HI` | exit 3 if the flip or the record before it is uncertain |
| `-W N` | show the model N neighbouring records |
| `--about TEXT` | what the records are; the biggest accuracy lever |
| `--trace` | print each round's probes and the narrowed range on stderr |

The rest is in `man lookv` or `lookv --help`. `-Q` quotes the worst case up front: the number of
rounds is known from the file size.

## Exit status

0 found the flip · 1 it never flips · 2 error · 3 uncertain (`--band`) or `--verify` disagreed ·
4 declined at `-Q` or over a budget.

## See also

- [grev](grev.md): every matching record, not only the first flip.
- [seg](seg.md): every point where the topic changes.
- [isv](isv.md): one yes/no answer about the whole input.
- [DESIGN](../DESIGN.md)
