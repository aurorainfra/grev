# rank

**sort by meaning.** rank scores every record against a query and prints them best first. By
default the score is P(yes) for "does this record fit QUERY?". With `-L`, each record is placed on
ordered levels you name, such as not urgent|soon|urgent|critical. Use it to search a small corpus
by relevance, or to put a queue in priority order.

## Example: search a FAQ by meaning

```console
$ head -3 examples/rank-faq.txt
Q: How do I change my plan? A: Open Settings > Billing and pick a new plan; changes apply at the next renewal.
Q: Can I export my data? A: Yes, Settings > Data > Export gives you a CSV of all orders and customers.
Q: I forgot my password. A: Use "Forgot password" on the sign-in page; the reset link is valid for 30 minutes.
$ rank -s -m3 --about 'FAQ entries' 'answers: how do I get back into my account if I cannot log in' examples/rank-faq.txt
0.92	Q: I forgot my password. A: Use "Forgot password" on the sign-in page; the reset link is valid for 30 minutes.
0.02	Q: How do I turn on two-factor authentication? A: Settings > Security > Two-factor, then scan the QR code.
0.01	Q: How do I change my plan? A: Open Settings > Billing and pick a new plan; changes apply at the next renewal.
```

- **The match needs no shared words.** The query says "get back into my account", the entry says
  "forgot my password". The answer is the only entry with a high probability, and the gap to the
  runner-up tells you how clear the match was.
- **`-m3` limits the output, not the work:** every entry is still scored, in one request, and the
  top three are printed.

## More examples

Put a support queue in priority order on named levels. The score is the probability-weighted
level, from 0 (not urgent) to 3 (critical):

```console
$ rank -s -L 'not urgent|soon|urgent|critical' 'How urgent is the support ticket {}?' examples/tickets.txt
2.97	Your checkout page has been down for 20 minutes and I'm losing sales. Fix it NOW.
2.29	The app crashes every time I tap "Save" on the profile screen.
2.17	Password reset emails never arrive.
1.61	I cancelled last month but you billed me again. I want my money back.
1.41	I was charged twice for order A-104, please refund the duplicate.
0.51	Export to CSV puts all the data in one column in Excel.
0.49	Can you add dark mode? My eyes hurt at night.
0.10	How do I change the email address on my account?
0.01	Do you offer discounts for non-profits?
0.00	Love the new update, the search is so much faster!
```

`-r` sorts lowest first, and `-n` keeps the original line numbers. Here are the three least "light"
dishes, the heaviest ones:

```console
$ rank -n -r -m3 --about 'restaurant menu' 'is a light meal' examples/menu.txt
1:chicken tikka masala
3:spaghetti carbonara
4:tofu stir fry with rice
```

## Options worth knowing

| option | meaning |
|---|---|
| `-L 'low\|…\|high'` | rank on 2–10 ordered levels (a Score) instead of P(yes) |
| `-m N` | print the top N (every record is still scored) |
| `-s`, `-n`, `-r` | score column, original line numbers, lowest first |
| `--about TEXT`, `-S FILE` | domain context; a reference document shared by every question |
| `--yes TEXT`, `--no TEXT` | pin down what counts as a fit (default mode) |
| `-W N` | let the model see neighbouring records |
| `-d DELIM`, `--para`, `-z` | fields for `{1}` `{2}`; paragraph or NUL records |

The sort is stable, so ties keep input order. There's no streaming, as with `sort`. All options:
`rank --help` or `man rank`.

## Exit status

0 ok, 2 error (records whose question failed sort last), 4 declined at `-Q` or over `--max-cost`.

## See also

- [grev](grev.md): keep only the records that fit.
- [pickv](pickv.md): the single best line, with an "is it here at all?" check.
- [probev](probev.md): several scores per record, as columns.
