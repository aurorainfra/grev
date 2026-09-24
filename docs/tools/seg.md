# seg

`csplit` by meaning: split a stream wherever the topic changes.

At every boundary between two records, `seg` asks whether a new segment begins there. The model
sees the records around each boundary as a line-numbered document, not a lone pair. `seg` prints
a separator line at each yes, or with `--split` starts a new file. Use it on meeting notes,
transcripts, digests, logs and shell history: anything that runs several topics together without
marking them.

## Example: split meeting notes into topics

```console
$ cat examples/seg-standup.txt
Release 2.14 is on track for Thursday.
The last blocker, the checkout timeout, was fixed yesterday.
QA signed off on the payment flows this morning.
We still need release notes; Priya will draft them today.
On-call was rough: the gateway paged four times overnight.
Root cause was the new rate limiter rejecting health checks.
Marco rolled the limiter back at 3am and error rates recovered.
We need a follow-up to exempt health checks before re-enabling it.
Two candidates for the backend role are in final interviews next week.
Please sign up for interview slots in the hiring sheet.
The team offsite is confirmed for the 14th in Lisbon.
Book flights by Friday so finance can approve them in one batch.
$ seg -s --sep='---' examples/seg-standup.txt
Release 2.14 is on track for Thursday.
The last blocker, the checkout timeout, was fixed yesterday.
QA signed off on the payment flows this morning.
We still need release notes; Priya will draft them today.
--- 0.84
On-call was rough: the gateway paged four times overnight.
Root cause was the new rate limiter rejecting health checks.
Marco rolled the limiter back at 3am and error rates recovered.
We need a follow-up to exempt health checks before re-enabling it.
--- 0.94
Two candidates for the backend role are in final interviews next week.
Please sign up for interview slots in the hiring sheet.
--- 0.89
The team offsite is confirmed for the 14th in Lisbon.
Book flights by Friday so finance can approve them in one batch.
```

That gives four topics: release, on-call, hiring and offsite. `-s` puts P(new segment) on each
separator. Without `--sep`, segments are separated by an empty line.

## More examples

A custom question. Here it splits shell history into the tasks you were doing:

```console
$ seg -s 'Does {2} start working on a different task than {1}?' examples/seg-history.txt
git clone git@github.com:acme/billing.git
cd billing
make deps
go test ./...
go test -run TestInvoiceRounding ./internal/invoice
git checkout -b fix-rounding
git commit -am "Round invoice totals half-even"
--- 0.68
docker ps
docker logs -f billing-worker-3
docker restart billing-worker-3
--- 0.63
ssh prod-db-1
psql -c 'select count(*) from pending_jobs'
```

`{1}` is the record before the boundary and `{2}` the one after. A question without placeholders is
read as a predicate, e.g. `seg 'the speaker changes' transcript.txt`.

Write each segment to its own file with `--split`, which prints the file names:

```console
$ seg --split standup- examples/seg-standup.txt
standup-00
standup-01
standup-02
standup-03
$ wc -l standup-*
  4 standup-00
  4 standup-01
  2 standup-02
  2 standup-03
 12 total
```

Keep segments from getting too short with `--min N`. A new segment can't start until the current
one has N records, so the 2-line hiring note joins the offsite:

```console
$ seg --min 3 examples/seg-standup.txt
…
We need a follow-up to exempt health checks before re-enabling it.

Two candidates for the backend role are in final interviews next week.
Please sign up for interview slots in the hiring sheet.
The team offsite is confirmed for the 14th in Lisbon.
Book flights by Friday so finance can approve them in one batch.
```

## Options worth knowing

| option | what it does |
|---|---|
| `QUESTION` | What counts as a boundary. Use `{1}`/`{2}`, or write a predicate. Default: "Does a new topic begin at {2}?" |
| `-s` | Put P(new segment) on each separator line. |
| `--sep[=STR]` | Separator line (default: empty line; `---` with `--para`). |
| `--split PREFIX` | Write segments to PREFIX00, PREFIX01, … and print their names. |
| `--min N` | A segment has at least N records. |
| `-t P` | How sure the model must be before starting a new segment (default 0.5). |
| `-W N` | Records of context the model sees around each window (default 3). |
| `--para` | Records are paragraphs, e.g. a digest of stories. |
| `--line-buffered` | Stream, e.g. `tail -f app.log \| seg --line-buffered …`. |

The rest is in `man seg` or `seg --help`.

## Exit status

| code | meaning |
|---|---|
| 0 | ok |
| 2 | error |
| 4 | declined at `-Q`, or over a budget |

## See also

- [unwrap](unwrap.md) rejoins wrapped lines first.
- [tagv](tagv.md) labels the segments.
- [uniqv](uniqv.md) collapses repeats instead of finding boundaries.
- [DESIGN](../DESIGN.md) explains why `seg` sends line-tagged windows rather than record pairs.
