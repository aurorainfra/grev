# jev

**Plumbing and admin.** Configure the tools, manage the API key, check spend, and ask the
model questions directly.

The other tools each do one thing to a stream of records. `jev` does everything around them:
- `config` edits `~/.grevconfig`
- `key` stores and checks the API key
- `spend` reads the local spend ledger
- `models` lists what your key can use
- `ask` puts a few questions to the model about one text
- `raw` sends a hand-written API request

## Example: first-time setup

```console
$ jev key set
TypeSafe API key (input hidden):
stored key …a1b2 as api.key in /home/you/.grevconfig (mode 0600)
$ jev key status
source: api.key (~/.grevconfig:2)
file:   /home/you/.grevconfig
key:    …a1b2 (108 chars)
check:  ok (2 models available)
$ jev config set defaults.progress auto
$ jev config set limits.daily 5
$ jev config set tool.grev.about 'application logs'
$ jev config list --show-origin
~/.grevconfig:2	defaults.progress=auto
~/.grevconfig:5	limits.daily=5
~/.grevconfig:8	tool.grev.about=application logs
```

`key set` checks the key against the API before storing it. `key status` never prints more than
the last four characters. `config set` edits the file in place and keeps your comments.
- `defaults.progress auto`: every tool shows its progress overlay when stderr is a terminal.
- `limits.daily 5`: caps spend across all tools at $5 a day.
- `tool.grev.about`: gives `grev` a default `--about`.

(The key and the home path above are placeholders; the rest is real output.)

## More examples

Ask the model directly. This is handy for trying out a question before using it in
[probev](probev.md) or [grev](grev.md):

```console
$ echo 'I was charged twice for order A-104, please refund the duplicate.' |
    jev ask -q 'refund: asks for a refund' \
            -q 'dept: Which team should handle it? [billing|tech|sales]' \
            -q 'mood: How upset is the writer? <calm|annoyed|angry>'
refund  noul    0.99
dept    choice  billing (conf 1.00)  billing 1.00 · sales 0.00 · tech 0.00
mood    score   0.26 (conf 0.61)  0:calm 0.74 · 1:annoyed 0.26 · 2:angry 0.00
```

Check what the tools have spent, by tool and by day, against the caps:

```console
$ jev spend --days 3
today       2026-09-24  $0.0023  (cap $5)
this month  2026-09     $0.0023  (no cap)

this month by tool:
  seek     $0.0015
  probev   $0.0004
  cutv     $0.0003
  jev      $0.0000

last 3 days:
  2026-09-22  $0.0000
  2026-09-23  $0.0000
  2026-09-24  $0.0023

ledger: ~/.local/state/grev/spend
```

Send a raw API request (`model` is filled in if missing) and see the unmodified response,
including the token usage the API reports:

```console
$ echo '{"state": "The deploy failed: permission denied writing /var/www",
         "questions": {"perm": {"type": "noul", "instructions": "Is this a permissions problem?"}}}' | jev raw
{"model":"jev-1.13.0","answers":{"perm":{"type":"noul","noul":0.98}},"usage":{"input_tokens":282,"output_tokens":20}}
```

List the models your key can use:

```console
$ jev models
jev-latest     2026-09-10  The latest iteration of TypeSafe's System One Model: Jev
jev-preview    2026-09-10  A preview version of `jev-latest`: should be better in most ways
```

A typo in a config name is flagged, not silently used:

```console
$ jev config set limits.montly 50
jev: warning: limits.montly is not a known config key (see grevconfig(5))
$ jev config unset limits.montly
```

## Commands

| command | what it does |
|---|---|
| `config list [--show-origin]`, `get`, `set`, `unset`, `edit`, `path` | read and edit `~/.grevconfig`; `api.key` is shown masked |
| `key set` | store a key as `api.key` (typed hidden, or piped on stdin) |
| `key import` | move a key from `TYPESAFE_API_KEY` / `TYPESAFE_API_KEY_FILE` into the config |
| `key status`, `path`, `rm` | show the source in use and check it; where `key set` writes; remove it |
| `spend [--days N]` | today's and this month's spend against `limits.daily` / `limits.monthly` |
| `models` | the models your key can use |
| `ask -q SPEC… [FILE]` | ask questions about FILE, stdin, `--state TEXT` or `-S FILE`; `--json` for raw answers |
| `raw [FILE]` | POST a request JSON as-is and print the response |

SPEC is the same mini-syntax as [probev](probev.md): `name: question`, `[a|b|c]` for a
Choice, `<lo|mid|hi>` for a Score. See `jev --help`, `man jev`, and `man grevconfig` for every
config key.

## Exit status

0 ok, 1 key check failed or config key not found, 2 error, 4 declined or over a spend limit,
130 interrupted.

## See also

[CONFIG](../CONFIG.md) (config file, key sources, spend caps), [probev](probev.md),
[grev](grev.md), [DESIGN](../DESIGN.md).
