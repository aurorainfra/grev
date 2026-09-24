# pickv

`grep -o` for answers: point at the one line, or the one regex match, that answers a question.

`pickv` asks which single line best answers your question. Unlike [grev](grev.md), which judges
each line on its own, `pickv` compares the lines against each other. A separate yes/no check asks
whether the input answers the question at all. So you get the best line, or nothing (exit 1) when
nothing fits.

With `-e REGEX`, the candidates are the regex matches instead of lines. The regex finds every
email address or amount, `pickv` picks the one you asked for, and it prints the match exactly as
written. The model can't invent a value or transpose a digit.

## Example: find the flag in a man page

```console
$ man tar | pickv 'how do I list the contents of an archive?'
     tar -t [-f ARCHIVE] [OPTIONS] [MEMBER...]
```

`man tar` is about 1,150 lines. `pickv` read it in windows of up to 255 lines, one request each,
asking for the best line and whether the window answers the question at all. It then asked one
final round among the window winners. That was 5 requests, about 26k tokens and $0.0011.

## More examples

The runner-ups with their scores and line numbers, for when the best line needs context:

```console
$ man tar | pickv -m3 -s -n 'how do I extract into a different directory?'
0.78	748:     -C, --directory=DIR
0.10	289:            Extract  all  files  into DIR, or, if used without argument, into a
0.10	749:            Change to DIR before performing any operations.  This option is or‐
```

No answer in the input: nothing is printed and the exit status is 1. Use `--force` to see the
closest miss anyway.

```console
$ man tar | pickv 'how do I send an email?'; echo $?
1
```

Regex mode on an invoice email ([`examples/pickv-invoice.eml`](../../examples/pickv-invoice.eml))
that has five addresses and six amounts:

```console
$ pickv -e '[\w.+-]+@[\w.-]+\.\w+' 'where should the receipt be sent?' examples/pickv-invoice.eml
dana.personal@mailbox.example
$ pickv -e '-?\$[0-9][0-9,.]*' 'the total amount due' examples/pickv-invoice.eml
$1,314.00
$ pickv -s -e '-?\$[0-9][0-9,.]*' 'the credit given for the outage' examples/pickv-invoice.eml
0.99	-$25.00
$ pickv -e '[\w.+-]+@[\w.-]+\.\w+' "the customer's phone number" examples/pickv-invoice.eml; echo $?
1
```

It picked the personal address the email asks for over the sender, To and Cc addresses; the total
rather than the subtotal; and the negative credit line. When the question can't be answered from
the candidates, the exit status is 1.

## Options worth knowing

| option | what it does |
|---|---|
| `-e REGEX` | Candidates are regex matches, printed verbatim (repeatable; Go RE2 syntax). |
| `-m N` | Print the N best candidates, best first. |
| `-s`, `-n` | Prefix the probability and the line number. |
| `-C N` | Lines mode: show N lines of context around the pick. |
| `-t P` | How sure the "is it answered at all?" check must be (default 0.5). |
| `--min-conf C` | Exit 3 when the pick itself is less certain than C (default 0.5). |
| `--force` | Print the best candidate even when nothing answers the question. |
| `--about TEXT` | Say what the input is. |

The rest is in `man pickv` or `pickv --help`.

## Exit status

| code | meaning |
|---|---|
| 0 | found |
| 1 | nothing answers the question |
| 2 | error |
| 3 | found, but the pick is uncertain |
| 4 | declined at `-Q`, or over a budget |

## See also

- [grev](grev.md) keeps every line that answers yes, rather than the single best one.
- [rank](rank.md) sorts all lines by how well they fit.
- [cutv](cutv.md) picks table columns instead of lines.
- [DESIGN](../DESIGN.md) explains why `pickv` pairs its choice with an "is it answered at all?"
  check, and why it sends line-tagged documents.
