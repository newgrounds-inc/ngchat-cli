# Cutting a release

A `v*` tag is the whole trigger: `.github/workflows/release.yml`
reruns vet and the race tests on Linux and macOS, runs govulncheck,
and only then lets GoReleaser build the six archives, `checksums.txt`
and the GitHub release. A tag on a red commit does not ship. Nothing
is built locally; `goreleaser release --snapshot --clean` is only a
dry run of the config.

## Release candidate

The README pins the current candidate because `@latest` still resolves
to the old `v0.1.0` scaffold until `v1.0.0` exists. So every rc is a
pin commit that is also the tagged commit:

```sh
sed -i 's/@v1.0.0-rc.7/@v1.0.0-rc.8/' README.md
git commit -am 'docs(readme): pin rc.8'
git tag v1.0.0-rc.8
git push origin main && git push origin v1.0.0-rc.8
gh run watch                                   # release workflow
gh release view v1.0.0-rc.8                    # six archives + checksums
```

Land the code first, in its own commits, so the pin commit is only
the pin; a CHANGELOG line for the change belongs with the change under
`[Unreleased]`, not in the pin. The tag name is the version the binary
reports (`-X main.version={{.Version}}`), so `ngchat -version` on the
published archive is the check that the right commit shipped.

GoReleaser writes the release notes from the commit subjects since the
previous tag, grouped into Features (`feat`) and Bug fixes (`fix`);
`docs`, `test`, `chore`, `ci`, `style` and `build` are filtered out.
Subjects are the notes, so a `refactor` that operators must act on
(an env var rename, a default that changed) also needs its CHANGELOG
line, since it lands under "Other" with no explanation.

## `v1.0.0` and later

Same steps, plus before the tag is pushed: `CHANGELOG.md` gets
`[1.0.0] - YYYY-MM-DD` in place of `[Unreleased]` with a fresh empty
`[Unreleased]` above it, and the README pin can drop back to `@latest`
once the tag exists. The one-time checks around the first stable tag
are section 10 of `docs/manual-test-plan.md`.

## When it goes wrong

- The test or vuln job fails: fix on `main`, then move the tag
  (`git tag -f`, `git push -f origin <tag>`) only if the release did
  not publish; once GoReleaser has published, cut the next rc instead.
  A moved tag breaks `go install @<tag>` for anyone who fetched it.
- GoReleaser fails after the tests: check `gh release view <tag>`
  for a partial release and delete it (`gh release delete <tag>`,
  which keeps the git tag) before re-running the workflow from the
  Actions tab; GoReleaser refuses to upload over an existing release.
