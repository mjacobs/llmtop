# Release checklist

At each release, freeze the agent wire format before creating the tag:

1. Run `go test ./internal/model -count=1`. If an intentional wire change
   needs a new golden file, run `go test ./internal/model -update` and review
   the diff in `internal/model/testdata/wire/snapshot.json`.
2. Copy that golden file to `internal/model/testdata/wire/v<version>.json`
   (for example, `v0.1.0.json` when preparing the first release). Never
   regenerate or overwrite a previously released fixture. The `-update`
   flag writes only `snapshot.json`.
3. Run `go test ./internal/model -count=1` and `make check`. The frozen-file
   test decodes every `v*.json` with the current types and checks every
   populated baseline field independently of the encoder. The rich
   `wireSample()` derives from `sample()` and populates all current fields,
   including optional fields and nonzero GPU indexes and queue counts.
   Preserve those baseline values in future samples; if they must change,
   retain the old expectations and add explicit expectations for the new
   release fixture.
4. Include the new frozen fixture in the validated release commit, and tag
   that commit. Follow the version-specific publishing notes for the rest
   of the release.

Before the first release there are no versioned fixtures. In that case the
compatibility test checks a temporary versioned copy of `snapshot.json` so
the discovery, decoding and field assertions still run on every `make check`.

The rich snapshot pins the presence and values of optional fields, rather
than every combination in which those fields may be omitted at runtime.
It is a synthetic contract sample, not a recording of a live server state.
Future additive fields are allowed by JSON decoding. When adding one, extend
`wireSample()` and its independent compatibility assertions, and retain
version-specific expectations for older frozen fixtures where the new field
was absent. Do not require a newly added optional field in an older fixture.
