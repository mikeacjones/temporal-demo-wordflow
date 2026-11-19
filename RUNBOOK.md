# Runbook: Temporal Wordflow

This runbook is for presenting the demo. The [README](README.md) explains what it is and how it's built.

## What it shows

Wordflow is a word-crossword game. Every level a player plays is a Temporal Workflow, and every guess and hint is an Update to it. Player accounts, points and the campaign catalog live in DynamoDB; the Workflows hold what is in motion.

The demo makes three problems visible:

- **A game in progress has to survive anything.** The board, the guesses and the clock can't live in the browser or in a server's memory. Here they live in the level's Workflow, which picks up where it was after the Worker dies. Beats 2 and 3 show it.
- **Every request must apply exactly once, even when something fails or retries.** A guess, a hint or a level's points can't be lost or counted twice, including when the level has to change the player's record in a database. Beats 2, 3 and 4 show it.
- **Code changes ship while people are mid-game.** A level that started on one build finishes on that build, while new levels start on the new one. Beat 6 shows it on tmprl-demo.cloud.

## Before you start

- Install Docker with Compose. Nothing else runs on your machine; the Temporal dev server, DynamoDB Local, the Worker and the web app are containers.
- No model or other paid API is needed. The Worker refuses to start without `OPENAI_API_KEY`, so Compose passes a placeholder unless your shell sets one. Only the nightly campaign generation calls OpenAI, and this runbook doesn't use it.

### On tmprl-demo.cloud

