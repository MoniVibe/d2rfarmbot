# R1 evidence (laptop, tested @ 3cec116)

Flight recordings are gzipped (`gunzip -k flight_*.jsonl.gz`); raw total was ~34 MB.
Pairs (flight <-> run, matched by end mtime, 2026-09-23 local +03:00):

| flight | run | window | why picked |
|---|---|---|---|
| flight_1790164752 | run_2026-09-23w | 14:58-15:20 | 20 stuck lines, advance pinned 19s @15:15 |
| flight_1790163625 | run_2026-09-23u | 14:40-14:56 | stand pinned x3 @14:41-14:42, loot stuck d=9, fence errand refusals |
| flight_1790162830 | run_2026-09-23q | 14:26-14:35 | fence pinned x3 @14:29, loot pinned @14:30, restock refusals |
| flight_1790163370 | run_2026-09-23r | ~14:36-14:37 | 8 stuck lines, short run |
| flight_1790163476 | run_2026-09-23s | ~14:38 | 7 stuck lines, short run |
