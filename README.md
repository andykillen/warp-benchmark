# Warp benchmark runner

A single Go executable, using only the standard library. It discovers your product sitemap once, builds a reproducible workload, runs that workload against each target, then executes your configured `wrk` profiles in order. Each finished result is saved locally and optionally POSTed to your existing REST endpoint with a bearer token.

## Start here

Use your installed Go 1.27.1. The module declares Go 1.22 as its minimum and does not pin or download a replacement toolchain.



```bash
cp config.example.json config.json
go build -buildvcs=false -o warp-bench .
export BENCH_COOKIE_LOGGED_IN='wordpress_logged_in_256cd14049b13d565fe9ae359b7e7b34=andrejlen%7C17917465a5'
./warp-bench -config config.json -plan
./warp-bench -config config.json
```

Before running, edit these exact configuration fields:

1. `targets`: your WordPress, Warp and ASR origins. You can have one target or any number. Each must serve the same paths/content.
2. `sitemap.url`: the actual product sitemap or sitemap index. Alternatively, set `sitemap.paths_file` to a newline-delimited list of `/product/.../` paths; this takes precedence over the URL.
3. `country_header`: replace `X-Bench-Country` with the header your installation recognises. This is a placeholder, not a claim about Warp's header name.
4. `session_cookie_env.logged_in`: the environment variable holding the current login cookie header. The example requires `BENCH_COOKIE_LOGGED_IN`.
5. The two AJAX cases' `path`: replace `REPLACE_WITH_YOUR_ACTION` with your real cached endpoint, or remove those cases until ready.
6. `publish.url`: your existing collector endpoint, or leave blank for local results. When enabled, export `BENCH_TOKEN` (or change `publish.token_env`).
7. `wrk`: set your desired profiles. Install `wrk` in PATH, or use `"wrk": []` to run only the Go sequential tests.

`-plan` fetches the sitemap and writes exact workload plans without executing benchmarks. If publishing is configured, configuration validation still requires its token. All configuration paths, including `paths_file`, are relative to your current working directory.

## What runs

The example contains:

* 1,000 product requests, varying desktop/mobile/tablet, NL/GB/US, logged in/out, and absent/EUR/GBP/USD currency cookies.
* 100 sequential AJAX requests logged out, and 100 logged in.
* 100 sequential homepage requests.
* Three homepage `wrk` profiles, with 20, 100 and 250 connections.

Every Go request finishes reading and closing its response body before the next starts. Every case and `wrk` process also finishes before the next begins. `wrk` intentionally creates concurrency within its own load tests.

Product paths are deduplicated, sorted, then shuffled with `seed`. All matching products are visited before a path repeats. Visitor states rotate between passes; a full path/state matrix visits every combination. A 1,000-request run is a reproducible subset when the matrix is larger than 1,000. Increase `requests` to cover the entire matrix. With 200 products, 3 devices, 3 countries, 2 sessions and 4 currency states, full coverage requires 14,400 requests.

Plans are built once and reused on every target. `plan_hash` hashes paths and state labels, including campaign parameters. It identifies the workload, not the server configuration or cookie credential values.

To reproduce paths across runs despite sitemap changes, use the saved `paths.txt` as `sitemap.paths_file`. Keep the same seed, cases, state lists and request counts.

## Cookies and login

Cookies are inputs, not an automated WordPress login/logout flow.

* `common_cookie_env`: optional environment variable containing cookies common to every scenario. Keep authentication and currency cookies out of this header.
* `session_cookie_env.logged_in`: variable holding your logged-in Cookie header.
* `session_cookie_env.logged_out`: optional variable holding a separate anonymous/logged-out Cookie header. Omit it for no session cookies.
* Currency is set separately. Existing copies of `currency_cookie` are removed; an empty currency means the cookie is absent.

