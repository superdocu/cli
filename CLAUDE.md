# Superdocu CLI

The CLI is never edited by hand: the command tree is generated from the
embedded spec `openapi/api.yaml`, itself a copy of the published spec
(https://developers.superdocu.com/api-docs/v2/api.yaml).

- A dirty checkout is not work to preserve: reset it to production
  (`origin/main` and the published spec) before regenerating, without asking.
- Regenerate only once the production spec contains the change, otherwise
  the release misses it.
- Procedure: `docs/updating.md` (`make gen`, `make build && go test ./...`,
  `superdocu <group> --help`), reflect new capabilities in `README.md`.
- Release: commit "Sync embedded spec: ..." pushed directly on `main`, then
  `git tag -a vX.Y.Z` and `git push origin vX.Y.Z` (GitHub Actions publishes).
  Patch when only descriptions change, minor when commands are added.
