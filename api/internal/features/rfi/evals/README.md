# RFI extraction evals

Ten dictated transcripts, each scored against the real extraction pipeline and model. Every change to `../prompt.md` or to the extraction model must pass this set before it ships.

## Running

```
make db-up
ANTHROPIC_API_KEY=... LLM_EXTRACT_MODEL=claude-sonnet-5-5 \
TEST_DATABASE_URL='postgres://fa:fa@localhost:5432/fieldassistant_test?sslmode=disable' \
make evals
```

The test is behind the `evals` build tag, so `go test ./...` and `go vet ./...` ignore it. It skips, naming the variable, when any of the three variables is unset. CI runs it as the `api-evals` job in `.github/workflows/api.yml` on pushes to `develop` and `main` that touch `api/internal/features`, `llm` or `pipeline`, and on manual dispatch. The job skips with a notice when the `ANTHROPIC_API_KEY` secret is empty.

The untagged `TestCases_Parse` runs in the normal test suite. It checks that every case file decodes strictly, names only RFI payload fields, and refers only to IDs that exist in its fixture.

## How it works

Each case runs through `pipeline.Drafts.Create` with the real Anthropic client. Its fixture rows are written straight into `lookup_cache` as fresh rows, so lookups never reach Procore. A fake Procore client answers only the project detail call, carrying the case's time zone. The clock is noon on the case's `today` in that zone, so relative dates are deterministic. Each case uses its own project ID.

## Case format

```json
{
  "name": "...",
  "transcript": "...",
  "today": "2026-11-10",
  "time_zone": "America/Chicago",
  "user": {"procore_id": 42, "name": "Pat Field"},
  "fixture": {"users": [{"id": 11, "label": "Ben West", "extra": {"role": "Architect"}}], "locations": [], "...": []},
  "expect": {
    "payload": {"assignee_ids": [11]},
    "absent": ["due_date"],
    "unresolved": [{"field": "assignee_ids", "reason": "ambiguous", "candidate_ids": [12, 13]}],
    "forbid_ids": ["location_id"],
    "any_of": [{"payload": {"location_id": 202}}, {"absent": ["location_id"]}]
  }
}
```

- `payload` keys must equal the validated payload by JSON equality.
- `absent` keys must be missing or empty.
- `unresolved` fields must be flagged. `reason` and `candidate_ids` are checked only when given.
- `forbid_ids` names ID fields that must hold no ID. Any ID there counts as a hallucination.
- `any_of` lists alternatives. Each is merged with the top-level blocks, and the case scores against the alternative that matches best.

## Metrics and thresholds

Each case prints the fields matched over expected, hallucinated IDs, unresolved precision and extraction latency. A summary line follows.

| Metric | Definition | Fails the run when |
| --- | --- | --- |
| Exact-match rate | Matched over expected checks, across all cases | under 0.8 |
| Hallucinated IDs | IDs in the validated payload that are not in the fixture or sit in a `forbid_ids` field | above 0 |
| Unresolved precision | Flagged fields that were expected, over flagged fields; 1 when nothing is flagged | reported only |
| p95 latency | Nearest-rank p95 of extraction time | over 8 s |

An extraction error fails the run and scores that case as all missed.

## Results

### 2026-10-24, `claude-sonnet-5-5`, prompt `7f01a1f2da57`

First live run, after the extraction loop moved to `tool_choice: auto` (the model rejects `any` and `tool`). It passed on the first attempt with no prompt tuning.

| Case | Fields | Hallucinated | Precision | ms |
| --- | --- | --- | --- | --- |
| 01-twisted-column | 5/5 | 0 | 1.00 | 4964 |
| 02-assign-to-mike | 2/2 | 0 | 1.00 | 3740 |
| 03-due-next-friday | 0/1 | 0 | 0.00 | 6183 |
| 04-spoken-sheet | 1/1 | 0 | 1.00 | 3813 |
| 05-spec-no-cost-code | 1/2 | 0 | 0.00 | 5292 |
| 06-location-not-in-tree | 2/2 | 0 | 1.00 | 6743 |
| 07-assignee-not-in-directory | 2/2 | 0 | 1.00 | 6250 |
| 08-impacts-unknown | 2/2 | 0 | 1.00 | 5505 |
| 09-cost-known | 1/1 | 0 | 1.00 | 4396 |
| 10-filler-and-restarts | 5/5 | 0 | 1.00 | 4790 |

```
model claude-sonnet-5-5, prompt 7f01a1f2da57
exact-match rate 0.913 (21/23), hallucinated IDs 0, mean unresolved precision 0.80, p95 latency 6.743s
```

Misses:

- Case 3 left `due_date` empty instead of 2026-11-13 for "next Friday". The prompt has no rule for "next", as predicted.
- Case 5 left `specification_section_id` empty instead of 301.

Cases 6 and 7 produced no IDs. Case 9 wrote the cost value as expected.