The runner builds a fresh Cookie header for each request. It does not use a cookie jar and does not adopt server `Set-Cookie` responses, so a response cannot alter the next request's scenario. Cookies with the same name are overridden by the session header, then by the currency setting.

Without a configured login environment variable, mixed cases use only logged-out state. A fixed logged-in case requires one. Configured session variables must be populated. The tool cannot infer whether WordPress accepted a login cookie: check that it is current and valid on each target. Targets with different WordPress cookie hashes/users need separate runs with their appropriate credentials and the same saved paths/configuration/seed.

Cookie and bearer values are not written into plans or result JSON. Local files/directories use private permissions. The private temporary Lua script needed by `wrk` contains request headers and is deleted afterwards. A forced OS kill can leave that script behind.

## Browser profiles and request headers

Device comes from the User-Agent. The default desktop is Chrome on Linux, mobile is Chrome on Android with `Mobile`, tablet is Chrome on Android without `Mobile`. Default client hints match those profiles. These are fixed sample Chrome 131 strings, not a live claim about the latest browser release.

Override a profile through `user_agent_env`, for example:

```bash
export BENCH_UA_MOBILE='YOUR_FULL_MOBILE_CHROME_USER_AGENT'
```

When overriding the UA, default client hints are omitted to avoid version mismatches. You can explicitly provide matching hints through configurable headers. Do not use `headers.User-Agent`; the device profiles own this header.

Navigation defaults include Accept, gzip, Sec-Fetch headers, Upgrade-Insecure-Requests and client hints. No language variation is generated. Optional headers belong in global `headers` or case `headers`. An empty value removes a default header. Headers such as Referer, Origin, Priority or application-specific headers are configurable rather than fabricated when there is no corresponding browser interaction.

Host, framing and connection handling are managed by the HTTP client. This is an HTTP benchmark with browser-style headers; Go's TLS/HTTP implementation is not Chrome's browser fingerprint, and no JavaScript or assets are loaded.

The country header changes the application's supplied country input. It does not change the actual source IP or geographical origin. Point it at the existing trusted input your system uses; an edge that overwrites that header cannot be overridden by this client.

## Campaign query variation

Global `campaign` adds reproducible random query values:

```json
{
  "enabled": true,
  "probability": 0.5,
  "parameters": ["gclid", "utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"]
}
```

Half the requests receive campaign parameters with this configuration. Existing query parameters are preserved and existing values with the same keys are not replaced. Values are synthetic benchmark identifiers. Supported keys also include `dclid`, `gbraid`, `wbraid`, `gad_source` and `gad_campaignid`.

Use `"campaign_enabled": false` on a case to disable this variation. The example disables it for the throughput homepage case so its baseline keeps a stable URL. It remains enabled for mixed products and AJAX cases.

Campaign parameters do not necessarily bypass caching: that is precisely what you are measuring. For sequential tests each generated campaign URL is fixed in the saved plan. `wrk` cycles through the same finite campaign URL pool separately in each worker, rather than inventing unbounded new values during timed request generation.

## AJAX, JSON and POST

Endpoint cases support any HTTP method, a literal `body`, case headers and a fixed or mixed state. Example POST configuration:

```json
{
  "name": "AJAX POST logged out",
  "kind": "endpoint",
  "path": "/wp-admin/admin-ajax.php",
  "method": "POST",
  "body": "action=your_cached_action",
  "requests": 100,
  "device": "desktop",
  "country": "NL",
  "session": "logged_out",
  "currency": "EUR",
  "headers": {
    "Content-Type": "application/x-www-form-urlencoded",
    "Accept": "*/*",
    "Sec-Fetch-Dest": "empty",
    "Sec-Fetch-Mode": "cors",
    "Sec-Fetch-Site": "same-origin",
    "Sec-Fetch-User": "",
    "Upgrade-Insecure-Requests": "",
    "X-Requested-With": "XMLHttpRequest"
  }
}
```

