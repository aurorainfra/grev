# Configuration

Every key, with its type and default, is in the man page generated from the schema:
`man grevconfig`, or `jev --help-man=config | man -l -` from a source checkout.

## Files

- **Global config:** the tools read `${XDG_CONFIG_HOME:-~/.config}/grev/config`, then
  `~/.grevconfig`; later values win. On Windows the first is `%AppData%\grev\config`.
  `jev config set` writes to `~/.grevconfig`, unless only the XDG file exists.
- **`GREV_CONFIG=PATH`** uses that file instead of both. `GREV_CONFIG=` (empty) turns config off.
- **`[include] path = FILE`** reads another file, relative to the including one. Use it to keep
  secrets out of a dotfiles repo.
- **There is no per-project config.** A cloned repository could otherwise point `api.endpoint`
  somewhere else and collect your key.

## Format

It is a subset of git-config:
- `[section]` and `[section "subsection"]` headers; `key = value` lines.
- `#` and `;` comments; `"quoted values"` with `\" \\ \n \t`; a trailing `\` continues a line.
- A bare key means true.
- Section and key names ignore case and dashes: `maxCost`, `max-cost` and `maxcost` are the same
  key.
- **Windows paths:** backslashes start escapes, as in git, so write `C:/Users/me/typesafe.key` or
  `"C:\\Users\\me\\typesafe.key"`.

Mistakes are reported with `file:line`:
- unknown keys and bad values: a warning
- syntax errors: an error (exit 2), except in `jev config`, so a broken file can still be fixed

## Precedence

**command-line flag > environment variable > config (`[tool "x"]` over `[defaults]`) > built-in
default.** For example, `-M` beats `TYPESAFE_DEFAULT_MODEL`, which beats `api.model`, which beats
the pinned `jev-1.13.0`. Boolean options can be switched off for one run with `--no-<flag>`
(`--no-progress`, `--no-scores`); amounts take `off` (`--max-cost=off`, `--confirm-above=off`).

## The API key

The key lives in `~/.grevconfig` like everything else. `jev key set` stores it as `api.key` and
keeps the file at mode 0600; the tools warn if it's readable by anyone else. With a password
manager, set `api.keyCommand` instead (e.g. `pass show typesafe/api`), and the key is never
written to disk.

Automation can supply the key from the environment, which beats the config. The sources are tried
in this order:
1. `TYPESAFE_API_KEY`: the key itself, as in the official SDKs.
2. `TYPESAFE_API_KEY_FILE`: a file holding it (Docker/Kubernetes secrets).
3. `$CREDENTIALS_DIRECTORY/typesafe_api_key`: systemd `LoadCredential=`.

`jev key import` moves a key from those variables into `~/.grevconfig`. `jev key status` shows
which source is in use, masked, and checks that it works.

## Examples

A cautious interactive setup:

```ini
[api]
	keyCommand = pass show typesafe/api
[defaults]
	progress = auto
	confirmAbove = 0.10
[limits]
	daily = 2
	monthly = 20
```

A batch box or CI (no terminal, so anything over the budget refuses instead of asking; the key
comes from `TYPESAFE_API_KEY` or `TYPESAFE_API_KEY_FILE`):

```ini
[defaults]
	jobs = max
	maxCost = 1.00
	confirmAbove = off
[limits]
	daily = 25
```

Per-tool defaults take any long option of that tool:

```ini
[tool "grev"]
	about = application logs
	threshold = 0.6
[tool "tagv"]
	other
	scores
```

A model the built-in price table doesn't know yet:

```ini
[model "jev-1.14.0"]
	price = 0.05          ; USD per million input tokens
[api]
	model = jev-1.14.0
```

## Spend

Every request adds its cost to a local ledger at `${XDG_STATE_HOME:-~/.local/state}/grev/spend`
(`%LocalAppData%\grev\spend` on Windows). Set `GREV_LEDGER` to move it; set it empty to turn it
off, which also turns the caps off. `jev spend` shows today's and this month's spend by tool,
against `limits.daily` and `limits.monthly`.
