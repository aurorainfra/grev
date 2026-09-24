# unwrap

`fmt` in reverse: rejoin text that was hard-wrapped mid-sentence.

PDF extracts, emails, man pages and old READMEs arrive with a newline every 70 characters.
`unwrap` removes the newlines that break a sentence and keeps the ones the author meant. The clear
cases are settled in code and never sent to the model: blank lines, headings, list items, quotes,
tables, indented code and fenced blocks. For each remaining line break, the model gets the
surrounding lines as a line-numbered document and is asked whether the next line picks up
mid-sentence. Joined lines get one space between them. Nothing else changes, so the output is
still your text, word for word.

## Example: clean up a `pdftotext` extract

```console
$ cat examples/unwrap-paper.txt
3.2 Failure handling

When a worker crashes while holding a lease, the coordina-
tor reassigns its partitions once the lease expires. In our
deployment this takes between 4 and 9 seconds, during which
the affected partitions accept writes but do not acknowledge
them.

We considered three ways to shorten that window:
- shorter leases, which increase coordinator load;
- fencing tokens, which require client changes;
- a hot standby per partition, which doubles the cost.

We chose shorter leases, because they keep the recov-
ery path entirely on the server side.
$ unwrap --dehyphen examples/unwrap-paper.txt
3.2 Failure handling

When a worker crashes while holding a lease, the coordinator reassigns its partitions once the lease expires. In our deployment this takes between 4 and 9 seconds, during which the affected partitions accept writes but do not acknowledge them.

We considered three ways to shorten that window:
- shorter leases, which increase coordinator load;
- fencing tokens, which require client changes;
- a hot standby per partition, which doubles the cost.

We chose shorter leases, because they keep the recovery path entirely on the server side.
```

The paragraphs are whole again, the list and the heading are untouched, and `--dehyphen` mended
`coordina-`/`tor` and `recov-`/`ery`. Without it, those joins keep the hyphen (`coordina- tor`),
because removing it changes the text.

## More examples

A memo that lost its formatting. The heading has no blank line after it, yet it stays on its own
line, and the indented command stays as it was:

```console
$ unwrap examples/memo.txt
Migration to the new build system

Hi everyone, quick heads up about the build system migration that is happening next week. We have been running the new pipeline in shadow mode for three weeks and the results look solid, so it is time to make the switch for real.

What changes for you
The old make targets keep working until the end of the month. The new entrypoint is a single command that wraps everything, including the docs build that used to be separate.

    bun run build

Generated artifacts no longer need to be committed. The new pipeline uploads them to the registry automatically, and checking them in just creates merge conflicts.
```

To tune thresholds, `-s` prints every line with the model's P(join with the line before) instead of
joining. A `-` means code decided that break without asking.

```console
$ unwrap -s examples/unwrap-paper.txt
-	3.2 Failure handling
-	
-	When a worker crashes while holding a lease, the coordina-
0.68	tor reassigns its partitions once the lease expires. In our
0.54	deployment this takes between 4 and 9 seconds, during which
0.70	the affected partitions accept writes but do not acknowledge
0.33	them.
…
-	We chose shorter leases, because they keep the recov-
0.61	ery path entirely on the server side.
```

`them.` scored 0.33 and was still joined. After a line with no sentence-ending punctuation, the
join threshold is 0.2. After `.`, `!`, `?`, `:` or `;` it is 0.5. Change both with `-t D,T`.

## Options worth knowing

| option | what it does |
|---|---|
| `--dehyphen` | Also join `exam-` + `ple` into `example`. This changes bytes, so it's off by default. |
| `-s` | Show P(join) per line instead of joining, for tuning. |
| `-t D,T` | Join thresholds after a dangling line and after a punctuated line (default 0.2,0.5). |
| `-j SEP` | Separator between joined lines (default one space). |
| `--line-buffered` | Stream, printing lines as soon as their breaks are decided. |
| `--about TEXT` | Say what the text is. |

The rest is in `man unwrap` or `unwrap --help`.

## Exit status

| code | meaning |
|---|---|
| 0 | ok |
| 2 | error |
| 4 | declined at `-Q`, or over a budget |

## See also

- [seg](seg.md) finds topic boundaries after the text has been unwrapped.
- [grev](grev.md) and [pickv](pickv.md) work better on whole sentences than on wrapped fragments.
- [DESIGN](../DESIGN.md) explains why `unwrap` sends line-numbered documents rather than line
  pairs. An A/B test showed that heading breaks get joined without the surrounding context.
