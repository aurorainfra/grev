# uniqv

`uniq` by meaning: collapse neighbouring lines that say the same thing in different words.

`uniqv` asks one yes/no question per pair of adjacent records: are these the same? It collapses
each run of "same" records to the first one. Like `uniq`, it only compares neighbours, so the pairs
are independent and all go out in parallel. If duplicates aren't adjacent, group them first with
`sort`, [rank](rank.md) or [tagv](tagv.md).

## Example: fold an alert storm into incidents

```console
$ cat examples/uniqv-alerts.log
09:02 [PAGE] api-gateway: 5xx rate above 5% for 5 minutes
09:03 [PAGE] api-gateway: error rate still elevated (7.2% 5xx)
09:05 [PAGE] api-gateway returning 502s to 7% of requests
09:11 [WARN] db-primary: replication lag 45s
09:12 [WARN] db-primary replica is 52 seconds behind
09:20 [PAGE] api-gateway: 5xx rate back above 5%
09:31 [INFO] deploy web-frontend v2.14.0 finished
09:33 [WARN] disk /var on build-04 at 91%
$ uniqv -c 'Do {1} and {2} report the same ongoing incident?' examples/uniqv-alerts.log
      3 09:02 [PAGE] api-gateway: 5xx rate above 5% for 5 minutes
      2 09:11 [WARN] db-primary: replication lag 45s
      1 09:20 [PAGE] api-gateway: 5xx rate back above 5%
      1 09:31 [INFO] deploy web-frontend v2.14.0 finished
      1 09:33 [WARN] disk /var on build-04 at 91%
```

- **Three wordings, one incident:** the gateway pages describe it as "5xx rate", "error rate" and
  "502s to 7% of requests".
- **Two wordings of the lag:** "replication lag 45s" and "52 seconds behind" fold together.
- **A new incident:** the 09:20 page comes after the gateway had cleared ("back above 5%"). It
  starts its own run, which is exactly what the question asks.

## More examples

The default question, "Do {1} and {2} refer to the same thing?", on a sorted company list. `-s`
shows P(same as the line before) for each record that begins a run:

```console
$ uniqv -s examples/companies.txt
-	Alphabet Inc.
0.03	Apple Computer, Inc.
0.02	Google LLC
0.02	Microsoft
```

A domain-specific question keeps parent companies apart. Here Alphabet and Google stay separate:

```console
$ uniqv -c 'Are {1} and {2} the same company?' examples/companies.txt
      1 Alphabet Inc.
      3 Apple Computer, Inc.
      1 Google LLC
      3 Microsoft
```

Only the repeats (`-d`), every record of each repeated run with scores (`-D -s`), or only the
one-offs (`-u`):

```console
$ uniqv -d 'Do {1} and {2} report the same ongoing incident?' examples/uniqv-alerts.log
09:02 [PAGE] api-gateway: 5xx rate above 5% for 5 minutes
09:11 [WARN] db-primary: replication lag 45s
$ uniqv -D -s 'Do {1} and {2} report the same ongoing incident?' examples/uniqv-alerts.log
-	09:02 [PAGE] api-gateway: 5xx rate above 5% for 5 minutes
0.92	09:03 [PAGE] api-gateway: error rate still elevated (7.2% 5xx)
0.90	09:05 [PAGE] api-gateway returning 502s to 7% of requests
0.37	09:11 [WARN] db-primary: replication lag 45s
0.89	09:12 [WARN] db-primary replica is 52 seconds behind
$ uniqv -u 'Do {1} and {2} report the same ongoing incident?' examples/uniqv-alerts.log
09:20 [PAGE] api-gateway: 5xx rate back above 5%
09:31 [INFO] deploy web-frontend v2.14.0 finished
09:33 [WARN] disk /var on build-04 at 91%
```

On a live stream, `tail -f alerts.log | uniqv --line-buffered '…'` prints each run as soon as it
ends.

## Options worth knowing

| option | what it does |
|---|---|
| `QUESTION` | When two neighbours count as the same; it must use `{1}` (earlier) and `{2}` (later). |
| `-c` | Prefix each run with its length, like `uniq -c`. |
| `-d` / `-D` / `-u` | Repeated runs once / every repeated record / only non-repeated records. |
| `-s` | Prefix P(same as the record before). |
| `-t P` | How sure the model must be before two records count as the same (default 0.5). |
| `--line-buffered` | Stream, printing each run when it ends. |
| `--about TEXT` | Say what the records are. |

The rest is in `man uniqv` or `uniqv --help`.

## Exit status

| code | meaning |
|---|---|
| 0 | ok |
| 2 | error |
| 4 | declined at `-Q`, or over a budget |

## See also

- [tagv](tagv.md) and [rank](rank.md) group or order records so that duplicates end up adjacent.
- [grev](grev.md) keeps or drops single records.
- [seg](seg.md) finds boundaries in a stream rather than duplicates.
