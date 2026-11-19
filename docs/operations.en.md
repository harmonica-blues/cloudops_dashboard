# CloudOps — Configuration and operations guide

[Project overview](../README.en.md) · [日本語](operations.ja.md) | **English**

Run the commands in this guide from the repository root.

CloudOps is a Docker dashboard for monitoring WAF, EC2, and RDS and querying WAF logs with Athena. This project is self-contained: the Go code and frontend are built into the image.

> **Deployment environments**
>
> - **EC2 behind an ALB:** Copy the project, set `CLOUDOPS_TRUSTED_PROXIES` in `.env` to your VPC CIDR, and run `docker compose up -d --build`. See [EC2 + ALB deployment (HTTPS)](#ec2--alb-deployment-https). Do not copy `.env.local` to EC2.
> - **Local testing:** Use `.env.local` to serve on `127.0.0.1:8082` and mount the AWS profiles from your computer's `~/.aws`. Local data uses a separate database volume.
>
>   ```powershell
>   docker compose --env-file .env.local up -d --build
>   docker compose --env-file .env.local exec cloudops /app/cloudops user add <username>
>   docker compose --env-file .env.local down
>   ```

## Prepare configuration files

For the first run, copy `config.example.json` to `config.json`. For EC2, copy `.env.example` to `.env`; for local testing, copy `.env.local.example` to `.env.local`. Edit the copies for your environment. Do not overwrite existing configuration files.

The local example targets Linux / macOS. On Windows, change the `COMPOSE_FILE` separator to `;` and remove the `AWS_CONFIG_DIR` line. The local example sets `COMPOSE_PROJECT_NAME=cloudops-local`, keeping its data volume separate from the EC2 deployment.

Real environment files (`.env`, `.env.local`, `config.json`), databases, backups, personal notes, and Docker image archives are excluded from Git and the Docker build context. Never put real credentials or environment-specific values into published examples.

## Fictional data for screenshots

The `screenshot` mode displays fictional WAF, EC2, and RDS charts, period comparisons, sampled requests, query results, and estimated spending. The interface identifies it as `撮影モード（架空）` (screenshot mode, fictional). It makes no AWS connections, disables query execution and CSV downloads, and does not persist fictional query history. These values are never used as fallback data in `aws` mode.

Start it with a dedicated port and volume, without loading your regular `.env` (Linux / macOS):

```bash
docker compose --env-file /dev/null -f docker-compose.screenshot.yml up -d --build
docker compose --env-file /dev/null -f docker-compose.screenshot.yml exec cloudops /app/cloudops user add demo
```

On Windows, replace `/dev/null` with an empty environment file. Open [http://127.0.0.1:8083](http://127.0.0.1:8083) and sign in with the password printed by the command. You can capture Overview, WAF, EC2, RDS, and Queries in light or dark theme, including custom date ranges. Screenshot data does not read real environment settings.

```bash
# Stop only the screenshot environment; its volume retains login information.
docker compose --env-file /dev/null -f docker-compose.screenshot.yml down
```

## Dashboard features

- Overview, WAF, EC2, RDS, and Queries views.
- AWS account identity, EC2 states, CloudWatch metrics, RDS, and WAF data.
- Preset ranges of one day, seven days, and 30 days. Custom ranges accept start and end timestamps, up to 90 days.
- The current page and applied range are stored in URL parameters, for example `/?page=waf&period=7`. Custom ranges include UTC start and end timestamps at minute precision, such as `/?page=waf&period=custom&start=2026-11-01T00:00:00Z&end=2026-11-02T00:00:00Z`, and restore the picker in JST. State survives reloads, sharing, browser back/forward, and login. Presets remain relative to the time the link is opened; custom timestamps remain fixed. Unapplied inputs and SQL are not stored in the URL. Unknown pages fall back to Overview; invalid ranges (missing timestamps, reversed dates, future dates, or more than 90 days) fall back to the last day. Shared links still require login.
- WAF, EC2, and RDS time series charts, with averages and peaks. RDS free storage shows the latest value. Charts fill their cards and display timestamped tooltips on hover.
- Main WAF counts and EC2 / RDS averages include comparisons with the preceding period of equal length. Increases of at least 50% are yellow; at least 100% are red. Previous-period metrics for presets are fetched at most once per hour to limit additional CloudWatch requests.
- Unavailable metrics appear as `データなし` (no data); real metrics are never fabricated.
- In-memory caching with periodic collection at `collection_interval`. Collection pauses after `idle_pause_after` without dashboard access. Only the three preset ranges are collected periodically; adding presets increases CloudWatch requests and cost.
- The page rereads the cache every 60 seconds, excluding custom ranges.
- On initial display, navigation, range changes, and manual refresh, lines and mini bar charts reveal from left to right and rings fill to their values over approximately 1.1 seconds. Automatic refreshes and resizing do not restart the animation. The OS / browser reduced-motion preference disables it. Animation does not delay data fetching.
- `aws` mode uses real data; `demo` shows an empty dashboard; `screenshot` supplies fictional data for captures.
- The WAF view shows rule-level blocked counts from CloudWatch for the selected period, plus sampled requests from the last three hours through `GetSampledRequests`. It does not use Athena. See [WAF sampled requests and rule-level counts](#waf-sampled-requests-and-rule-level-counts).
- WAF log queries run manually through Athena. SQL templates in `queries/` are available in the query selector.
- Login is required. Users, audit logs, query history (including the author), and daily Athena usage are stored in SQLite. See [Login and user administration](#login-and-user-administration).

## Main files

```text
.
├── Dockerfile / docker-compose.yml   Container build and execution
├── .env                             Compose environment variables; excluded from Git and image
├── config.example.json              Template to copy to config.json
├── go.mod / go.sum                   Go module definition and checksums
├── app/                             Go code and frontend (package main)
├── queries/                         Saved SQL files, mounted read-only
├── deploy/                          Example IAM policy and EC2 user data
└── notes/                           Personal notes; not part of the application
```

Inside `app/`:

- `main.go` and `awscollector.go`: HTTP API, cache, and AWS collection.
- `comparison.go`: previous-period comparisons.
- `athena.go` and `savedqueries.go`: Athena execution and loading saved queries.
- `wafdetails.go`: WAF sampled requests and blocked counts by rule.
- `budget.go`: Athena daily spending limits and partition range validation.
- `auth.go` and `templates/login.html`: login, sessions, and auditing.
- `store.go`: SQLite storage for users, sessions, audit logs, query history, daily usage, and the spending lock.
- `users.go`: administrative commands for users and releasing the spending lock.
- `web/`: frontend files embedded into the binary with `go:embed`; they live under the Go package for that reason.
- `*_test.go`: tests.

`deploy/iam-policy.example.json` contains example permissions for infrastructure reads and Athena. `deploy/user-data.sh` sets up EC2.

## Host environment variables

Put these in `.env` at the repository root, or set them in your shell before running Compose.

| Variable | Required | Used by | Purpose |
| --- | --- | --- | --- |
| `AWS_PROFILE` | Local AWS access | `docker-compose.local.yml` | Profile configured in the host's `.aws` directory. Do not set it for EC2 IAM role authentication. |
| `AWS_REGION` | In `aws` mode | Compose and application | Main EC2 / RDS / CloudWatch region; overrides JSON `region`. |
| `CLOUDOPS_CONFIG_FILE` | Recommended | Compose | Host path to the JSON configuration; defaults to `config.example.json`. |
| `CLOUDOPS_MODE` | Optional | Compose | Defaults to `aws`. Use `demo` for an empty dashboard without AWS. |
| `COMPOSE_FILE` | Local AWS access | Docker Compose | `docker-compose.yml;docker-compose.local.yml` on Windows, or use `:` on Linux / macOS. Loads the local AWS credential mount. Do not set it on EC2. |
| `USERPROFILE` | Automatically set on Windows | Local Compose override | Locates the host `.aws` directory. |
| `AWS_CONFIG_DIR` | Local Linux / other systems | Local Compose override | Path to the host `.aws` directory. Defaults to the `.aws` directory under `USERPROFILE`. |
| `CLOUDOPS_TRUSTED_PROXIES` | Behind an ALB or other proxy | Compose and application | Comma-separated trusted source CIDRs, such as the VPC CIDR. Only requests from these sources may supply `X-Forwarded-For` for audit IPs and `X-Forwarded-Proto` for secure cookies. Overrides JSON `trusted_proxies`. |
| `CLOUDOPS_BIND_ADDRESS` | Optional | Compose | Host bind address; defaults to `127.0.0.1` (local access only). See [VPN access](#vpn-access). |
| `CLOUDOPS_PORT` | Optional | Compose | Published host port; defaults to `8080`. |

Example `.env` for local AWS access on Windows:

```dotenv
AWS_PROFILE=your-profile
AWS_REGION=ap-northeast-1
CLOUDOPS_CONFIG_FILE=./config.json
COMPOSE_FILE=docker-compose.yml;docker-compose.local.yml
```

For EC2, use the example in [EC2 + ALB deployment](#ec2--alb-deployment-https). Variables set in the shell take precedence over `.env`.

Compose already sets the internal variables `CLOUDOPS_CONFIG`, `CLOUDOPS_ADDRESS`, and `CLOUDOPS_DB`. The last overrides `database.path`. The local override also sets `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, and `HOME` inside the container.

## JSON configuration

Copy [config.example.json](../config.example.json) to `config.json` and configure your environment:

```json
{
  "mode": "aws",
  "address": "127.0.0.1:8080",
  "region": "your-region",
  "timezone": "UTC",
  "collection_interval": "5m",
  "idle_pause_after": "30m",
  "waf": {
    "web_acl_id": "web-acl-id",
    "web_acl_name": "web-acl-name",
    "region": "us-east-1"
  },
  "ec2": {
    "instance_ids": ["i-xxxxxxxxxxxxxxxxx"],
    "memory_namespace": "CWAgent",
    "memory_metric": "mem_used_percent"
  },
  "rds": { "instance_ids": ["rds-identifier"] },
  "athena": {
    "region": "your-region",
    "database": "",
    "catalog": "AwsDataCatalog",
    "workgroup": "",
    "results_location": "",
    "table": "",
    "queries_dir": "queries",
    "daily_budget_usd": 1.0,
    "max_query_days": 31
  },
  "database": { "path": "data/cloudops.db" },
  "auth": { "session_ttl": "12h", "cookie_secure": false },
  "trusted_proxies": []
}
```

`database.path` points to SQLite. In Docker, `CLOUDOPS_DB=/data/cloudops.db` overrides it, so you do not need to change the path. `auth.session_ttl` sets the login lifetime (default `12h`).

Set `auth.cookie_secure` to `true` only when serving over HTTPS; enabling it on HTTP prevents login. When an ALB terminates HTTPS, you can leave it `false` if `trusted_proxies` (or `CLOUDOPS_TRUSTED_PROXIES`) includes the ALB's source CIDRs: `X-Forwarded-Proto: https` then enables Secure automatically. Only requests from trusted CIDRs may use the last `X-Forwarded-For` value as the user's audit IP. Without trusted proxies, the headers are ignored and the connection's source IP is recorded.

`web_acl_id` and `web_acl_name` must identify the same Web ACL. CloudFront WAF metrics use `us-east-1`. EC2 instance IDs and RDS identifiers must exist in the main `region`.

`timezone` controls display timestamps, for example `Asia/Tokyo`. Backend collection, caching, comparisons, and Athena `log_time` partitions always use UTC. Chart axes, tooltips, the footer range, and the `{{timezone}}` saved-query placeholder use this display setting. Apply configuration changes with `docker compose restart`.

Custom range inputs always use JST, regardless of the computer's timezone. Ranges cannot exceed 90 days or end in the future.

`idle_pause_after` pauses periodic CloudWatch collection after inactivity (default `30m`; `"0"` disables it). On the next visit, the requested preset is collected before displaying it, which can take a few seconds, and periodic collection resumes. Automatic refresh stops while a tab is in the background, so hidden tabs do not count as active visits. `/api/health` exposes `collection_paused`.

EC2 memory is not a standard AWS metric. Configure `ec2.memory_namespace` and `ec2.memory_metric` for the metric your OS sends to CloudWatch. The expected dimension is `InstanceId` alone. Omitted values use CloudWatch Agent defaults.

| Publisher | `memory_namespace` | `memory_metric` |
| --- | --- | --- |
| CloudWatch Agent (default) | `CWAgent` | `mem_used_percent` |
| Legacy CloudWatch Monitoring Scripts | `System/Linux` | `MemoryUtilization` |

You can inspect available metrics with:

```bash
aws cloudwatch list-metrics --dimensions Name=InstanceId,Value=<id> --query "Metrics[?Namespace!='AWS/EC2']"
```

Athena fields can remain empty until Glue, the workgroup, and the S3 results bucket are ready.

### WAF sampled requests and rule-level counts

The bottom of the WAF view shows two datasets without using Athena:

| Dataset | Contents | Source and collection |
| --- | --- | --- |
| Blocked requests by rule | Top 10 rules for the selected period | CloudWatch `BlockedRequests` with the `Rule` dimension. Each rule adds metric reads; preset results are cached for 15 minutes (the Japanese guide estimates under USD 1 per month for 10 rules). |
| Sampled requests, last three hours | Latest 200 samples: timestamp, action, client IP, country, method, URI, rule | WAF `GetSampledRequests` (free API), cached for five minutes. |

These cost figures are indicative; actual charges depend on AWS pricing and usage.

- These are AWS WAF samples, up to 100 per rule, not a complete request log. They always cover the last three hours, regardless of the selected period. Use Athena for full-log investigations.
- The IAM role needs `wafv2:GetSampledRequests`, and each rule must enable `SampledRequestsEnabled` (enabled by default for rules created through the console).
- Rules are discovered through CloudWatch `ListMetrics` and cached for one hour; `wafv2:GetWebACL` is not required.
- Set `waf.scope` to `CLOUDFRONT` (default) for CloudFront or `REGIONAL` for ALB / API Gateway. The Web ACL ARN is built from `web_acl_name` and `web_acl_id`; provide the Web ACL name used for its metrics.

### Athena queries

Use the Queries view to run Athena SQL against WAF logs manually. Queries run only after clicking Execute; periodic collection does not use Athena.

- Set `athena.database` and either `athena.workgroup` or `athena.results_location`.
- Set `athena.table` to the WAF log table name; it replaces `{{table}}` in saved SQL.
- Saved queries are `.sql` files in `queries/`. Selecting one loads it into the editor, where it can be edited before execution. See [Writing saved queries](../queries/README.en.md).
- Only read-only `SELECT` / `WITH` statements are accepted. The UI displays at most 1,000 result rows.
- `CSVダウンロード` (Download CSV) retrieves the full result file stored by Athena in S3 (`results_location`). The browser downloads directly from S3 through a presigned URL valid for five minutes. The result bucket must be in the same region as Athena and requires `s3:GetObject` permission.
- History shows the author, bytes scanned, and estimated cost, calculated at about USD 5 per TB with a 10 MB minimum per query. The latest 20 entries are displayed; all query history is stored in the database. After restart, successful query results are fetched again from Athena without rerunning the query.
- Everyone can see query history. For sensitive SQL, select `結果を共有しない` (Do not share results). Other users see history metadata such as author, name, status, and scan volume, while only the author can access SQL, results, errors, and CSV. SQL is still stored in the database. Anyone can cancel a running query.
- Configure the workgroup's per-query data usage limit to help prevent unexpectedly expensive scans.

#### Usage limits

The application checks these before starting a query:

| Limit | Behavior | Setting |
| --- | --- | --- |
| Daily spending | Blocks new queries when today's estimated usage, using the configured `timezone`, reaches the limit. **Changing the date or recreating the container does not release the lock.** The lock persists in SQLite. | `athena.daily_budget_usd`, default USD `1.0` |
| Partition range | Rejects SQL without `log_time BETWEEN 'yyyy/MM/dd/HH/mm' AND 'yyyy/MM/dd/HH/mm'`, or with a range exceeding the limit. Conditions in comments do not count. | `athena.max_query_days`, default `31` days |
| Concurrency | Rejects new queries while any user's query is running. | One query at a time |

- Today's spending is calculated from the workgroup's `ListQueryExecutions` / `BatchGetQueryExecution` history. This includes queries run through the AWS console or other tools in the same workgroup and survives container restarts. Use a dedicated workgroup for CloudOps.
- If usage cannot be checked, for example because of missing permissions, queries are blocked.
- Checked usage is recorded by day in `athena_daily_usage`. It reflects the last check that day, so queries executed just before midnight may not be included.
- The Queries view shows today's spending and its limit. Edited templates and manually entered SQL require confirmation before execution.
- Costs are scan-based estimates and may differ from bills due to rounding and other factors. A running query's cost is added after completion, so spending can exceed the limit by one query. Use the workgroup's per-query data usage limit to bound that scan.

**Releasing a spending lock (administrator):** Use either method below. If today's usage still exceeds the limit, the next check locks execution again.

1. Increase `athena.daily_budget_usd` in `config.json` above the limit recorded when the lock was created, then recreate the container. Startup releases the lock.

   ```powershell
   docker compose up -d --force-recreate
   ```

2. To release it without increasing the limit, for example after the date changes, run the following. No restart is needed.

   ```powershell
   docker compose exec cloudops /app/cloudops budget unlock
   ```

### Find configuration values

| Setting | AWS console | Read-only CLI example |
| --- | --- | --- |
| `region` | EC2 → Instances; check the URL or region selector | `aws ec2 describe-instances --instance-ids <id> --query "Reservations[].Instances[].Placement.AvailabilityZone"` |
| `waf.web_acl_id` / `web_acl_name` | WAF & Shield → Web ACLs; select Global for CloudFront | `aws wafv2 list-web-acls --scope CLOUDFRONT --region us-east-1` |
| `athena.database` | Athena query editor or Glue → Databases | `aws glue get-databases --region <region>` |
| `athena.workgroup` | Athena → Workgroups; default `primary` | `aws athena list-work-groups --region <region>` |
| `athena.results_location` | Athena → Workgroups → Query result location | `aws athena get-work-group --work-group primary --region <region>` |

## First run with PowerShell

Run these from the repository root:

```powershell
# Only when not using .env
$env:AWS_PROFILE = "your-profile"
$env:AWS_REGION = "your-region"
$env:CLOUDOPS_CONFIG_FILE = "./config.json"

aws sts get-caller-identity

Copy-Item config.example.json config.json
# Edit config.json with your resource IDs.

docker compose up -d --build
```

Create the first login user (see [Login and user administration](#login-and-user-administration)):

```powershell
docker compose exec cloudops /app/cloudops user add <username>
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). By default, only `127.0.0.1:8080` is published on the host; the service listens on `0.0.0.0:8080` inside the container.

## Login and user administration

Dashboard access requires login, except for `/api/health`. There is a single user type, and all users have the same capabilities. Accounts cannot be created through the UI; an administrator runs commands inside the container.

| Action | Command |
| --- | --- |
| Create a user | `docker compose exec cloudops /app/cloudops user add <username>` |
| Reset a password | `docker compose exec cloudops /app/cloudops user passwd <username>` |
| Disable / enable a user | `docker compose exec cloudops /app/cloudops user disable <username>`; replace `disable` with `enable` to restore access |
| List users | `docker compose exec cloudops /app/cloudops user list` |
| Release the Athena lock | `docker compose exec cloudops /app/cloudops budget unlock` |

- `add` and `passwd` generate a random password and display it once. Deliver it securely. To supply a password through standard input, use `-T` and `--password-stdin` (at least 12 characters): `"<password>" | docker compose exec -T cloudops /app/cloudops user add <username> --password-stdin`.
- After login, users can change their own passwords through `パスワード変更` (Change password) in the header. This requires the current password and at least 12 characters for the new one. Other sessions are revoked. Incorrect current passwords count as login failures.
- Administrative password resets and account disabling revoke the user's active sessions.
- Usernames allow ASCII letters, digits, `.`, `_`, `@`, and `-`; they are case-insensitive.
- Five consecutive failed logins lock an account for 15 minutes.
- Passwords are hashed with PBKDF2-SHA256. Session cookies use `HttpOnly` and `SameSite=Strict`. Only token hashes are stored in SQLite.

### Database

SQLite at `/data/cloudops.db` stores users, sessions, audit logs, query history, daily spending, and the Athena lock. `/data` is the `cloudops-data` Docker volume, which survives container recreation. **`docker compose down -v` deletes the volume and its data.** This volume is the container's only writable application storage.

| Table | Contents |
| --- | --- |
| `users` | Username, password hash, disabled state, login failures, and lock state |
| `sessions` | Token hashes and expiry timestamps |
| `audit_log` | Events including `login`, `login_failed`, `logout`, `password_change`, `password_change_failed`, `query_start`, `query_cancel`, `csv_download`, `dashboard_refresh`, `budget_lock`, `budget_unlock`, and `user_add` |
| `athena_queries` | Every executed query: author, SQL, private flag, state, scanned bytes, and estimated cost. Result rows are not stored. |
| `athena_daily_usage` | Daily estimated Athena spending in USD |
| `budget_lock` | Persistent lock triggered by the spending limit |

For a backup or inspection, stop the container, copy the database directory, and then open the copy with a host SQLite tool:

```powershell
docker compose stop
docker compose cp cloudops:/data ./backup
docker compose start
```

## VPN access

Login does not encrypt HTTP traffic: passwords and cookies travel unencrypted. **Restrict HTTP access to the VPN.** For HTTPS, use [EC2 + ALB deployment](#ec2--alb-deployment-https).

### Publish from your computer on its VPN IP

1. Use `ipconfig` on Windows to find the VPN adapter's IPv4 address and network range.
2. Set `CLOUDOPS_BIND_ADDRESS` to that IP in `.env` and recreate the container:

   ```dotenv
   CLOUDOPS_BIND_ADDRESS=10.x.x.x
   ```

   ```powershell
   docker compose up -d
   ```

3. Allow port 8080 only from the VPN network in Windows Firewall (administrator PowerShell):

   ```powershell
   New-NetFirewallRule -DisplayName "CloudOps dashboard" -Direction Inbound -Protocol TCP -LocalPort 8080 -RemoteAddress <vpn-cidr> -Action Allow
   ```

   Replace `<vpn-cidr>` with your VPN network, for example `10.8.0.0/16`.
4. VPN users can visit `http://<vpn-ip>:8080/`.

If the VPN IP changes, update `.env` and rerun `docker compose up -d`. Startup fails if the configured IP is unavailable, for example when the VPN is disconnected.

To stop sharing, restore `CLOUDOPS_BIND_ADDRESS=127.0.0.1`, recreate the container, and remove the firewall rule with `Remove-NetFirewallRule -DisplayName "CloudOps dashboard"`.

### Run on a dedicated machine

- If the host IP can change, use `CLOUDOPS_BIND_ADDRESS=0.0.0.0` and **block all non-VPN sources with the firewall**.
- On Linux, Docker manages iptables directly, so published ports may bypass `ufw` rules. Bind to the VPN interface's IP or restrict traffic through the `DOCKER-USER` chain.
- On EC2, use the instance's IAM role without mounting `.aws`. For local Linux credentials, set `AWS_CONFIG_DIR` to the host directory and load `docker-compose.local.yml` through `COMPOSE_FILE`. Files must be readable by container UID 65532 (`nonroot`).
- For long-running local setups, a dedicated IAM user with only the [example policy](../deploy/iam-policy.example.json) permissions can avoid repeated SSO sign-ins. EC2 deployment uses an IAM role.

Run the empty dashboard without AWS:

```powershell
$env:CLOUDOPS_MODE = "demo"
docker compose up -d --build
```

Stop it:

```powershell
docker compose down
```

After changing code or frontend files:

```powershell
docker compose build --no-cache
docker compose up -d --force-recreate
```

## EC2 + ALB deployment (HTTPS)

Terminate HTTPS at the ALB and forward HTTP from the ALB to the EC2 container:

```text
Browser ──HTTPS 443──▶ ALB (certificate *.example.jp; HTTP 80 redirects to 443)
                       │ HTTP 8080 with X-Forwarded-For / X-Forwarded-Proto
                       ▼
                      EC2 (Docker, CloudOps container, IAM role) ──▶ AWS APIs
```

1. **IAM role:** Create a role with permissions based on [deploy/iam-policy.example.json](../deploy/iam-policy.example.json), then attach it to EC2 through an instance profile. Do not use access keys.
2. **IMDSv2 hop limit:** Set the response hop limit to `2`, because container requests cross Docker networking to retrieve temporary IAM role credentials. The default of 1 is insufficient. At launch, use Advanced details → Metadata response hop limit `2` and Metadata version V2 only (token required). For an existing instance:

   ```bash
   aws ec2 modify-instance-metadata-options --instance-id <instance-id> \
     --http-endpoint enabled --http-tokens required --http-put-response-hop-limit 2
   ```

3. **Deploy:** Install Docker and the Docker Compose plugin, copy the project, and create `config.json`. Use the following `.env` without `AWS_PROFILE` or `COMPOSE_FILE`. For a 2 GB instance such as t3.small, adding 1–2 GB of swap can help avoid build-time memory exhaustion.

   ```dotenv
   AWS_REGION=ap-northeast-1
   CLOUDOPS_CONFIG_FILE=./config.json
   # Listen on all interfaces for ALB traffic; restrict the security group to the ALB.
   CLOUDOPS_BIND_ADDRESS=0.0.0.0
   CLOUDOPS_PORT=8080
   # ALB IPs change; use the VPC CIDR containing the ALB subnets.
   CLOUDOPS_TRUSTED_PROXIES=10.0.0.0/16
   ```

   ```bash
   docker compose up -d --build
   docker compose exec cloudops /app/cloudops user add <username>
   ```

4. **Target group:** Use instance targets, HTTP, and port `8080`. Register EC2 and use `/api/health` for health checks (no login, HTTP 200). Sessions are persisted in the database, so sticky sessions are not required for this deployment.
5. **ALB:** Add a certificate to the HTTPS 443 listener and forward to the target group. Redirect HTTP 80 to HTTPS. Use an internal ALB for VPN-only access. For an internet-facing ALB, restrict source networks with security groups.
6. **Security groups:**

   | Resource | Inbound | Source |
   | --- | --- | --- |
   | ALB | 443, 80 | User networks: VPN or corporate CIDRs |
   | EC2 | 8080 | ALB security group only |

   Do not expose EC2 port 8080 to the internet; connections bypassing the ALB are not HTTPS.
7. **DNS:** Point a Route 53 alias A record at the ALB. With another DNS provider, use a CNAME to the ALB DNS name.
8. **Verify:** Open `https://<domain>/` and confirm the login page appears. After login, check that `audit_log.ip` contains the user's IP rather than the ALB IP; this verifies the trusted proxy configuration.

### Deploy updates using an image built locally

Compiling the AWS SDK and SQLite on a 2 GB instance can be slow, even with swap. Build the image locally and transfer it as a tar archive to avoid rebuilding on EC2. Users, audit logs, query history, and spending remain in the `cloudops-data` volume when the container is recreated.

1. **Test locally** from the project root, with Docker running:

   ```powershell
   docker run --rm -v "${PWD}:/src" -w /src golang:1.27.4-alpine go test ./...
   ```

2. **Build and save.** Use `linux/arm64` instead for an ARM instance such as t4g:

   ```powershell
   docker build --platform linux/amd64 -t ec2-dash-cloudops:latest .
   docker save ec2-dash-cloudops:latest -o ec2-dash-cloudops.tar
   ```

3. **Transfer to EC2.** Upload the archive to `~/` with SFTP or another transfer tool. If changed, also upload `docker-compose.yml`, `config.json`, and `queries/` into `~/ec2-dash/`. Source under `app/` is not needed for loading the built image. Do not upload `.env.local` or `docker-compose.local.yml`.
4. **Load the image on EC2:**

   ```bash
   docker load -i ~/ec2-dash-cloudops.tar
   ```

   If a matching image tag already exists, Docker may show:

   ```text
   The image ec2-dash-cloudops:latest already exists, renaming the old one with ID sha256:… to empty string
   Loaded image: ec2-dash-cloudops:latest
   ```

   `latest` now points to the new image; the previous image becomes untagged (`<none>`). The running container still uses the old image at this point.
5. **Recreate the container without `--build`.** That flag would rebuild on EC2 rather than use the transferred image. A changed image should show `Recreated`:

   ```bash
   cd ~/ec2-dash && docker compose up -d
   ```

6. **Verify** that the running container's image ID matches the new `latest`, not the old ID displayed during loading:

   ```bash
   docker images ec2-dash-cloudops
   docker inspect --format '{{.Image}}' $(docker compose ps -q cloudops)
   ```

   Then sign in at `https://<domain>/` and check the updated feature.
7. **Remove old, untagged images:**

   ```bash
   docker image prune -f
   ```

Configuration-only changes require no image transfer: use `docker compose restart`. Changes to `queries/*.sql` do not even require a restart.

## Credentials and permissions

On EC2, use the instance's IAM role as described above. For local use, configure a profile on the host with `aws configure --profile your-profile` or SSO, then load `docker-compose.local.yml` through `COMPOSE_FILE`. The host `.aws` directory is mounted read-only; access keys are never copied into the image.

For SSO, run `aws sso login --profile your-profile` on the host before starting the container. Read-only mounting prevents the container from refreshing the token itself.

Start with [deploy/iam-policy.example.json](../deploy/iam-policy.example.json) and narrow resources for your environment. It includes STS, EC2, RDS, and CloudWatch permissions. Athena additionally requires Athena, Glue, and S3 permissions. Spending checks need `athena:ListQueryExecutions` and `athena:BatchGetQueryExecution`; without them, queries are blocked.
