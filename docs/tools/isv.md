# isv

**test(1) for questions.** isv asks one yes/no question about the whole input (stdin or a file)
and answers with its exit status: 0 for yes, 1 for no. It prints nothing unless you ask for the
probability, so it drops straight into `if`, `&&`, git hooks and CI steps.

## Example: stop a commit that adds a secret

```console
$ cat examples/staged.diff
diff --git a/internal/payments/client.go b/internal/payments/client.go
index 3f2a1b0..9c8d7e6 100644
--- a/internal/payments/client.go
+++ b/internal/payments/client.go
@@ -12,6 +12,8 @@ import (
 const defaultTimeout = 10 * time.Second
 
+const paymentsAPIKey = "pay_prod_7Hq2xK9mVw4Lr8Tn3Zs6Yd1Pc5"
+
 // Client talks to the payment provider.
 type Client struct {
 	http *http.Client
$ isv -s 'adds a secret or credential' < examples/staged.diff; echo "exit=$?"
0.99
exit=0
```

A harmless diff is a firm no:

```console
$ git diff --cached | isv -s 'adds a secret or credential'; echo "exit=$?"
0.01
exit=1
```

The staged change in that run was a typo fix in `README.md`. As a pre-commit hook
(`.git/hooks/pre-commit`):

```sh
#!/bin/sh
if git diff --cached | isv 'adds a secret or credential'; then
	echo 'refusing to commit: the staged diff looks like it adds a secret' >&2
	exit 1
fi
```

The question is asked once, with the whole input as the model's context. No pattern list is
involved, so it doesn't matter whether the key is called `stripeKey`, `apiToken` or
`paymentsAPIKey`.

## More examples

Triage a CI failure before paging anyone; `--about` says what the input is:

```console
$ isv -s --about 'CI job log' 'the build failed because of a flaky or environment-related test, not a code bug' examples/isv-ci.log; echo "exit=$?"
0.85
exit=0
$ isv -s --about 'CI job log' 'a test assertion about business logic failed' examples/isv-ci.log; echo "exit=$?"
0.08
exit=1
```

Some inputs are genuinely borderline. `--band` turns the middle into exit 3, so a script can hand
those to a person:

```console
$ echo 'Please get back to me by Friday about the renewal.' | isv -s --band 0.3:0.7 'the message is urgent'; echo "exit=$?"
0.42
exit=3
$ echo 'The checkout page has been down for an hour, we are losing orders!' | isv -s --band 0.3:0.7 'the message is urgent'; echo "exit=$?"
0.98
exit=0
```

## Options worth knowing

| option | meaning |
|---|---|
| `-s` | print P(yes) on stdout |
| `-v` | exit 0 for no and 1 for yes |
| `-t P` | yes when P(yes) ≥ P (default 0.5) |
| `--band LO:HI` | exit 3 when LO ≤ P(yes) ≤ HI; yes means above HI |
| `--yes TEXT`, `--no TEXT` | spell out what counts as yes and no, for subtle questions |
| `--about TEXT` | what the input is |
| `--chunks any\|all\|mean` | split an input too large for one request and combine the answers |

All options: `isv --help` or `man isv`.

## Exit status

0 yes, 1 no, 2 error, 3 uncertain (with `--band`), 4 declined at `-Q` or over `--max-cost`.

## See also

- [grev](grev.md): the same question asked of every line.
- [oneof](oneof.md): pick one of several answers instead of yes/no.
- [lookv](lookv.md): where in an ordered input the answer flips from no to yes.
