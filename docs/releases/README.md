# Curated Release Notes

Files here are optional, hand-written intros for a release. Name a file after
the tag it belongs to — `v0.1.0.md` for tag `v0.1.0` — and its contents are
placed above the generated commit changelog in the published GitHub Release.

Tags without a matching file publish the generated sections alone.

The first release is a special case: with no previous tag to diff against, the
changelog would be every commit in the repository's history, so it prints
`Initial release.` instead. Curated notes carry that release.

Good things to put here:

- The headline change, in a sentence.
- Breaking configuration or schema changes, and what an operator must do.
- Upgrade steps beyond `docker compose pull && docker compose up -d`.

Preview the result before tagging:

```bash
make release-notes TAG=v0.1.0
```

See [release-process.md](../release-process.md) for the full flow.
