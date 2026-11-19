# Word lists

The campaign generator uses these lists to check the levels its agent designs. Both files are gzipped and embedded in the Worker binary.

| File | Source | Words | License |
|---|---|---|---|
| `enable1.txt.gz` | ENABLE (Enhanced North American Benchmark Lexicon), copy from [dolph/dictionary](https://github.com/dolph/dictionary/blob/master/enable1.txt) | 172,823 | Public domain |
| `scowl-50-us.txt.gz` | SCOWL / English Speller Database, size 50, US spelling, generated from [app.aspell.net](https://app.aspell.net/create?max_size=50&spelling=US&max_variant=0&diacritic=strip&download=wordlist&encoding=utf-8&format=inline) on 2026-11-19 | about 62,000 lowercase entries | Notice below; the generated file also carries it |

How the game uses them:

- **English words** are ENABLE. A level answer that isn't in ENABLE is rejected as not a word.
- **Common words** are ENABLE words that are also in SCOWL size 50. Every level answer must be common, because players have to find every answer to finish a level. The intersection keeps out ENABLE's obscure entries (AIRT, ADIT, AGER) and SCOWL's abbreviations (ABBR, ADJ).

To refresh SCOWL, download the URL above and gzip it with `gzip -9n`.

## SCOWL copyright notice

```
Copyright 2000-2026 by Kevin Atkinson

Permission to use, copy, modify, distribute, and sell any part of the English
Speller Database (ESDB, previously known as SCOWLv2), or word lists
created from it, is hereby granted without fee, provided that the above
copyright notice appears in all copies and that both the above copyright
notice and this notice appear in supporting documentation.  Kevin Atkinson
makes no representations about the suitability of this database for any
purpose.  It is provided "as is" without express or implied warranty.
```
