# Eval results

Run with `make eval` (live; about $0.002). Model `jev-1.13.0`, 2026-09-24. There are 5 labelled sets
of 20–30 records each (menu items, log lines, commit messages, support messages, Go source lines).

| variant (mean over sets)                          | acc@0.5 | acc@best-T | AUC   |
|---------------------------------------------------|--------:|-----------:|------:|
| record in question, natural `{}` question + about | 0.983   | 1.000      | 1.000 |
| record in question, natural `{}` question         | 0.973   | 0.990      | 0.996 |
| record in question, predicate template + about    | 0.967   | 1.000      | 1.000 |
| record in question, statement template            | 0.953   | 0.980      | 0.984 |
| record in question, predicate template            | 0.933   | 0.980      | 0.978 |
| same, 8 questions per request (vs 128)            | 0.933   | 0.980      | 0.978 |
| all records in state, question points at `lines[i]` | 0.903 | 0.943      | 0.969 |

Decisions:

- **The record goes inside each question** (`{"text": …, "question": …}`), not in a shared state
  array. Addressing records by path adds a hop of indirection and costs accuracy. It was worst on
  short, similar records.
- **Default template:** `Is it true that \`text\` QUERY?`. Naming the domain helps the most:
  `--about "git commit messages"` or a natural `{}` question ("Does the commit message {} describe
  a bug fix?") adds 3–5 points. The help texts say so.
- **Up to 128 questions per request.** Packing 8 or 128 gives the same answers; 128 costs fewer
  request overheads.
- **Threshold 0.5.** The best threshold per set ranged from 0.05 to 0.6, with no consistent
  offset. Calibration holds up well enough that a global 0.5 is the honest default. Tune `-t`
  per use.

Measured request framing: about 262 tokens per request plus about 7 per question
(`internal/jev/engine.go`). Estimates run about 8% above actual usage.