For REST/JSON, use your endpoint path and `Accept: application/json`. The runner does not interpret `X-Warp-Cache-Control`: it records that response header and lets your existing Warp caching policy operate.

Sequential requests expect 2xx by default. Set `expected_statuses` explicitly to change that. Redirects are recorded, not followed, so a login redirect cannot silently become a successful page request. Expired authentication returning a 200 login page still needs application-level verification. `wrk` reports socket errors and non-2xx/3xx counts; it does not apply the sequential custom status allowlist.

## Results and publishing

Each suite gets a directory in `results/`, containing plans, sitemap paths and `result-ID.json` files. Each result records suite/target/case, seed, plan hash, machine, optional revision, timings, status counts, response body bytes, error counts and per-request state/results. Sequential response metadata includes both Warp cache-policy header names, Cache-Control, X-Warp-Template, Age and Content-Encoding when present.

Sequential latency includes the complete body read. `response_headers_ms` is time until headers are available, not a TCP-level first-byte metric. Percentiles use nearest rank and include only successful requests. Requests/sec uses successful requests divided by total elapsed test time. Bytes count response body bytes; gzip remains compressed, consistently with the requested encoding. HTTP framing/TLS bytes are not included in that Go body-byte count.

`wrk` results preserve total requests, elapsed time, throughput, transfer rate, latency distribution, thread stats and errors, with units normalised to milliseconds and bytes. Raw `wrk` output is not saved because subprocess error text can expose secrets.

Publishing happens outside each timed test:

```http
POST YOUR_ENDPOINT
Authorization: Bearer YOUR_SHARED_TOKEN
Content-Type: application/json
Idempotency-Key: RESULT_ID
```

The body is one complete result object with `schema_version: 1` and unique `id`. Your endpoint should persist it and return 2xx; deduplicate by `id` because retries/replays can deliver the same result more than once. It can then update your existing dashboard however you choose. No database, hosting platform or dashboard stack is imposed by this project.

Each result is saved before uploading. Transient transport failures, HTTP 429 and server errors retry; other non-2xx statuses stop that upload. Redirects are not followed. Failures remain saved, later tests continue, and the process exits nonzero if requests, wrk or delivery failed.

Replay a specific suite's unsent files:

```bash
./warp-bench -config config.json -out results/YOUR_SUITE_ID -replay
```

Successful uploads get a `.sent` marker and are skipped on replay. Replay reads the specified directory, not recursively. Ctrl+C cancels the active request/process and retains completed results plus a partial sequential result when possible.

## Repeatable comparisons

`cache_mode` is a descriptive label only. The runner does not purge, warm or bypass caches automatically. Choose your cache conditions before starting; use labels such as `cold`, `warm` or `bypass` after arranging those conditions. A mixed workload can revisit the same product under different states, as intended.

Set `BENCH_REVISION` to the tested Warp revision if wanted. It is explicit because the runner's own repository may not be the Warp repository.

```bash
export BENCH_REVISION='YOUR_WARP_COMMIT'
go test -race ./...
go vet ./...
```

For same-machine comparisons, use consistent settings and repeat runs. Timed Go sequential tests include their lightweight request/measurement overhead. `wrk` is itself a load generator and consumes resources during throughput tests. Publishing and sitemap discovery are outside the measured test intervals.

## Code roadmap

* `main.go`: suite order, cancellation, saved plans and result delivery.
* `config.go`: configuration schema, validation and shared HTTP client.
* `sitemap.go`: XML/index/gzip discovery and reusable paths files.
* `scenario.go`: device profiles, state matrix, request headers and cookies.
* `campaign.go`: reproducible Google campaign query generation.
* `runner.go`: complete-response sequential loop and measurements.
* `wrk.go`: private Lua script, subprocess execution and unit-aware output parser.
* `publish.go`: atomic local JSON, bearer POST, retry and replay.
* `bench_test.go`: behavioural and integration checks.

All functions are supplied in full, with no missing scaffolding or external Go dependencies.
