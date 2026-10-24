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

Not yet run. No Anthropic API key was available when the set was written. The first live run should record the model, prompt version and summary line here, and tune `../prompt.md` until the thresholds pass. Cases 6 and 7 must never produce an ID.

Watch on the first run:

- Case 3 expects "next Friday", said on Tuesday 2026-11-10, to mean 2026-11-13. The prompt has no rule for "next" and may flag the date as ambiguous instead.
- Case 9 expects `cost_impact.value` to be `"2000"`. The model may write `"$2,000"`; decide during tuning whether the prompt or the expectation should change.