The catalog entry ([`projects/live/demo/wordflow.yaml`](https://github.com/temporal-sa/temporal-fieldkit-platform/blob/main/projects/live/demo/wordflow.yaml)) runs the same code against a project Temporal Cloud Namespace:

- `web` is the game and API (`cmd/server`), at https://games.tmprl-demo.cloud. It needs no Temporal login to play.
- `worker` is the Worker (`cmd/worker`), run by the Temporal Worker Controller as Worker Deployment `temporal-word-game`. Each change to the repo's `main` branch rolls out as a new Worker Deployment Version.
- The platform creates the two DynamoDB tables and gives both pods their credentials.
- The Worker isn't crashable from the platform, so beat 3's "stop the Worker" step is local only.

## Start it

From the repository root:

```bash
make up
```

Open http://localhost:8080. A healthy start shows the **Log in** and **Sign up** cards under the **Temporal Wordflow** header. The `worker` logs show `Started Worker ... TaskQueue temporal-word-game` and a `RegisterCampaign` Activity: the web app starts the `Temporal Foundations` campaign's Workflow at startup, and it publishes the campaign to the catalog table and completes.

Keep three more tabs ready:

- http://localhost:8080/leaderboard, the public leaderboard.
- http://localhost:8080/workflows/, the read-only Workflow Explorer. The **My Workflows ↗**, **Game Workflow ↗** and **Campaign Workflow ↗** links in the game open it.
- The Temporal UI at http://localhost:8233, for anyone who wants the raw Event History.

### Answers for the first levels

So you don't have to solve puzzles live:

| Level | Letters | Answers |
|---|---|---|
| 1 · Workflow Flow | FLOW | FLOW, WOLF, LOW, OWL |
| 2 · Workflow Time | EIMT | TIME, ITEM, EMIT, TIE |
| 3 · Durable State | AESTT | STATE, TASTE, SEAT, EAST, TEA, SET |

The rest are in [`internal/bootstrap/temporal-foundations.json`](internal/bootstrap/temporal-foundations.json).

## The walkthrough

To spell a word, click the letters in the circle, then **Submit word**. **Clear** empties the word and **Shuffle** reorders the letters. The game has no keyboard shortcuts.

### 1. Sign up

1. In **Sign up**, enter a username, a display name, a password, and the same password in **Confirm password**. Press **Create account**. Use a new username for every presentation.
2. The page switches to the player view: five stats (**current streak**, **best streak**, **points**, **lifetime earned**, **levels completed**), the **Streak freeze** shop, and the **Temporal Foundations** campaign card with levels 1 to 3 open and the rest locked.
3. Say: "My account is a row in DynamoDB: a password hash, points and progress. Nothing has run in Temporal for me yet. That starts when I play."

### 2. Play a level, and each guess is an Update

1. Press **Start campaign**. Level 1, **Workflow Flow**, opens with the letters F, L, O and W.
2. Spell `WOLF` and press **Submit word**. The notice reads **Word recorded in history.** and the word fills in on the grid. Spell `FOL`; the notice reads **Not in the wordflow.** and the word appears in the **Not in the wordflow** list beside the board.
3. Point at the **Bonus points** panel. The speed, hint and accuracy bonuses are computed in the level Workflow, and the accuracy bonus just dropped by 2.
4. Click **Game Workflow ↗**. The Explorer opens `wordflow-level/<username>/temporal-foundations/1`. Its timeline starts with the `start-level` Update and a `ClaimLevel` Activity, then one `submit-guess` Update per guess.
5. Say: "Pressing Start sent one call that started this Workflow and ran its first Update. That Update claimed the level in my record and returned the board. Each guess is another Update that returns the new board, so the browser never computes game state."
6. Finish the level with `FLOW`, `LOW` and `OWL`. The card reads **Workflow completed · Execution succeeded.** with the points breakdown. In the Explorer, the last step is a `RecordLevelResult` Activity: that's the points reaching my record.

### 3. Stop the Worker mid-level, and nothing is lost (local only)

1. Press **Play level 2**. Spell one word, such as `TIME`, and submit it.
2. Stop the Worker: `docker compose stop worker`.
3. Spell `ITEM` and press **Submit word**. The button reads **Submitting…** and stays that way, because no Worker is running the Workflow.
4. Start the Worker again: `docker compose start worker`. Within a few seconds the guess lands and the notice reads **Word recorded in history.**
5. Reload the page. You're still logged in, and level 2 comes back with both words in place.
6. Say: "The Worker was gone, and the request waited for it. When it came back, it rebuilt the level from its history and applied the guess once. Nothing saved the board anywhere."

On tmprl-demo.cloud, skip steps 2 to 4. Reload the page or log out and back in instead: the level resumes where you left it, because it was never in the browser.

### 4. Buy a hint with points

1. On level 2, the button reads **Reveal one · 2 free**. Press it twice; each press reveals a letter and counts down a free hint.
2. The button now reads **Reveal one · 10 points**. Press it. The notice reads **Spent 10 points · letter revealed**, and the player's points drop by 10.
3. Click **Game Workflow ↗**. The level's timeline shows a `SpendPoints` Activity inside the `use-hint` Update.
4. Say: "The level owns the hint and my record owns the points. The level charges me with an Activity, and the charge only goes through while this level is the one I have open and I can afford it. A retry can't take the points twice, because a retried Update gets its first answer back."

You need points for this beat, so play it after finishing level 1. A new player has 0 points, and a paid hint before the first win fails with `not enough points; need 10, have 0`.

### 5. The leaderboard

1. Finish level 2 with the remaining answers.
2. Switch to the leaderboard tab. Your display name, with its `#` tag, appears with your lifetime points and levels completed. The page refreshes every 3 seconds.
3. Say: "The leaderboard is an index on the players table, sorted by lifetime points. Recording a level's result updates it in the same write."

### 6. Ship new code without breaking players mid-level (tmprl-demo.cloud)

This beat needs a deployment on tmprl-demo.cloud, where the Worker is versioned. The local Worker is unversioned.

1. Start a level and leave it open. In the Workflow Explorer, open its `wordflow-level/...` Workflow. The strip under the metrics reads **Versioning** `PINNED`, **Deployment** `temporal-word-game`, and **Build** with the rollout's Build ID.
2. Say: "Every run is pinned to the build it started on, so a deploy never swaps code under a game in progress."
3. After a new rollout of `main` becomes current, make another guess in the open level. It still runs on the old Build ID; start the next level and its Workflow shows the new one.
4. Say: "The Worker Controller keeps the old build's Worker running until its last pinned Workflow finishes, then scales it down. Nobody wrote a version check in the game code. Levels with no time limit end after a day without a move, so an abandoned game can't keep an old build alive forever."

## Show it in code

These entries link to commit `c6c1f9d`.

### Start a level with one call

**When to use it:** after beat 2, or when someone asks how the API knows whether the Workflow exists yet.

**Where it is:** [`internal/api/server.go` lines 556–580](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/api/server.go#L556-L580) (`code -g internal/api/server.go:556`)

```go
// One call starts the level's Workflow and runs its start-level Update,
// which claims the level in the player's record and returns the board.
ctx := request.Context()
start := s.temporal.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
	ID:                       workflows.WordflowLevelWorkflowID(playerID, campaignID, input.Level),
	TaskQueue:                s.taskQueue,
	WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
}, workflows.WordflowLevelWorkflowName, workflows.WordflowLevelWorkflowInput{
	PlayerID: playerID, CampaignID: campaignID, Level: input.Level,
})
handle, err := s.temporal.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
	StartWorkflowOperation: start,
	UpdateOptions: client.UpdateWorkflowOptions{
		UpdateID: input.RequestID, UpdateName: workflows.UpdateStartLevel,
		WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{workflows.StartLevelInput{}},
	},
})
```

**What you need to understand:**

- Update-With-Start starts the Workflow if no run with that ID is open and sends it the Update, in one round trip. The Go SDK requires a conflict policy for it; `USE_EXISTING` sends the Update to the open run if there is one.
- The Workflow ID is `wordflow-level/<player>/<campaign>/<level>`, so a player can't have two runs of the same level open.
- `WaitForStage: Completed` makes the HTTP call return the Update's result, which is the opening board.

**Docs:** [Update-With-Start](https://docs.temporal.io/develop/go/workflows/message-passing), [Workflow ID conflict policy](https://docs.temporal.io/workflow-execution/workflowid-runid)

### Claim the level inside the Update, and end the Workflow if the claim is refused

**When to use it:** after beat 2, or when someone asks how the game stops a player opening a locked level.

**Where it is:** [`internal/workflows/wordflow_level.go` lines 128–154](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/workflows/wordflow_level.go#L128-L154) (`code -g internal/workflows/wordflow_level.go:128`), calling [`internal/activities/levels.go` lines 31–61](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/activities/levels.go#L31-L61) (`code -g internal/activities/levels.go:31`)

```go
if state != nil {
	return StartLevelResult{Game: wordflowLevelView(state)}, nil
}
info := workflow.GetInfo(updateCtx)
startedAt := workflow.Now(updateCtx)
var claim ClaimLevelResult
err := workflow.ExecuteActivity(workflow.WithActivityOptions(updateCtx, workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
}), ActivityClaimLevel, ClaimLevelActivityInput{
	PlayerID: input.PlayerID, CampaignID: input.CampaignID, Level: input.Level,
	LevelWorkflowID: info.WorkflowExecution.ID, StartedAt: startedAt,
}).Get(updateCtx, &claim)
if err != nil {
	// The player can't have this level now; the Workflow ends without a result.
	rejected = true
	return StartLevelResult{}, err
}
```

```go
state, err := a.Store.UpdatePlayer(ctx, input.PlayerID, func(state *player.State) error {
	if state.ActiveGame != nil && state.ActiveGame.WorkflowID == input.LevelWorkflowID &&
		input.Level >= 1 && input.Level <= len(registration.Levels) {
		claimed = catalog.Resolution{Allowed: true, Puzzle: registration.Levels[input.Level-1], TotalLevels: len(registration.Levels)}
		return nil
	}
	claimed = catalog.Resolve(registration, state.ProgressForStart(input.CampaignID, input.StartedAt), input.Level, input.StartedAt)
	if !claimed.Allowed {
		return &player.Error{Type: claimed.ErrorType, Message: claimed.Reason}
	}
	// ...
```

**What you need to understand:**

- An Update handler can run Activities. The board isn't returned until the claim has landed in the player's record.
- The Activity reads the campaign, checks the unlock and eligibility rules, and marks the level open in one conditional write. A player with another level open is refused.
- If the claim already landed (an Activity retry after a timeout), the Activity sees its own Workflow ID as the open level and returns the same puzzle instead of refusing.
- A refusal is a non-retryable `ApplicationError` with a stable type such as `level_not_next`. It comes back as the Update's error, and the API turns it into a 409 with the message.

**Docs:** [Request-Response via Updates](https://docs.temporal.io/design-patterns/request-response-via-updates), [Non-Retryable Errors](https://docs.temporal.io/design-patterns/non-retryable-errors)

### Make every guess an Update, with the request ID as the Update ID

**When to use it:** after beat 2 or 3, or when someone asks what happens if the same request arrives twice.

**Where it is:** [`internal/api/server.go` lines 787–796](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/api/server.go#L787-L796) (`code -g internal/api/server.go:787`). The browser makes the ID in [`internal/api/web/app.js` lines 710–717](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/api/web/app.js#L710-L717).

```go
func (s *Server) update(ctx context.Context, workflowID, updateID, updateName string, input, output any) error {
	handle, err := s.temporal.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID: workflowID, UpdateID: updateID, UpdateName: updateName,
		WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{input},
	})
	if err != nil {
		return err
	}
	return handle.Get(ctx, output)
}
```

```js
async function submitGuess() {
  const word = selectedIndexes.map((index) => letterOrder[index]).join("");
  if (!word) return;
  const result = await withGameMutation(elements.submit, "Submitting…", () =>
    api(`/api/me/campaigns/${currentGame.campaignId}/levels/${currentGame.level}/guesses`, {
      method: "POST",
      body: JSON.stringify({ requestId: requestID(), word }),
    }));
```

**What you need to understand:**

- `WaitForStage: Completed` makes the HTTP call return the Update's result, which is the new board. That's why the button waits on **Submitting…** in beat 3: the Update waits for a Worker.
- The Temporal Service deduplicates Updates by Update ID within a Workflow run. If the same request ID arrives twice, the second call gets the first call's result and the handler doesn't run again.
- The browser makes a new ID for each click. Deduplication protects against a request delivered twice, such as a client or gateway retry, not against a player pressing the button twice.

**Docs:** [Message IDs](https://docs.temporal.io/handling-messages#exactly-once-message-processing), [Go: Send an Update](https://docs.temporal.io/develop/go/workflows/message-passing#send-update-from-client)

### Change the player's record exactly once from a Workflow

**When to use it:** after beat 4 or 5, or when someone asks how a Workflow changes a database without double-counting on a retry.

**Where it is:** the hint in [`internal/workflows/wordflow_level.go` lines 228–244](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/workflows/wordflow_level.go#L228-L244) (`code -g internal/workflows/wordflow_level.go:228`), the result in [`internal/activities/levels.go` lines 74–84](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/activities/levels.go#L74-L84) (`code -g internal/activities/levels.go:74`), and the write in [`internal/store/store.go` lines 139–170](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/store/store.go#L139-L170) (`code -g internal/store/store.go:139`)

```go
if freeHintCount(state, update.Hint) == 0 {
	price, err := hintPointCost(state.Puzzle, update.Hint)
	// ...
	var spend SpendPointsResult
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(updateCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	}), ActivitySpendPoints, SpendPointsActivityInput{
		PlayerID: state.PlayerID, LevelWorkflowID: state.WorkflowID, Amount: price,
	}).Get(updateCtx, &spend)
```

```go
// RecordLevelResult applies a finished level to the player's record: points,
// streak and progress, and closes the level so the player can start another.
// A result recorded before is ignored, so a retry changes nothing.
func (a *Levels) RecordLevelResult(ctx context.Context, input workflows.RecordLevelResultActivityInput) error {
	_, err := a.Store.UpdatePlayer(ctx, input.PlayerID, func(state *player.State) error {
		state.FinishLevel(input.LevelWorkflowID, input.Result)
		return nil
	})
	return playerError(err)
}
```

```go
_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
	TableName:                aws.String(s.players),
	Item:                     item,
	ConditionExpression:      aws.String("#version = :version"),
	// ...
})
if isConditionFailed(err) {
	continue
}
```

**What you need to understand:**

- Activities can run more than once: a timeout after the write landed means a retry. Each one is written so a second run changes nothing. `RecordLevelResult` only applies a result while that level is the one the player has open, and closing it is part of the same write.
- A hint's `SpendPoints` runs inside the `use-hint` Update. If the API retries that Update with the same ID, Temporal returns the first answer and the Activity doesn't run again.
- The store writes a player with a condition on the version it read. Two writes at once (a hint and a streak freeze) can't overwrite each other: the loser reads again and reapplies its change.
- A broken game rule, such as `insufficient_points`, is a non-retryable error. Anything else, such as DynamoDB being unreachable, is retried by the default Retry Policy.

**Docs:** [Idempotent Activities](https://docs.temporal.io/activity-definition), [Retry Policies](https://docs.temporal.io/encyclopedia/retry-policies)

### Pin each Workflow to its build, and keep every Workflow short

**When to use it:** during beat 6.

**Where it is:** [`internal/workflows/names.go` lines 50–64](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/workflows/names.go#L50-L64) (`code -g internal/workflows/names.go:50`), and the idle limit in [`internal/workflows/wordflow_level.go` lines 291–319](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/workflows/wordflow_level.go#L291-L319) (`code -g internal/workflows/wordflow_level.go:291`)

```go
// Register registers the Workflows, Pinned when Worker Versioning is on: a
// level, campaign window or generation run finishes on the version it started on.
func Register(registry worker.WorkflowRegistry, versioned bool) {
	behavior := workflow.VersioningBehaviorUnspecified
	if versioned {
		behavior = workflow.VersioningBehaviorPinned
	}
	// ...
}
```

```go
if deadline == nil && idle == nil {
	idleFor := max(lastMoveAt(state).Add(untimedLevelIdleLimit).Sub(workflow.Now(ctx)), time.Second)
	idle = workflow.NewTimer(ctx, idleFor)
}
// ...
if idle != nil {
	selector.AddFuture(idle, func(workflow.Future) {
		idle = nil
		if !workflow.Now(ctx).Before(lastMoveAt(state).Add(untimedLevelIdleLimit)) {
			state.Abandoned = true
		}
	})
}
```

**What you need to understand:**

- With a Build ID set, every Workflow type is Pinned: a run stays on the Worker Deployment Version it started on. The Worker Controller injects the Build ID and keeps a version's pods until no pinned Workflow is open on it.
- Pinned only works if Workflows end. A level ends when it's solved, at its time limit, or after `untimedLevelIdleLimit` (24 hours) without a move. A campaign Workflow publishes its campaign and completes, or waits for its end date. Player accounts, which never end, aren't Workflows.
- The idle timer isn't restarted on every guess. When it fires, the Workflow checks the time since the last move and starts a new timer if the player came back.
- There is no patching or version branching in the Workflow code.

**Docs:** [Worker Versioning](https://docs.temporal.io/worker-versioning), [Temporal Worker Controller](https://github.com/temporalio/temporal-worker-controller)

### What's demo-only

When someone asks what they'd change for their own system, these are the parts to leave out or replace:

- **The identity model.** [`internal/api/auth.go`](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/api/auth.go) derives a deterministic password hash from the username and signs a 30-day session JWT. It has no email verification, account recovery, MFA or rate limiting. A real app would use an identity provider.
- **The Workflow Explorer.** [`internal/explorer/server.go` lines 37–41](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/internal/explorer/server.go#L37-L41) uses only List, Describe and Get History, and sanitizes every payload, so visitors can see the machinery without a Temporal login. It's for the demo audience; operators use the Temporal UI.
- **Publishing the campaign at server start.** [`cmd/server/main.go` line 62](https://github.com/temporal-sa/temporal-demo-wordflow/blob/c6c1f9d9a25a85ebe12630c63d02e7ca50439ebf/cmd/server/main.go#L62) starts the built-in campaign's Workflow every time the web server starts, which republishes it. A real system would publish campaigns from an admin tool, as `cmd/campaign` does.
- **The game rules.** Scoring, hints, streaks and unlock timing are game logic, not Temporal features.

## Reset and tear down

- **Between presentations:** sign up with a new username. Every player is separate, so nothing else needs resetting. The leaderboard keeps earlier players.
- **To wipe everything locally:** `make down`, then `make up`. DynamoDB Local and the Temporal dev server keep their data in memory, so both start empty.
- **To stop locally:** `make down`.
- **On tmprl-demo.cloud:** there's nothing to stop. A level you leave open ends by itself after a day without a move. Close the leaderboard tab when you're done; it reads the table every 3 seconds.
- **Moving tmprl-demo.cloud from the old design:** Workflows of the removed types (`PlayerWorkflow`, `CatalogWorkflow`, `LeaderboardWorkflow`) and long-lived `WordflowCampaignWorkflow` runs stay pinned to old builds and keep those builds' Workers running. Terminate them once the new version is current; the new tables start empty and everyone signs up again.

## When something goes wrong

| What you see | Likely cause | Fix |
|---|---|---|
| The `worker` or `web` container exits with `[WORDFLOW_PLAYERS_TABLE ...] must be set` | It started without the table names | Use `make up`, which sets them; on the platform, check the DemoProject's `spec.aws` |
| The page stays on **Loading campaigns** or a button stays on **Submitting…** | No Worker is running; every Update waits for one | `docker compose start worker` |
| **Start campaign** fails with `level 2 is not next; expected level 1` or `the next level has not unlocked yet` | The catalog's rules refused the claim | Play the level the card offers |
| **Reveal one** fails with `not enough points` | The player hasn't won a level yet | Finish level 1 first; it pays at least 10 points |
| **Start campaign** fails with `Finish the active level before starting another campaign.` | A level is still open | Press **Resume level N** and finish it |
| **Create account** fails with `username is already registered with a different password` | The username is taken | Use a new username |
| The Explorer shows no **Versioning** strip | The Worker is unversioned, which is normal locally | Show beat 6 on tmprl-demo.cloud |

## Keeping this runbook current

The code links point at a fixed commit. When a linked file changes, update its link, line numbers and snippet together. Before an event, list the linked files that have changed:

```sh
grep -oE 'blob/[0-9a-f]{40}/[^#)]+' RUNBOOK.md | sort -u | while IFS=/ read -r _ sha file; do
  git diff --quiet "$sha" HEAD -- "$file" || echo "changed since ${sha:0:7}: $file"
done
```
