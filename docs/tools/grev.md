# grev

**grep for questions.** grev asks the model one yes/no question per record (a line, by
default) and prints the records it answers yes to, verbatim and in input order. Use it when the
thing you're looking for is a meaning, not a string: "shows the process crashed", "was caused by
the client", "asks for money back".

## Example: find the crashes in an application log

```console
$ wc -l examples/app.log
16 examples/app.log
$ grev -p --about 'application log' 'shows the process crashed' examples/app.log
2026-09-24T09:00:34Z ERROR panic: runtime error: invalid memory address or nil pointer dereference [signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x6a3f2e]
2026-09-24T09:00:34Z INFO  supervisor: worker 3 exited with status 2, restarting
2026-09-24T09:00:50Z ERROR fatal error: concurrent map writes
2026-09-24T09:00:51Z INFO  supervisor: worker 1 exited with status 2, restarting
2026-09-24T09:01:22Z ERROR out of memory: killed process 2231 (worker) total-vm:4198340kB, anon-rss:3981220kB
grev: 16 q · 1 req · 1,388 tok · $0.000058 · 0.6s · jev-1.13.0
```

- **What matched:** no single keyword finds these lines. A panic, a Go fatal error and an OOM kill
  all qualify, and so do the supervisor lines reporting a worker that died. The `ERROR` lines about
  a 502 retry and a delayed email don't.
- **How it was asked:** grev puts each line inside its own question, and `--about` tells the model
  what the lines are.
- **Cost:** all 16 questions went in one request, for $0.00006 (the `-p` summary).

## More examples

A question with `{}` reads naturally; `-n` and `-s` add the line number and P(yes):

```console
$ grev -n -s --about 'nginx error log' 'says an upstream timed out' examples/nginx-error.log | cut -c1-110
0.99	1:2026/09/24 03:12:07 [error] 1123#1123: *88121 upstream timed out (110: Connection timed out) while read
0.99	6:2026/09/24 03:12:44 [error] 1123#1123: *88256 upstream timed out (110: Connection timed out) while conn
0.99	14:2026/09/24 03:13:52 [error] 1126#1126: *88433 upstream timed out (110: Connection timed out) while rea
$ grev 'Does the support ticket {} ask for money back?' examples/tickets.txt
I was charged twice for order A-104, please refund the duplicate.
I cancelled last month but you billed me again. I want my money back.
```

Counting is done in code, one answer per line, never by the model. `-C` shows context like grep:

```console
$ grev -c --about 'nginx error log' 'was caused by the client, not the server' examples/nginx-error.log
5
$ grev -n -C1 --about 'application log' 'shows a failed payment attempt' examples/app.log
4-2026-09-24T09:00:13Z INFO  POST /v1/checkout 201 212ms
5:2026-09-24T09:00:20Z ERROR payment provider returned 502, retrying (attempt 1/3)
6-2026-09-24T09:00:21Z INFO  payment provider ok after retry
```

Streaming: `--line-buffered` answers and prints lines as they arrive (`tail -f`, `journalctl -f`):

```console
$ tail -n 8 examples/app.log | grev --line-buffered --about 'application log' 'shows the process crashed'
2026-09-24T09:00:50Z ERROR fatal error: concurrent map writes
2026-09-24T09:00:51Z INFO  supervisor: worker 1 exited with status 2, restarting
2026-09-24T09:01:22Z ERROR out of memory: killed process 2231 (worker) total-vm:4198340kB, anon-rss:3981220kB
```

## Options worth knowing

| option | meaning |
|---|---|
| `--about TEXT` | what the input is; the biggest accuracy win (see [DESIGN](../DESIGN.md)) |
| `-v` | select the records the answer is *no* for (computed as 1 − P, not a negated question) |
| `-c`, `-n`, `-m N`, `-q` | count, line numbers, stop after N, quiet: as in grep |
| `-A/-B/-C N` | context records around each match |
| `-s`, `-t P` | print P(yes); change the threshold (default 0.5) |
| `--band LO:HI`, `--uncertain FILE` | treat the middle as uncertain; write those records to FILE |
| `-W N` | let the model see N neighbouring lines |
| `-d DELIM` | split records into fields for `{1}`, `{2}` |
| `-l`, `-L` | judge whole files and print their names |
| `--line-buffered` | streaming mode |

All options: `grev --help` or `man grev`.

## Exit status

0 if a record was selected, 1 if none, 2 on error, 3 if only uncertain records (see `--band`), 4
if declined at `-Q` or stopped by `--max-cost`.

## See also

- [isv](isv.md): one question about the whole input.
- [rank](rank.md): order records by how well they fit, instead of filtering.
- [tagv](tagv.md): label every record instead.
- [pickv](pickv.md): the single best line.
- [DESIGN](../DESIGN.md): why each record goes inside its own question.
