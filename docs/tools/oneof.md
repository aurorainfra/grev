# oneof

**case for questions.** oneof asks which of your labels best describes the whole input and prints
that label. It's a single Choice question (up to 255 labels) whose answer is always one of the
labels you gave: never a paraphrase, never an invented category. That makes it safe to use in
`case` statements, routers and scripts.

## Example: route incoming support messages

Each file in an inbox holds one message. A small script sends each one where it belongs:

```console
$ head -n1 101.txt 102.txt 103.txt
==> 101.txt <==
I was charged twice for order A-104, please refund the duplicate.

==> 102.txt <==
The app crashes every time I tap "Save" on the profile screen.

==> 103.txt <==
How do I change the email address on my account?
$ cat route.sh
for f in "$@"; do
  case "$(oneof bug feature question billing < "$f")" in
    bug)     echo "$f → issue tracker" ;;
    billing) echo "$f → finance@" ;;
    *)       echo "$f → support queue" ;;
  esac
done
$ sh route.sh 101.txt 102.txt 103.txt
101.txt → finance@
102.txt → issue tracker
103.txt → support queue
```

oneof sends one request per message and gets back one label from the given list. The `case`
arms can never see anything else.

## More examples

`--probs` shows the whole distribution. Labels can carry a description after `=` to sharpen the
boundaries, and `-s` prefixes the confidence:

```console
$ echo 'Do you offer discounts for non-profits?' | oneof --probs bug feature question billing
0.90	question
0.10	billing
0.00	bug
0.00	feature
$ echo 'Can you add dark mode? My eyes hurt at night.' | oneof -s 'bug=something that used to work is broken' 'feature=asks for something new' 'question=asks how to do something'
1.00	feature
```

A choice always picks *something*. `--other` adds an escape label, and exit 1 tells a script that
nothing fit:

```console
$ echo 'Love the new update, the search is so much faster!' | oneof --other bug feature question billing; echo "exit=$?"
other
exit=1
```

With `-t`, a low-confidence answer still prints its label but exits 3, so a script can escalate it
instead of guessing:

```console
$ echo 'Why does my invoice show two seats when the app only lets me add one user?' | oneof -s -t 0.8 bug feature question billing; echo "exit=$?"
0.40	question
exit=3
```

## Options worth knowing

| option | meaning |
|---|---|
| `LABEL=DESCRIPTION` | describe what a label covers, for labels that are easy to confuse |
| `-f FILE` | read labels from a file, one `label<TAB>description` per line |
| `--other[=NAME]` | add an escape label; exit 1 when it wins |
| `-s`, `--probs` | print the confidence, or every label's probability |
| `-t C` | exit 3 when confidence is below C (the label is still printed) |
| `-q QUESTION` | ask something other than "Which option best describes the input?" |
| `--about TEXT` | what the input is |
| `-i FILE` | read the input from a file instead of stdin |

All options: `oneof --help` or `man oneof`.

## Exit status

0 a label was picked, 1 the `--other` label won, 2 error, 3 confidence below `-t` (the label is
still printed), 4 declined at `-Q` or over `--max-cost`.

## See also

- [tagv](tagv.md): the same Choice for every record instead of the whole input.
- [isv](isv.md): a single yes/no.
- [probev](probev.md): many questions per record at once.
