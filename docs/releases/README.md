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
   test decodes every `v*.json` with the current types and checks stable
   values independently of the encoder. Preserve those baseline values in
   future samples; if they must change, retain the old expectations and add
   explicit expectations for the new release fixture.
4. Include the new frozen fixture in the validated release commit, and tag
   that commit. Follow the version-specific publishing notes for the rest
   of the release.

Before the first release there are no versioned fixtures. In that case the
compatibility test checks a temporary versioned copy of `snapshot.json` so
the discovery, decoding and field assertions still run on every `make check`.
