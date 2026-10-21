# Validation

Built and checked on Linux with Go 1.23.12. The source declares Go 1.22 minimum, uses only standard-library APIs, and does not pin a toolchain. The user's Go 1.27.1 installation was not available in this execution environment.

Passed:

* `go test -race ./...`
* `go vet ./...`
* `go build -buildvcs=false -o /tmp/warp-bench .`
* A local CLI integration against two fixture targets, performing 60 sequential requests after sitemap discovery. Verified identical plan hashes, successful body reads, gzip handling, bearer POSTs, secret exclusion from results and replay deduplication.

Automated tests cover reproducible state plans, campaign query preservation, cookie isolation, desktop/mobile/tablet UA selection, body completion before subsequent requests, redirect/error accounting, sitemap indexes and gzip, full product visitation before repetition, wrk metric/unit/error parsing, wrk subprocess/Lua-script generation, publishing/replay, and path origin safety.

The wrk subprocess test uses a fixture executable; no real wrk load or live Warp installation was available. Real benchmark numbers will come from running the configured suite on the user's systems. No illustrative throughput values are presented as measured Warp performance.
