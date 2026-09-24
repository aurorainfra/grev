# tagv

**Label every record.** tagv asks, for each record, which of your labels fits it best (a Choice
question per record) and prints `label<TAB>record`. It can also route records into one file per
label, or walk a taxonomy level by level. It is `awk '{print > $1}'` where the first field comes
from the model.

## Example: triage a support queue

```console
$ tagv -p billing technical feature-request other < examples/tickets.txt
billing	I was charged twice for order A-104, please refund the duplicate.
technical	The app crashes every time I tap "Save" on the profile screen.
feature-request	Can you add dark mode? My eyes hurt at night.
technical	Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
other	How do I change the email address on my account?
billing	I cancelled last month but you billed me again. I want my money back.
technical	Export to CSV puts all the data in one column in Excel.
billing	Do you offer discounts for non-profits?
technical	Password reset emails never arrive.
other	Love the new update, the search is so much faster!
tagv: 10 q · 1 req · 970 tok · $0.000041 · 0.5s · jev-1.13.0
```

- **Output:** every record comes back verbatim, with the label as an extra first column, so `cut`,
  `sort` and `uniq -c` work on it.
- **Cost:** all ten questions went in one request, for $0.00004.
- **Give it an escape label.** Here `other` catches the praise and the how-to question. Without an
  escape, a Choice has to pick one of the real labels, and the praise ends up in `technical`.

## More examples

`--split DIR` routes records into one file per label, and prints the counts on stderr:

```console
$ tagv --split queues/ billing technical feature-request other < examples/tickets.txt
tagv: billing	3
tagv: technical	4
tagv: feature-request	1
tagv: other	2
$ cat queues/technical
The app crashes every time I tap "Save" on the profile screen.
Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
Export to CSV puts all the data in one column in Excel.
Password reset emails never arrive.
```

`--only` prints just the records with one label, without the label column: grep by category.

```console
$ tagv --only technical billing technical feature-request other < examples/tickets.txt
The app crashes every time I tap "Save" on the profile screen.
Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
Export to CSV puts all the data in one column in Excel.
Password reset emails never arrive.
```

`--tree` takes an indented taxonomy ([`examples/tagv-taxonomy.txt`](../../examples/tagv-taxonomy.txt))
and walks it one level at a time. The label becomes a path, and `-s` shows its confidence:

```console
$ tagv -s --tree examples/tagv-taxonomy.txt < examples/tickets.txt
1.00	billing/refund	I was charged twice for order A-104, please refund the duplicate.
0.99	technical/crash	The app crashes every time I tap "Save" on the profile screen.
1.00	product/feature-request	Can you add dark mode? My eyes hurt at night.
1.00	technical/outage	Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
0.91	account/profile	How do I change the email address on my account?
1.00	billing/refund	I cancelled last month but you billed me again. I want my money back.
0.96	technical/data	Export to CSV puts all the data in one column in Excel.
1.00	billing/pricing	Do you offer discounts for non-profits?
1.00	technical/email	Password reset emails never arrive.
1.00	product/praise	Love the new update, the search is so much faster!
```

With `-t`, low-confidence records get the `?` label instead of a guess. Without an escape label,
the how-to question is a weak fit for all three labels:

```console
$ tagv -s -t 0.8 billing technical feature-request < examples/tickets.txt
…
0.93	technical	Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
0.78	?	How do I change the email address on my account?
1.00	billing	I cancelled last month but you billed me again. I want my money back.
…
```

## Options worth knowing

| option | meaning |
|---|---|
| `LABEL=DESCRIPTION`, `-f FILE` | describe labels, or read them from a file |
| `--other[=NAME]` | escape label for records that fit none |
| `-s`, `-t C`, `--unsure NAME` | confidence column; records below C get `?` (or NAME) |
| `--only L[,L]` | print only the records with these labels, without the label |
| `--split DIR` | append each record to `DIR/<label>` |
| `--tree FILE`, `-b K` | hierarchical labels from an indented taxonomy, beam width K |
| `-q QUESTION` | a custom question; `{}` is the record |
| `--about TEXT`, `-W N` | domain context; neighbouring records |
| `--line-buffered` | streaming mode |

All options: `tagv --help` or `man tagv`.

## Exit status

0 ok, 2 error (records whose question failed are labelled `!`), 4 declined at `-Q` or over
`--max-cost`.

## See also

- [oneof](oneof.md): one label for the whole input.
- [grev](grev.md): keep or drop records instead of labelling them.
- [probev](probev.md): several labels and scores per record, as columns.
