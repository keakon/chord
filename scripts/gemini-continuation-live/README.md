# Gemini continuation acceptance

Run against the official endpoint with `GEMINI_API_KEY` set:

```sh
go run ./scripts/gemini-continuation-live \
  --prompt-file /path/to/task.txt \
  --max-output-tokens 131072 \
  --thinking-level high \
  --timeout 45m \
  --artifacts /tmp/chord-gemini-continuation-live
```

This makes live API calls and can consume the configured token budget. The runner
explicitly enables continuation for `gemini-4-argon`; it does not change user
configuration or rely on catalog defaults.

Use `--cut-after-checkpoint` to inject one read failure after a checkpoint and
pending output. Use `--cancel-after-checkpoint` separately to verify cancellation.
These options are mutually exclusive. Recovery requires an actual subsequent
request and matching final/preview text; an uncovered recovery sample exits with
an error. Cancellation requires the requested checkpoint cancellation and no
subsequent request.

Inspect `manifest.json` for separate `long_decode_verified`, `recovery_verified`,
and `cancellation_verified` results. A successful ordinary run can still be a
single-hop sample: long decode requires known context usage above 65,536 output
tokens, at least two requests, and matching final/preview text. Partial usage
retains confirmed counters but sets `billing_complete` to false.

Artifacts include the manifest, final text, and callback preview. They exclude
credentials, continuation tokens, and raw protocol bodies. Use a fresh artifacts
directory for each sample.

Local transport and acceptance regression tests run with:

```sh
go test ./scripts/gemini-continuation-live
```
