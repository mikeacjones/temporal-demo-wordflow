# Temporal Wordflow

A Temporal-themed word-crossword game where every move a player makes is a
Workflow Update, and an AI agent loop writes a new daily challenge every night.

- Each level is a [Workflow](https://docs.temporal.io/workflows) started with
  [Update-With-Start](https://docs.temporal.io/develop/go/message-passing#update-with-start);
  guesses and hints are [Updates](https://docs.temporal.io/encyclopedia/workflow-message-passing#sending-updates),
  and a durable timer ends a timed level.
- [Schedules](https://docs.temporal.io/schedule) start the daily challenge and
  monthly pack generators, which run an OpenAI agent loop with each model turn
  and tool call as an Activity.
- Workers use [Worker Versioning](https://docs.temporal.io/worker-versioning)
  with Pinned Workflows, rolled out by the Temporal Worker Controller.
- Player accounts, scores and the campaign catalog live in DynamoDB.

## How it works

Workflows hold what is in motion: a level being played, a campaign's open
window, a generation run. DynamoDB holds what outlives them.

| Component | Owns | Lifetime |
| --- | --- | --- |
| `WordflowLevelWorkflow` | One player's attempt at one level: answers found, guesses, hints, timing and score | From the first move until it is solved, runs out of time, or sits untouched for 24 hours with no time limit |
| `WordflowCampaignWorkflow` | Publishing one campaign to the catalog, and taking a dated one down when it ends | A permanent campaign completes once published; a dated one at its end time |
| `WordflowCampaignGeneratorWorkflow` | Designing one daily challenge or monthly pack with an agent loop, then starting its campaign | One run per Schedule fire |
| DynamoDB `players` table | Each player's password hash, display name, points, streak, campaign progress and the one level they have open; its `leaderboard` index ranks lifetime points | Permanent |
| DynamoDB `campaigns` table | The snapshot of every published campaign | Until the campaign ends |
| HTTP API | Nothing durable; it signs session JWTs, reads the tables and sends Updates | Stateless |

Starting a level is one call: the API sends Update-With-Start to
`wordflow-level/{player}/{campaign}/{level}`. The `start-level` Update runs a
`ClaimLevel` Activity, which checks the catalog's unlock and eligibility rules
against the player's record and marks the level open there, then returns the
board. A refused claim comes back as the Update's error and the Workflow ends.
Guesses and hints are Updates on the same Workflow. A paid hint runs a
`SpendPoints` Activity. When the level ends, a `RecordLevelResult` Activity adds
the points, streak and progress to the player's record. That Activity is safe to
retry: a result for a level that is no longer open is ignored. HTTP request IDs
are the Update IDs, so Temporal deduplicates retried requests.

Player records are written with optimistic concurrency: each write is
conditional on the version it read, and a conflicting write reads again and
reapplies its change.

Two Temporal Schedules create new campaigns. The worker creates them at startup
if they do not exist and leaves existing ones untouched, so a pause or edit made
in the Temporal UI survives a restart.

| Schedule | Fires (America/Toronto) | Creates |
| --- | --- | --- |
| `wordflow-daily-challenge` | Every day at midnight | `daily-challenge-YYYY-MM-DD`: three hard levels, one attempt, 180-second limit, closes at the next midnight |
| `wordflow-monthly-pack` | Midnight on the 1st | `monthly-pack-YYYY-MM`: nine levels on one theme, no time limit, permanent |

Each fire starts `WordflowCampaignGeneratorWorkflow` with the campaign kind. It
runs an agent loop in which every model turn and every tool call is an Activity,
so the Temporal UI shows the whole conversation. The model builds the campaign
one level at a time in a draft that the Workflow keeps. It has four tools:

| Tool | Does |
| --- | --- |
| `suggest_anchors` | Lists common eight-letter anchor words that make good letter banks, putting those that share the fewest letters with saved levels first |
| `find_words` | Lists every common word a letter bank can spell, without answers from recent campaigns, and names the saved level that already uses a word |
| `set_level` | Checks one level and saves it to the draft if it passes: real common words, spellable from the bank, no repeats across levels, and a crossword that connects. It lists every problem at once |
| `submit_campaign` | Takes the description, checks the whole draft again, and ends the loop if it passes |

Words come from two lists embedded in the Worker (see
`internal/dictionary/data/README.md`). ENABLE decides whether a word is English;
answers must also be in SCOWL size 50, so every answer is a word players know.

The model must call a tool on every turn. Calls in one turn run one after
another, so a level is checked against levels saved earlier in the same turn. A
rejected level or submission goes back to the model as the tool's result, not as
a failure, and the model corrects it and tries again. The run fails only when the
model uses its turn budget (30 for a daily challenge, 40 for a monthly pack)
without a valid submission. The Workflow logs each rejected submission and its
reason. An accepted campaign starts as an abandoned child, and the Workflow
returns the recent answers (ten days of daily challenges, three monthly packs).
Temporal passes that result to the Schedule's next run, which rejects any answer
already used.
Both Schedules skip a run that overlaps a still-running one.

## Code tour

- [`internal/workflows/wordflow_level.go`](internal/workflows/wordflow_level.go):
  the level Workflow, from claim to recorded result.
- [`internal/workflows/wordflow_campaign.go`](internal/workflows/wordflow_campaign.go):
  publish a campaign, then take it down at its end time.
- [`internal/workflows/campaign_generator.go`](internal/workflows/campaign_generator.go):
  the generation agent loop.
- [`internal/activities/levels.go`](internal/activities/levels.go): the
  Activities that change a player's record.
- [`internal/player`](internal/player/player.go) and
  [`internal/catalog`](internal/catalog/catalog.go): the game rules for points,
  streaks, unlocks and eligibility, shared by the API and the Activities.
- [`internal/store`](internal/store/store.go): the DynamoDB tables.
- [`internal/bootstrap/temporal-foundations.json`](internal/bootstrap/temporal-foundations.json):
  the default campaign, published again each time the web app starts.

To add a campaign, build a `WordflowCampaignWorkflowInput` and start it as
`wordflow-campaign/{campaign-id}`; it publishes itself to the catalog.
The repository also includes a small manual campaign starter. It parses the
same compact level format as the permanent campaign, builds the crossword
layouts, and starts its Campaign Workflow:

```bash
docker compose --profile test run --rm -e TEMPORAL_ADDRESS=temporal:7233 test \
  go run ./cmd/campaign -file campaigns/daily-challenge-2026-09-21.json
```

The worker refuses to start without `OPENAI_API_KEY`, because it creates the
generation Schedules. `OPENAI_MODEL` is optional and defaults to `gpt-5.4-mini`.
Start a worker, then run a Schedule now instead of waiting for its time:

```bash
OPENAI_API_KEY=... make up
docker compose exec temporal temporal schedule trigger --schedule-id wordflow-daily-challenge
docker compose exec temporal temporal schedule trigger --schedule-id wordflow-monthly-pack
```

A trigger creates the campaign for the current period. A second trigger in the
same period fails when it tries to start the same campaign ID, after spending
one generation.

## Implemented game rules

- The default `Temporal Foundations` campaign never expires.
- Three levels unlock when a player joins that campaign, followed by three more
  every 24 hours. Missed levels remain available.
- Levels are sequential and guesses are unlimited.
- Campaign prerequisites are all-of requirements over completed campaigns and
  completed campaign levels.
- Leaving gameplay returns to the campaign browser without closing the level's
  Workflow. The player can resume it but cannot start another level until it
  ends. A level with no time limit that sees no guess or hint for 24 hours
  ends as abandoned and can be started again.
- An ended campaign rejects new level starts; a level started before the end may
  finish.
- A daily challenge is a dated, single-attempt campaign with three immediately
  unlocked but sequential levels. Its levels can configure a hard time limit,
  higher base score, and higher paid-hint prices. Timing out clears the active
  game and permanently closes that player's attempt for the dated campaign.
- Words that can be made from the letter queue but are not in the puzzle remain
  in durable, newest-first game history beside the board.
- Completing at least one level on a Toronto calendar day maintains the daily
  streak. A streak cannot be restored after a missed day.
- Every completed level awards 10 base points, plus up to 10 points each for
  speed, accuracy, and conserving hints. Incorrect guesses reduce the accuracy
  bonus; letter, brush, and whole-word hints reduce the hint bonus by 2, 4,
  and 8 points. The speed bonus drops from 10 to 7 to 4 in one-minute tiers,
  then reaches zero after three minutes. The in-game panel shows the live
  Workflow-derived speed, hint, and accuracy bonuses. A win always awards at
  least 10 points.
- Campaign levels may override the 10-point base score and the default hint
  prices without changing the shared level Workflow.
- Spendable points can be used for hints and streak freezes. Lifetime points
  track every earned game, milestone, and event point without decreasing when
  points are spent.
- Levels 5, 10, 15, and so on add campaign-configured milestone points; campaign events can add
  bonus points.
- Each game starts with two single-letter hints, one multi-letter brush, and one
  whole-word hint. Additional hints cost 10, 20, and 30 points respectively.
- A player can spend 50 points on one streak freeze. It is automatically
  consumed to protect the streak after exactly one missed Toronto calendar day.
- The included campaign has 30 Temporal-themed levels and two short bonus events.

## Run it

Requirements: Docker with Compose.

```bash
make up
```

Then open <http://localhost:8080>. The Temporal UI is at
<http://localhost:8233> and the event leaderboard at
<http://localhost:8080/leaderboard>. Compose runs a Temporal dev server,
DynamoDB Local (in memory, emptied on restart), the worker and the web app; the
worker and web app create the tables in DynamoDB Local.

| Command | Does |
| --- | --- |
| `make up` | Build and start the stack |
| `make down` | Stop it |
| `make test` | Run every test in a container, the store tests against DynamoDB Local |
| `make vet` | `go vet` in a container |
| `make workflowcheck` | Check Workflow code for non-determinism |

To present the demo, follow [RUNBOOK.md](RUNBOOK.md).

### Read-only Workflow Explorer

The web app includes an application-aware Workflow Explorer at
<http://localhost:8080/workflows/>.
It reads Visibility, Describe, and Event History data without issuing Workflow
Queries, Updates, or Signals. Anonymous visitors can inspect sanitized campaign
and campaign-generator executions. A browser carrying a valid Wordflow session
cookie can also inspect that player's level Workflows. Event details are decoded and sanitized at the API boundary;
credentials, puzzle answers, headers, identities, failure messages, memos, and
search attributes are never sent to the browser.

The explorer is served by the same web/API process and uses the same session
cookie, but neither the page nor its read-only endpoints execute or modify
Workflow state.

## Configuration

| Variable | Used by | Notes |
| --- | --- | --- |
| `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `TEMPORAL_API_KEY`, `TEMPORAL_TLS` | worker, web | Temporal connection |
| `WORDFLOW_PLAYERS_TABLE`, `WORDFLOW_CAMPAIGNS_TABLE` | worker, web | Required; both programs stop at startup without them |
| `DYNAMODB_ENDPOINT` | worker, web | DynamoDB Local only; the programs create the tables there |
| `TEMPORAL_WORKER_BUILD_ID`, `TEMPORAL_DEPLOYMENT_NAME` | worker | Set by the Worker Controller; without a Build ID the worker is unversioned |
| `OPENAI_API_KEY`, `OPENAI_MODEL` | worker | Required for campaign generation; the model defaults to `gpt-5.4-mini` |
| `SESSION_JWT_SECRET` | web | At least 32 characters, the same on every replica |

On the demo platform, the DemoProject (`projects/{test,live}/demo/wordflow.yaml`
in temporal-sa/temporal-fieldkit-platform) declares both tables in `spec.aws`,
which also gives the pods their AWS credentials, and runs the worker under the
Worker Controller. A version with Pinned Workflows still open keeps its pods
until they finish.

## Identity model

Signup is local to the application. The normalized username is the player's
key in the `players` table and appears in level Workflow IDs. Before storing
anything, the API derives a PBKDF2-SHA256 password hash with a username-derived
salt; raw passwords are never stored or sent to Temporal. The browser keeps an
API-signed JWT in an HTTP-only cookie. The public display name gets a stable
eight-character discriminator derived from the username, such as
`Michael#8f3a1c2d`.

This small authentication model suits a demo: its deterministic hash is still
open to offline guessing if the table is exposed, and there is no email
verification, account recovery, MFA, or distributed rate limiting.
