# seek

**find by description.** Walk a directory tree to the file or directory a description names.

`seek` starts at the root. At every level it asks one Choice question: which of these entries
leads to what you're looking for? Directories are described by a sample of the names beneath
them. The best few paths are kept at each step (a beam search), and a path's score is the
geometric mean of its step probabilities. A `(none)` option at every level lets the model say
"not here", so a query with no answer exits 1 instead of returning junk. Reach for it in an
unfamiliar codebase or a big tree, when you know what something *is* but not what it's
called.

## Example: find the HTTP server in the Go standard library

```console
$ seek -m3 -s 'the HTTP server implementation' "$(go env GOROOT)/src"
0.86	/usr/lib/go/src/net/http/server.go
0.37	/usr/lib/go/src/net/http
0.24	/usr/lib/go/src/net/http/http.go
```

The tree has about 12,000 files. `seek` looked at one directory level per request and asked 4
questions across 3 requests: 10,396 tokens, about $0.0004, in a second. `-m3` prints the three
best candidates and `-s` their scores.

## More examples

Jump to a directory (`-d` finds directories only), here in this repository:

```console
$ cd "$(seek -d 'Arch Linux packaging')"
$ seek -d 'Arch Linux packaging' .
packaging/arch
```

Search only what git tracks, with `-` reading a path list from stdin:

```console
$ git ls-files | seek 'the Arch Linux package recipe' -
packaging/arch/PKGBUILD
$ $EDITOR "$(git ls-files | seek 'the command line option parser' -)"
```

When the query is about what the code *does* rather than what it's called, add `--verify`. The
best candidates are re-checked against their first lines and re-ranked by that answer:

```console
$ seek -s --verify 'where HTTP retries and backoff are implemented' .
0.80	internal/jev/client.go
```

No good match exits 1:

```console
$ seek -s 'a recipe for banana bread' .
seek: best match examples/menu.txt scored 0.03, below -t 0.15
$ echo $?
1
```

## Options worth knowing

| option | what it does |
|---|---|
| `-d`, `-f` | directories only, or files only |
| `-m N`, `-s` | print the N best paths, with scores |
| `-b K` | keep the K best paths at each level (default 3) |
| `--verify` | re-check the best candidates against their content; recommended for queries about behaviour |
| `--peek N` | show the model the first N lines of candidate files while walking |
| `-a` | include hidden entries, `.git` and `node_modules` |
| `-t S` | minimum score to print (default 0.15, 0.5 with `--verify`) |
| `--about TEXT` | what the tree is, e.g. `--about 'a Django monorepo'` |

A search costs about one request per directory level. See `seek --help` or `man seek`.

## Exit status

0 a path scored at least `-t`, 1 none did, 2 error, 4 declined at `-Q` or over a budget.

## See also

[pickv](pickv.md) (the best line inside a file), [grev](grev.md) (every matching line),
[DESIGN](../DESIGN.md) (why every level offers "none").
