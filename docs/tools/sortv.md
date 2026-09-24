# sortv

**sort by a description.** sortv orders records along a dimension you describe in words:
"chronologically, earliest first", "from lightest to heaviest", "by how risky the change is,
safest first". Output is your records, reordered.

It works in two steps:
1. **Seed.** One request places every record on a five-step scale, from the start of the order
   to its end. The whole list (or a sample of it) is included as context.
2. **Refine.** Odd-even passes then compare neighbours pairwise ("should A come before B?") and
   swap the ones that are out of order. Each pair is asked both ways round to cancel position
   bias. Passes stop early once nothing moves.

## Example: history, in order

```console
$ cat examples/sortv-events.txt
The first iPhone goes on sale
Gutenberg prints the first Bible with movable type
The Berlin Wall falls
Apollo 11 lands on the Moon
The French Revolution begins with the storming of the Bastille
World War I begins
The first web page goes online at CERN
Columbus reaches the Americas
The Wright brothers make the first powered flight
The Roman Empire in the West falls
$ sortv -p -n --trace 'chronologically, earliest first' examples/sortv-events.txt
sortv: seed: placed 10 records on the scale
sortv: pass 1: compared 5 pairs, swapped 1
sortv: pass 2: compared 4 pairs, swapped 0
sortv: pass 3: compared 5 pairs, swapped 0
10:The Roman Empire in the West falls
2:Gutenberg prints the first Bible with movable type
8:Columbus reaches the Americas
5:The French Revolution begins with the storming of the Bastille
9:The Wright brothers make the first powered flight
6:World War I begins
4:Apollo 11 lands on the Moon
3:The Berlin Wall falls
7:The first web page goes online at CERN
1:The first iPhone goes on sale
sortv: 38 q · 4 req · 4,397 tok · $0.0002 · 1s · jev-1.13.0
```

- **Seed:** already nearly right. One comparison pass fixed the single pair that was out of order,
  and two quiet passes confirmed it.
- **Dates:** none of the lines contains a date. The order comes from what the model knows about
  each event.

## More examples

Where the seed is coarse, the passes matter. Twenty animals by typical adult weight, seed only
(`--passes 0`) and then with the default 4 passes:

```console
$ sortv --passes 0 --about 'animals, by typical adult body weight' 'from lightest to heaviest' examples/sortv-animals.txt | paste -sd ';'
hummingbird;honey bee;house mouse;house cat;chicken;rabbit;bald eagle;red fox;emperor penguin;gray wolf;golden retriever;domestic horse;brown bear;domestic pig;giraffe;African elephant;hippopotamus;white rhinoceros;orca;blue whale
$ sortv -p --about 'animals, by typical adult body weight' 'from lightest to heaviest' examples/sortv-animals.txt | paste -sd ';'
honey bee;hummingbird;house mouse;chicken;house cat;rabbit;bald eagle;red fox;emperor penguin;golden retriever;gray wolf;domestic pig;brown bear;domestic horse;giraffe;hippopotamus;orca;white rhinoceros;African elephant;blue whale
sortv: 96 q · 5 req · 8,523 tok · $0.0004 · 1s · jev-1.13.0
```

- **What the passes fixed:** bee vs. hummingbird, and horse vs. bear and pig. The elephant also
  moved from before the hippo, rhino and orca to after them.
- **What's left:** the remaining inversions are close calls (rabbit vs. cat, a couple of kg apart;
  rhino vs. orca). More passes wouldn't change them: 8 passes give the same order and stop early.

## sortv or rank?

| use | when |
|---|---|
| [rank](rank.md) | each record can be scored on its own: how well it fits a query, or where it falls on levels you define (`-L`) |
| sortv | only the relative order matters and no fixed scale fits: history, size, risk, "which should come first" |
| `sort` | the key is literally in the text: numbers, dates, names (the model is weak at arithmetic and date comparison) |

The v in sortv is the family suffix, as in grev and pickv. It has nothing to do with `sort -V`
(version sort).

## Options worth knowing

| option | meaning |
|---|---|
| `--passes N` | comparison passes after the seed (default 4; 0 = seed only). Each costs about one question per record. |
| `-r` | reverse the result |
| `-n` | prefix the original position |
| `-s` | prefix the seed position (0 start … 4 end) |
| `-m N` | print only the first N records |
| `--about TEXT` | what the records are; helps the model know the domain |
| `--trace` | show each pass and how many pairs it swapped |

The rest is in `man sortv` or `sortv --help`.

## Exit status

0 sorted · 1 empty input · 2 error · 4 declined at `-Q` or over a budget.

## See also

- [rank](rank.md): score records on their own and sort by the score.
- [tagv](tagv.md): group records into labels.
- [lookv](lookv.md): find where an already ordered input changes.
