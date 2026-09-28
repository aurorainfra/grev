# grev-settings

**Plumbing and admin.** Configure the tools, manage the API key, check spend, and ask the
model questions directly.

The other tools each do one thing to a stream of records. `grev-settings` does everything
around them:
- `config` edits `~/.grevconfig`
- `key` stores and checks the API key
- `spend` reads the local spend ledger
- `skill` installs the agent skill that teaches coding agents to use the tools
- `models` lists what your key can use
- `ask` puts a few questions to the model about one text
- `raw` sends a hand-written API request

## Example: first-time setup

```console
$ grev-settings key set
TypeSafe API key (input hidden):
stored key …a1b2 as api.key in /home/you/.grevconfig (mode 0600)
$ grev-settings key status
source: api.key (~/.grevconfig:2)
file:   /home/you/.grevconfig
key:    …a1b2 (108 chars)
check:  ok (2 models available)
$ grev-settings config set defaults.progress auto
$ grev-settings config set limits.daily 5
$ grev-settings config set tool.grev.about 'application logs'
$ grev-settings config list --show-origin
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
    grev-settings ask -q 'refund: asks for a refund' \
            -q 'dept: Which team should handle it? [billing|tech|sales]' \
            -q 'mood: How upset is the writer? <calm|annoyed|angry>'
refund  noul    0.99
dept    choice  billing (conf 1.00)  billing 1.00 · sales 0.00 · tech 0.00
mood    score   0.26 (conf 0.61)  0:calm 0.74 · 1:annoyed 0.26 · 2:angry 0.00
```

Check what the tools have spent, by tool and by day, against the caps:

```console
$ grev-settings spend --days 3
today       2026-09-24  $0.0023  (cap $5)
this month  2026-09     $0.0023  (no cap)

this month by tool:
  seek          $0.0015
  probev        $0.0004
  cutv          $0.0003
  grev-settings $0.0000

last 3 days:
  2026-09-22  $0.0000
  2026-09-23  $0.0000
  2026-09-24  $0.0023

ledger: ~/.local/state/grev/spend
```

Teach your coding agents to use the tools. This installs the grev
[Agent Skill](../../skills/grev/SKILL.md) for Claude Code (`~/.claude/skills`) and for Codex,
Gemini CLI, Copilot, Cursor, OpenCode, Goose and Amp (`~/.agents/skills`):

```console
$ grev-settings skill install
installed  ~/.claude/skills/grev → /usr/share/grev/skills/grev  (Claude Code)
installed  ~/.agents/skills/grev → /usr/share/grev/skills/grev  (Codex, Gemini CLI, Copilot, Cursor, OpenCode, Goose, Amp)
grev-settings: agents pick the skill up in new sessions; check with `grev-settings skill status`
$ grev-settings skill status
installed     ~/.claude/skills/grev → /usr/share/grev/skills/grev  (Claude Code)
installed     ~/.agents/skills/grev → /usr/share/grev/skills/grev  (Codex, Gemini CLI, Copilot, Cursor, OpenCode, Goose, Amp)
```

How it installs:
- **Link or copy.** With a package installed, the skill is a link to the packaged copy, so
  upgrades keep it current. Otherwise, or with `--copy`, the files are copied; run `install`
  again after upgrading. `status` exits 1 when a copy is missing or outdated.
- **Scope.** `--project` installs into the current directory's `.claude/skills` and
  `.agents/skills`. `--system` (as root) installs into the directories every user's agents read:
  Claude Code's managed settings directory and `/etc/codex/skills`. `--agent claude` or
  `--agent agents` picks one of the two directories.
- **Safety.** A different skill already named `grev` is never overwritten. With `--force` it is
  moved aside to `grev.bak-<time>`.
- **`show`** prints the skill. It is also published as `skills/grev` in the repository, for
  `npx skills add aurorainfra/grev` and `gh skill install aurorainfra/grev grev`.

Send a raw API request (`model` is filled in if missing) and see the unmodified response,
including the token usage the API reports:

```console
$ echo '{"state": "The deploy failed: permission denied writing /var/www",
         "questions": {"perm": {"type": "noul", "instructions": "Is this a permissions problem?"}}}' | grev-settings raw
{"model":"jev-1.13.0","answers":{"perm":{"type":"noul","noul":0.98}},"usage":{"input_tokens":282,"output_tokens":20}}
```

List the models your key can use:

```console
$ grev-settings models
jev-latest     2026-09-10  The latest iteration of TypeSafe's System One Model: Jev
jev-preview    2026-09-10  A preview version of `jev-latest`: should be better in most ways
```

A typo in a config name is flagged, not silently used:

```console
$ grev-settings config set limits.montly 50
grev-settings: warning: limits.montly is not a known config key (see grevconfig(5))
$ grev-settings config unset limits.montly
```

## Commands

| command | what it does |
|---|---|
| `config list [--show-origin]`, `get`, `set`, `unset`, `edit`, `path` | read and edit `~/.grevconfig`; `api.key` is shown masked |
| `key set` | store a key as `api.key` (typed hidden, or piped on stdin) |
| `key import` | move a key from `TYPESAFE_API_KEY` / `TYPESAFE_API_KEY_FILE` into the config |
| `key status`, `path`, `rm` | show the source in use and check it; where `key set` writes; remove it |
| `spend [--days N]` | today's and this month's spend against `limits.daily` / `limits.monthly` |
| `skill install`, `uninstall`, `status`, `show` | install the agent skill for Claude Code and `~/.agents/skills` agents; `--project`, `--system`, `--agent`, `--copy`, `--force` |
| `models` | the models your key can use |
| `ask -q SPEC… [FILE]` | ask questions about FILE, stdin, `--state TEXT` or `-S FILE`; `--json` for raw answers |
| `raw [FILE]` | POST a request JSON as-is and print the response |

SPEC is the same mini-syntax as [probev](probev.md): `name: question`, `[a|b|c]` for a
Choice, `<lo|mid|hi>` for a Score. See `grev-settings --help`, `man grev-settings`, and
`man grevconfig` for every config key.

## Exit status

0 ok, 1 key check failed, config key not found, or `skill status` found the skill missing or
outdated, 2 error, 4 declined or over a spend limit, 130 interrupted.

## See also

[CONFIG](../CONFIG.md) (config file, key sources, spend caps), [probev](probev.md),
[grev](grev.md), [DESIGN](../DESIGN.md).
