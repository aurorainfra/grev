# probev

**Many questions per record, as columns.** Turn free text into numbers and labels for awk,
sort and spreadsheets.

`probev` asks every question you give it about each record and prints one TSV row per
record: one column per question, then the record. A yes/no question prints P(yes), a Score
prints a position on its levels, and a Choice prints the chosen option. All questions about a
record go into one request, so extra questions cost almost nothing. Reach for it when one
yes/no isn't enough: triage, scoring or feature extraction, with the weighting done in code
you control.

## Example: triage product reviews

```console
$ probev -H \
    -q 'defect: The reviewer reports the product is broken or defective' \
    -q 'refund: The reviewer wants a refund or is returning it' \
    -q 'mood: How does the reviewer feel? <furious|unhappy|neutral|happy|delighted>' \
    -q 'topic: What is the review mostly about? [quality|shipping|price|support]' \
    < examples/probev-reviews.txt
defect	refund	mood	topic	record
0.06	0.18	1.27	shipping	Arrived two weeks late and the box was crushed, but the kettle itself works fine.
0.66	0.97	0.40	quality	Stopped heating after three days. I want my money back.
0.01	0.02	3.43	quality	Great kettle, boils fast and looks lovely on the counter.
0.97	0.70	0.52	quality	The lid hinge snapped on day one. Support never answered my emails.
0.03	0.11	1.76	price	A bit pricey for what it is, but it does the job.
0.91	0.98	1.01	quality	Leaks from the base whenever it's more than half full. Returning it.
0.02	0.03	3.32	quality	Quiet, fast, and the temperature presets are spot on for green tea.
0.95	0.06	3.71	support	Customer service replaced my faulty unit within a week, very impressed.
```

That's 32 answers from 8 requests (one per review): 3,209 tokens, about $0.0001. `mood` is a
position on the 0–4 scale (0 = furious), so 3.71 sits between happy and delighted. The last
review mentions a faulty unit (`defect` 0.95), but the reviewer is delighted with how support
handled it, and the columns keep those two facts apart.

## More examples

Composite scoring: weight the columns in awk and sort, so the reviews needing attention come
first:

```console
$ probev -q 'defect: The reviewer reports the product is broken or defective' \
         -q 'refund: The reviewer wants a refund or is returning it' \
         -q 'mood: How does the reviewer feel? <furious|unhappy|neutral|happy|delighted>' \
         < examples/probev-reviews.txt |
    awk -F'\t' '{ printf "%.2f\t%s\n", .5*$1 + .3*$2 + .2*(4-$3)/4, $4 }' | sort -rn | head -3
0.91	Leaks from the base whenever it's more than half full. Returning it.
0.86	The lid hinge snapped on day one. Support never answered my emails.
0.77	Stopped heating after three days. I want my money back.
```

`-s` adds a confidence column after each Choice and Score. `--no-record` drops the text:

```console
$ head -3 examples/probev-reviews.txt | probev -H -s --no-record \
    -q 'topic: What is the review mostly about? [quality|shipping|price|support]' \
    -q 'mood: How does the reviewer feel? <furious|unhappy|neutral|happy|delighted>'
topic	topic.conf	mood	mood.conf
shipping	0.98	1.25	0.79
quality	0.35	0.39	0.68
quality	1.00	3.39	0.68
```

JSON records (`--jsonl`) become structured state, so questions can compare fields. Here they
flag reviews whose text contradicts their star rating:

```console
$ probev --jsonl -H -q 'mismatch: Does `text` contradict the star rating in `stars` (1 = worst, 5 = best)?' \
    < examples/probev-reviews.jsonl
mismatch	record
0.94	{"id": "r-101", "stars": 5, "text": "Stopped heating after three days. Total waste of money."}
0.02	{"id": "r-102", "stars": 5, "text": "Great kettle, boils fast and looks lovely on the counter."}
0.95	{"id": "r-103", "stars": 1, "text": "Works perfectly, quiet and quick. Would buy again."}
0.15	{"id": "r-104", "stars": 2, "text": "The lid hinge snapped on day one."}
0.23	{"id": "r-105", "stars": 4, "text": "A bit pricey for what it is, but it does the job."}
```

`--json` prints the full distributions, which show when a Choice was close:

```console
$ echo 'Stopped heating after three days. I want my money back.' |
    probev --json -q 'refund: The reviewer wants a refund' -q 'topic: What is it mostly about? [quality|shipping|price|support]'
{"record":"Stopped heating after three days. I want my money back.","answers":{"refund":{"noul":0.98,"type":"noul"},"topic":{"choice":"quality","confidence":0.15,"probabilities":{"price":0.36,"quality":0.35,"shipping":0.01,"support":0.28},"type":"choice"}}}
```

## Question syntax (SPEC)

| SPEC | question type | column |
|---|---|---|
| `name: question` | yes/no | P(yes), 0–1 |
| `name: question [a\|b\|c]` | Choice; options may be `a=description` | the chosen option |
| `name: question <lo\|mid\|hi>` | Score, levels lowest first | position 0…N−1 |

`{}` in a question refers to the record. `-f FILE` takes raw API questions as a JSON object,
for full control over criteria.

## Options worth knowing

| option | what it does |
|---|---|
| `-H` | header row of question names |
| `-s` | confidence column after each Choice and Score |
| `--json` | JSON lines with every probability |
| `--jsonl` | input records are JSON objects, used as structured state |
| `--pack` | many short records per request, which is faster for one-liners |
| `--about TEXT`, `-S FILE` | shared context for every question |
| `--line-buffered` | stream rows as records arrive |

See `probev --help` or `man probev`.

## Exit status

0 ok, 2 error (failed questions print `-`), 4 declined at `-Q` or over a budget.

## See also

[grev](grev.md) (keep the records a single question says yes to), [tagv](tagv.md) (one label
per record), [rank](rank.md) (sort by one question), [DESIGN](../DESIGN.md).
