# cutv

**cut by description.** Pick table columns by what they hold, not by number or exact header.

`cutv` reads a CSV or TSV table, shows the model the header and a few sample rows, and asks
one Choice question per description: which column matches it. That is one request for any
number of descriptions. The rest of the table then streams through with no further calls. Reach for it when a
header is cryptic (`prim_cntct_eml`) or differs from export to export, and you'd otherwise have
to open the file to find the column number for `cut -f`.

## Example: pull contacts out of a CRM export

```console
$ cat examples/cutv-export.csv
cust_id,acct_nm,prim_cntct,prim_cntct_eml,ph_1,created_ts,mrr_usd,tier_cd,rgn
10231,Northwind Traders,Anne Dodsworth,anne@northwind.example,+1 206 555 0134,2025-11-03T09:12:44Z,1290.00,ENT,NA-W
10232,Blue Harbor Cafe,Luis Ortega,luis.ortega@blueharbor.example,+34 91 555 0199,2026-01-17T15:02:10Z,49.00,STD,EU-S
10233,Kestrel Robotics,Mina Park,mpark@kestrel.example,+82 2 555 0171,2026-02-28T02:40:05Z,480.00,PRO,APAC
10234,Hollis & Webb LLP,Grace Webb,gwebb@holliswebb.example,+44 20 7946 0112,2026-04-09T11:23:59Z,960.00,PRO,EU-W
10235,Sunfield Farms,Tom Reyes,orders@sunfield.example,+1 559 555 0148,2026-06-21T18:05:31Z,49.00,STD,NA-W
$ cutv 'contact email' 'monthly revenue' < examples/cutv-export.csv
prim_cntct_eml,mrr_usd
anne@northwind.example,1290.00
luis.ortega@blueharbor.example,49.00
mpark@kestrel.example,480.00
gwebb@holliswebb.example,960.00
orders@sunfield.example,49.00
```

Neither header says "email" or "revenue", but the model sees `prim_cntct_eml` next to
`anne@northwind.example` and `mrr_usd` next to `1290.00`. Columns come out in the order you
asked for them. The run was one request, 1,413 tokens, about $0.00006.

## More examples

Show the mapping and each confidence on stderr (`-s`), so a script can log what was chosen:

```console
$ cutv -s 'company name' 'signup date' 'plan tier' < examples/cutv-export.csv
cutv: company name → col 2 "acct_nm" (1.00)
cutv: signup date → col 6 "created_ts" (1.00)
cutv: plan tier → col 8 "tier_cd" (1.00)
acct_nm,created_ts,tier_cd
Northwind Traders,2025-11-03T09:12:44Z,ENT
Blue Harbor Cafe,2026-01-17T15:02:10Z,STD
…
```

A description that matches no column prints nothing and exits 1, rather than guessing:

```console
$ cutv 'shoe size' < examples/cutv-export.csv
cutv: shoe size → none (1.00)  no matching column
$ echo $?
1
```

TSV passes through byte for byte. `--no-header` makes the output easy to pipe:

```console
$ tr ',' '\t' < examples/users.csv | cutv --no-header 'email address' | sort
ada@example.com
alan@example.org
grace@example.net
margaret@example.com
```

## Options worth knowing

| option | what it does |
|---|---|
| `-s`, `--show` | print `description → col N "header" (confidence)` on stderr |
| `-t C` | refuse a match below confidence C (default 0.5); exits 3 and prints nothing |
| `--sample N` | show the model N sample rows (default 5); more helps with sparse columns |
| `--no-header` | drop the header row from the output |
| `-d DELIM` | force the delimiter (default: detect tab, comma or `;` from the header) |
| `-i FILE` | read the table from FILE instead of stdin |
| `--about TEXT` | say what the table is, e.g. `--about 'Stripe payouts export'` |

CSV fields are re-encoded, so quoting may be normalised and CRLF becomes LF; the values
themselves don't change. See `cutv --help` or `man cutv` for everything.

## Exit status

0 ok, 1 a description matches no column, 2 error, 3 a match is below the `-t` confidence,
4 declined at `-Q` or over a budget.

## See also

[probev](probev.md) (per-row answers as new columns), [grev](grev.md) (filter the rows),
[DESIGN](../DESIGN.md).
