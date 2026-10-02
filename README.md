# grev

Unix filters that ask questions instead of matching patterns.

```console
$ cat examples/menu.txt
chicken tikka masala
fresh fruit salad
spaghetti carbonara
tofu stir fry with rice

$ grev 'is a vegan meal' examples/menu.txt
fresh fruit salad
tofu stir fry with rice
```

The tools run on [TypeSafe's](https://typesafe.ai) Jev models, which answer typed questions with
calibrated probabilities instead of generating text. That's why these tools behave like `grep`,
`sort` or `cut`: **output is always your input**. Labels and scores appear only as explicit
columns. A few thousand lines cost fractions of a cent and take seconds.

## Examples

```sh
# Search logs by meaning
grev --about 'nginx error log' 'says the upstream server timed out' examples/nginx-error.log
journalctl -fu myapp | grev --line-buffered --about 'application log' 'shows the process crashed'
git log --oneline | grev --about 'commit messages' 'fixes a bug'

# Guard a commit: exit 0 means yes
git diff --cached | isv 'adds a secret or credential' && echo 'refusing to commit' && exit 1

# Label, route, rank
tagv billing technical feature-request other < examples/tickets.txt
tagv --split tickets/ billing technical other < examples/tickets.txt
rank -s -m3 -L 'not urgent|soon|urgent|critical' 'How urgent is the ticket {}?' examples/tickets.txt
sortv 'chronologically, earliest first' examples/sortv-events.txt

# Find, cut, dedupe, reflow
man tar | pickv 'how do I list the contents of an archive?'
cutv 'email address' 'phone number' < examples/users.csv
uniqv -c 'Are {1} and {2} the same company?' examples/companies.txt
unwrap examples/memo.txt
seek --verify 'the spend ledger' .

# Bisect by meaning: the first health check that reports a failure (3 requests for 600 lines)
lookv -n --about 'health checks, one per minute' 'reports a failing dependency' examples/lookv-health.log

# Edit by meaning: quote marks but not apostrophes; St. → Saint or Street by context
trv "'" '"' 'is used as a quotation mark, not an apostrophe' < examples/trv-story.txt
trv -e '\bSt\.' -o 'Saint|Street' 'what St. abbreviates here' < examples/trv-addresses.txt

# Probabilities as columns, for awk
probev -H -q 'refund: asks for money back' -q 'angry: the writer is angry' < examples/tickets.txt
```

| tool | like | does |
|---|---|---|
| [`grev`](docs/tools/grev.md) | grep | print the records the model says yes to |
| [`isv`](docs/tools/isv.md) | test | answer a yes/no question about the whole input with the exit status |
| [`oneof`](docs/tools/oneof.md) | case | print which label fits the whole input |
| [`tagv`](docs/tools/tagv.md) | awk | label every record; `--split DIR` routes records into files |
| [`sortv`](docs/tools/sortv.md) | sort | sort in an order described in words, by pairwise comparison |
| [`rank`](docs/tools/rank.md) | sort | sort records by how well they fit, or by ordered levels |
| [`pickv`](docs/tools/pickv.md) | grep -o | the one line or regex match that best answers a question |
| [`uniqv`](docs/tools/uniqv.md) | uniq | collapse adjacent records that mean the same thing |
| [`unwrap`](docs/tools/unwrap.md) | fmt | re-join hard-wrapped lines |
| [`seg`](docs/tools/seg.md) | csplit | split a stream into topic segments |
| [`cutv`](docs/tools/cutv.md) | cut | cut the CSV/TSV columns that match a description |
| [`seek`](docs/tools/seek.md) | find | walk a directory tree to what a description names |
| [`trv`](docs/tools/trv.md) | tr, sed | translate, delete, squeeze or replace only where an instruction applies |
| [`lookv`](docs/tools/lookv.md) | look, git bisect | find where an ordered input's answer flips, in a few rounds |
| [`probev`](docs/tools/probev.md) | awk | print per-record probability columns |
| [`grev-settings`](docs/tools/grev-settings.md) | git config | config, API key, spend, agent skill, models, one-off and raw requests |

Every tool has `--help` and a man page. The common flags:

| flag | meaning |
|---|---|
| `-p` | progress, with live and projected cost |
| `-Q` | show the quote and ask first |
| `-J N` / `-Jmax` | parallelism |
| `--max-cost` | per-run budget |

## Install

- **Packages:** deb, rpm, apk, Arch packages and archives for Linux, macOS, FreeBSD and
  Windows are on the [releases page](https://github.com/aurorainfra/grev/releases).
  `packaging/arch/PKGBUILD` builds from source.
- **With Go:** `go install github.com/aurorainfra/grev/cmd/...@latest`
- **From source:** `make && sudo make install`, which also installs the man pages and bash/zsh/fish
  completions.

## Use with coding agents

The tools ship with an [Agent Skill](skills/grev/SKILL.md) that teaches coding agents what the
tools do, when to use them over `grep` and `sort`, how to keep costs capped, and practical
recipes:

```sh
grev-settings skill install            # ~/.claude/skills (Claude Code) and ~/.agents/skills
grev-settings skill install --project  # the same, for the repository you're in
grev-settings skill status             # check it's installed and current
```

`~/.agents/skills` is read by Codex, Gemini CLI, GitHub Copilot, Cursor, OpenCode, Goose and Amp.
With a deb, rpm, apk or Arch package the skill is a link to `/usr/share/grev/skills/grev`, so
package upgrades keep it current. The skill also installs with the usual skill installers:
`npx skills add aurorainfra/grev` or `gh skill install aurorainfra/grev grev`.

## Configure

Everything lives in one file, `~/.grevconfig`, in git-config style; the full reference is
[`docs/CONFIG.md`](docs/CONFIG.md) (or `man grevconfig`). `grev-settings key set` puts your
[API key](https://console.typesafe.ai/keys) there with mode 0600:

```ini
[api]
	key = tsk-…                           ; written by `grev-settings key set`
[defaults]
	progress = auto                       ; the -p overlay whenever stderr is a terminal
	jobs = max
	confirmAbove = 0.25                   ; ask before runs quoted above $0.25 (built-in: $1)
[limits]
	daily = 5                             ; spend caps in USD, across all tools
	monthly = 50
[tool "grev"]
	about = application logs              ; per-tool defaults for any long option,
	threshold = 0.7                       ; e.g. how sure the model must be to say yes
```

Thresholds are set per tool because `-t` means different things: P(yes) in `grev`, the minimum
confidence of a choice in `tagv`, a path score in `seek`.

Manage it with `grev-settings config set limits.daily 5` or
`grev-settings config list --show-origin`, and check your spend with `grev-settings spend`.
Command-line flags beat environment variables, which beat the config. In CI, `TYPESAFE_API_KEY`
supplies the key without any file.

### Through OpenRouter

[OpenRouter](https://openrouter.ai) serves the same Jev API, so the tools run there with your
OpenRouter key. Point grev at it first, because `key set` checks the key against the configured
endpoint:

```sh
grev-settings config set api.endpoint https://openrouter.ai/api
grev-settings config set api.model jev-latest   # or jev-1.13; the default jev-1.13.0 isn't served there
grev-settings key set                           # paste your OpenRouter key
```

### Through Fastino

[Fastino](https://fastino.ai) serves the same decision API with its GLiDE model:

```sh
grev-settings config set api.endpoint https://api.fastino.ai
grev-settings config set api.model fastino/GLiDE
grev-settings key set                           # paste your Fastino key
```

GLiDE costs $0.30 per million input tokens and bills the shared context once per question, so
runs cost several times more than on Jev; quotes account for it.

## Cost and safety

Jev charges $0.042 per million input tokens, and output is free. Grepping a 4,000-line source
file costs about $0.008.

Nothing big runs by surprise:
- Any run quoted above `confirmAbove` ($1 unless configured) asks first. Without a terminal, it
  refuses.
- `--max-cost` and the daily/monthly caps stop a run before it goes over.
- Closing the output pipe (`| head`) stops the spending.
- Failed requests are retried with backoff. If one still fails during a big run, the tool asks
  on the terminal whether to retry, skip it or stop, rather than throw away what the run has
  already spent.

## More

- **[`docs/DESIGN.md`](docs/DESIGN.md):** how the tools phrase questions and why, the evals,
  flag conventions and the cost model.
- **[`docs/CONFIG.md`](docs/CONFIG.md):** configuration in depth.
- **Man pages:** `grev-tools(7)`, `grevconfig(5)`, and one per tool.

## License

Licensed under either of [Apache License, Version 2.0](LICENSE-APACHE) or
[MIT license](LICENSE-MIT), at your option.

Unless you explicitly state otherwise, any contribution intentionally submitted for inclusion in
this work by you, as defined in the Apache-2.0 license, shall be dual licensed as above, without
any additional terms or conditions.
