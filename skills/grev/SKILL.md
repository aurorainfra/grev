---
name: grev
description: Unix filters that ask a model yes/no, choice and scoring questions about text instead of matching patterns - grev (grep by meaning), isv (a yes/no answer as the exit status), pickv (the one line or regex match that answers a question), rank and sortv (sort by meaning), tagv (label or route records), uniqv (dedupe by meaning), trv (edit only where an instruction applies), lookv (bisect ordered input), seek (find a file by description), cutv (CSV columns by description), probev, oneof, seg and unwrap. Use when text has to be filtered, searched, sorted, classified or judged by meaning rather than by an exact pattern - logs, tickets, diffs, commit messages, CSVs, man pages, docs - especially inputs too large to read into context. Output is always the input's own records.
license: MIT OR Apache-2.0
compatibility: Needs the grev tools on PATH, network access to the Jev API, and an API key stored with grev-settings.
metadata:
  source: https://github.com/aurorainfra/grev
---

# grev tools

Classic Unix filters (`grep`, `test`, `sort`, `uniq`, `tr`, `cut`, `find`, `git bisect`)
where the pattern is a plain-English question. They pipe, take files, and use exit codes
like the originals.

## Jev in brief

The tools run on TypeSafe's Jev ("System One") models. Jev does not write text: it answers
typed questions with calibrated probabilities:
- **yes/no**: a probability
- **choice**: one of up to 255 labels
- **score**: a position on 2–10 ordered levels

So every tool behaves like a filter. **Output is always your input**: records selected,
reordered, labelled, split or counted, never model-written text. Scores and labels appear only
as explicit columns (`-s`).

It is fast and cheap. Input costs about $0.042 per million tokens, and output is free. Measured
runs:
- 10,000 web-log lines: 2.3 s, 3¢, 12 of 12 hidden attacks found, no false alarms
- a 900 kB coloured log: about 2¢
- a 1,100-line man page: 0.6 s, a tenth of a cent

**Why an agent should reach for it:** you can screen inputs far larger than your context without
reading them. You only read what comes back.

## Before the first run

```sh
grev-settings key status      # shows the key source (masked) and checks it works
```

If there is no key, ask the user to run `grev-settings key set`. It reads the key from their
terminal. Never handle, print or search for the key yourself, and never read `~/.grevconfig`
directly.

**Your input is sent to the configured API** (TypeSafe by default). Ask the user before piping
in private data or files with secrets.

## Which tool

| task | tool | like |
|---|---|---|
| keep the records (lines) that match a condition | `grev 'COND' FILE` | grep |
| one yes/no about the whole input, as the exit status | `isv 'COND' < FILE` | test |
| the single line (or `-e REGEX` match) that answers a question | `pickv 'QUESTION?' FILE` | grep -o |
| label every record, or route records into files | `tagv L1 L2 … < FILE`, `--split DIR` | awk |
| one label for the whole input | `oneof L1 L2 … < FILE` | case |
| score and sort records against a query or levels | `rank 'QUERY' FILE`, `-L 'lo\|…\|hi'` | sort |
| sort by an order described in words | `sortv 'ORDER' FILE` | sort |
| collapse adjacent records that mean the same | `uniqv FILE` | uniq |
| first record where the answer flips (ordered input) | `lookv 'COND' FILE` | git bisect |
| a file or directory in a tree, by description | `seek 'DESCRIPTION' DIR` | find |
| CSV/TSV columns by description | `cutv 'DESC'… < TABLE` | cut |
| probability columns for several questions | `probev -q 'name: question'… FILE` | awk |
| edit only where an instruction applies | `trv SET1 SET2 'INSTR'`, `trv -e RE -r TEXT 'INSTR'` | tr, sed |
| split a stream into topics | `seg FILE` | csplit |
| rejoin hard-wrapped lines | `unwrap FILE` | fmt |

Records are lines by default. `--para` makes them paragraphs, `-z` NUL-terminated, and
`-d DELIM` splits fields you can cite as `{1}`, `{2}` in the question. `{}` is the whole record.

## Rules that matter

- **Phrase a condition about one record**, positively: `'is an attack or exploit attempt'`,
  `'mentions a timeout'`, `'is a vegan meal'`. Not `'find the attacks'`.
- **Give context** with `--about 'what the input is'`, e.g. `--about 'nginx access log'`. It helps a lot.
- **Use the classic tool when meaning isn't needed.** `grep`, `awk` and `sort` are free and exact.
  The model is weak at arithmetic and date comparison. Combine them, cheap first:
  `grep -i error app.log | grev 'is caused by the database'`.
- **Pass whole files, don't loop.** The tools pack up to 128 questions per request and run
  requests in parallel.
- **Budget:** pass `--max-cost USD` on anything big.
  - If the quote exceeds it, nothing is sent: exit 4, with the quote on stderr. Show it to the
    user before re-running with a higher cap.
  - Without a terminal, a run quoted above $1 is refused the same way unless `--max-cost` covers it.
  - Don't use `-Q`: it needs a terminal to confirm.
- **Exit codes:** 0 yes/match, 1 no/none, 2 error, 3 uncertain (`isv --band`, `pickv`, `oneof -t`),
  4 declined or over budget.
- **Tuning:**
  - `-s` shows each record's probability.
  - `-t P` moves the yes threshold (default 0.5).
  - `grev --band LO:HI --uncertain FILE` sets borderline records aside.
  - `-p` shows progress and cost on stderr.

## Recipes

```sh
# Needle in a haystack: every line judged, only matches printed
grev --max-cost 0.10 --about 'web access log' 'is an attack or exploit attempt' access.log
grev -c 'mentions a timeout' app.log                       # count instead of print
git log --oneline | grev --about 'commit messages' 'fixes a bug'

# Guard or branch on a yes/no (exit 0 = yes)
git diff --cached | isv 'adds a secret or credential' && echo 'secret in the diff'

# One answer from a long document
man rsync | pickv 'how do I exclude a directory?'
pickv -e '[\w.+-]+@[\w.-]+\.\w+' 'where should the receipt be sent?' mail.eml   # prints the match verbatim

# Where did it start? A few requests even for thousands of ordered lines
lookv -n 'reports a failing dependency' health.log

# Find code or files by description
seek 'where the spend ledger is written' .

# Classify, route, rank, sort, dedupe
tagv billing technical feature-request other < tickets.txt
tagv --split out/ billing technical other < tickets.txt
rank -s -m3 -L 'not urgent|soon|urgent|critical' 'How urgent is {}?' tickets.txt
sortv 'chronologically, earliest first' events.txt
uniqv -c alerts.log

# Structured data
cutv 'email address' 'phone number' < users.csv
probev -H -q 'refund: asks for money back' -q 'angry: the writer is angry' < tickets.txt

# Edits by meaning (everything else is copied byte for byte)
trv "'" '"' 'is used as a quotation mark, not an apostrophe' < story.txt
trv -e '\bSt\.' -o 'Saint|Street' 'what St. abbreviates here' < addresses.txt
```

## More

- `TOOL --help` and `man TOOL` for every option.
- `man grev-tools` for an overview, and `man grevconfig` for defaults, spend caps and model settings.
- `grev-settings spend` for what has been spent today and this month.
