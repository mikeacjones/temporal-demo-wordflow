package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/player"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
)

func TestWorkflowExplorerURL(t *testing.T) {
	t.Parallel()

	got := workflowExplorerURL("player/42/game/2", "run-id")
	want := "/workflows/?runId=run-id&workflowId=player%2F42%2Fgame%2F2"
	if got != want {
		t.Fatalf("workflowExplorerURL() = %q, want %q", got, want)
	}
}

func TestWorkflowExplorerURLWithoutRunID(t *testing.T) {
	t.Parallel()

	got := workflowExplorerURL("wordflow-campaign/temporal-foundations", "")
	want := "/workflows/?workflowId=wordflow-campaign%2Ftemporal-foundations"
	if got != want {
		t.Fatalf("workflowExplorerURL() = %q, want %q", got, want)
	}
}

func TestIndexWithoutSessionRendersLoginImmediately(t *testing.T) {
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, newMemoryStore(), "test-task-queue", "default",
		"a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "games.example.test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), `data-session="anonymous"`)
	require.Contains(t, response.Body.String(), `id="startup"`)
	require.Contains(t, response.Body.String(), `id="auth" class="auth-grid"`)
	require.Contains(t, response.Body.String(), `name="passwordConfirmation"`)
	require.Contains(t, response.Body.String(), `property="og:url" content="https://games.example.test/"`)
	require.Contains(t, response.Body.String(), `property="og:image" content="https://games.example.test/social-preview.png"`)
	require.NotContains(t, response.Body.String(), "{{CANONICAL_URL}}")
}

func TestSignupRequiresMatchingPasswordConfirmation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "mismatch",
			body: `{"username":"alice","displayName":"Alice","password":"password123","passwordConfirmation":"password124"}`,
		},
		{
			name: "missing confirmation",
			body: `{"username":"alice","displayName":"Alice","password":"password123"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{temporal: temporalmocks.NewClient(t), store: newMemoryStore()}
			request := httptest.NewRequest("POST", "/api/signup", bytes.NewBufferString(test.body))
			response := httptest.NewRecorder()

			server.signUp(response, request)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.JSONEq(t, `{"error":"passwords do not match"}`, response.Body.String())
		})
	}
}

func TestSocialPreviewImageIsPublic(t *testing.T) {
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, newMemoryStore(), "test-task-queue", "default",
		"a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest("GET", "/social-preview.png", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Equal(t, "image/png", response.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=3600", response.Header().Get("Cache-Control"))
	require.Greater(t, response.Body.Len(), 1000)
}

func TestWorkflowExplorerIsMountedInWebApp(t *testing.T) {
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, newMemoryStore(), "test-task-queue", "default",
		"a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest("GET", "/workflows/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "Workflow Explorer")
	require.NotContains(t, response.Body.String(), "Explore campaigns, timers")
	require.NotContains(t, response.Body.String(), "Sanitized at the API boundary")
	require.Contains(t, response.Body.String(), `id="show-worker-events" type="checkbox" checked`)
}

func TestIndexWithExpiredSessionRendersLoginImmediately(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now.Add(-2*time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, newMemoryStore(), "test-task-queue", "default", string(secret))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `data-session="anonymous"`)
}

func TestIndexWithValidSessionStartsOnNeutralLoadingScreen(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, newMemoryStore(), "test-task-queue", "default", string(secret))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `data-session="authenticated"`)
}

func TestPlayerResponseContainsOnlyBrowserFields(t *testing.T) {
	response := newPlayerResponse(game.PlayerView{
		DisplayName: "Alice", Points: 25, CompletedLevelCount: 2,
		Campaigns: []campaign.PlayerCampaignProgress{{CampaignID: "campaign"}},
		ActiveGame: &game.ActiveGame{
			WorkflowID: "level/workflow", CampaignID: "campaign", Level: 2, Title: "Title", TotalLevels: 3,
		},
	})

	payload := jsonObject(t, response)
	for _, field := range []string{"playerId", "createdAt", "lastSeenAt", "campaigns", "completedLevels", "rewards"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("player response unexpectedly contains %q", field)
		}
	}
	var active map[string]json.RawMessage
	if err := json.Unmarshal(payload["activeGame"], &active); err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active["campaignId"] == nil || active["level"] == nil {
		t.Fatalf("activeGame fields = %v, want only campaignId and level", active)
	}
}

func TestCampaignResponseOmitsWorkflowOnlyFields(t *testing.T) {
	response := newCatalogCampaignResponse(campaign.View{
		CampaignID: "campaign", WorkflowID: "campaign/workflow",
		Kind: campaign.KindDailyChallenge, Failed: true,
		Game: campaign.GameSummary{ID: "wordflow"}, Title: "Campaign", Eligible: true,
	}, "https://example.test/workflow")
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "game", "eligible"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("campaign response unexpectedly contains %q", field)
		}
	}
	require.NotNil(t, payload["kind"])
	require.NotNil(t, payload["failed"])
}

func TestGameResponseOmitsWorkflowStateNotRenderedByBrowser(t *testing.T) {
	completedAt := time.Now()
	expiresAt := completedAt.Add(time.Minute)
	response := newGameResponse(game.GameView{
		CampaignID: "campaign", Level: 1, TimedOut: true, ExpiresAt: &expiresAt,
		SolutionWords: []game.SolutionWordView{{Answer: "FLOW", Found: false}},
		Score:         &game.GameScore{Points: 10, IncorrectGuesses: 3, HintsUsed: 2},
	})
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "playerId", "words", "completedAt"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("game response unexpectedly contains %q", field)
		}
	}
	require.NotNil(t, payload["timedOut"])
	require.NotNil(t, payload["expiresAt"])
	require.NotNil(t, payload["solutionWords"])
	var score map[string]json.RawMessage
	if err := json.Unmarshal(payload["score"], &score); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"incorrectGuesses", "hintsUsed"} {
		if _, ok := score[field]; ok {
			t.Fatalf("score response unexpectedly contains %q", field)
		}
	}
}

func jsonObject(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

// memoryStore is the Store in memory, for handler tests.
type memoryStore struct {
	mu        sync.Mutex
	players   map[string]player.State
	campaigns []catalog.Registration
}

func newMemoryStore() *memoryStore {
	return &memoryStore{players: map[string]player.State{}}
}

func (m *memoryStore) CreatePlayer(_ context.Context, state player.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.players[state.PlayerID]; ok {
		return store.ErrExists
	}
	m.players[state.PlayerID] = state
	return nil
}

func (m *memoryStore) Player(_ context.Context, playerID string) (player.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.players[playerID]
	if !ok {
		return player.State{}, store.ErrNotFound
	}
	return state, nil
}

func (m *memoryStore) UpdatePlayer(_ context.Context, playerID string, change func(*player.State) error) (player.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.players[playerID]
	if !ok {
		return player.State{}, store.ErrNotFound
	}
	if err := change(&state); err != nil {
		return player.State{}, err
	}
	m.players[playerID] = state
	return state, nil
}

func (m *memoryStore) Campaigns(context.Context) ([]catalog.Registration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]catalog.Registration(nil), m.campaigns...), nil
}

func (m *memoryStore) Leaderboard(_ context.Context, limit int) ([]game.LeaderboardRank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ranks []game.LeaderboardRank
	for _, state := range m.players {
		if state.LifetimePointsEarned > 0 {
			ranks = append(ranks, game.LeaderboardRank{DisplayName: state.DisplayName, LifetimePointsEarned: state.LifetimePointsEarned})
		}
	}
	sort.Slice(ranks, func(i, j int) bool { return ranks[i].LifetimePointsEarned > ranks[j].LifetimePointsEarned })
	if len(ranks) > limit {
		ranks = ranks[:limit]
	}
	for index := range ranks {
		ranks[index].Rank = index + 1
	}
	return ranks, nil
}

const testSecret = "a-test-session-secret-with-at-least-32-characters"

func signedInRequest(t *testing.T, method, target, body, playerID string) *http.Request {
	t.Helper()
	now := time.Now()
	token, err := newSessionToken([]byte(testSecret), playerID, now, now.Add(time.Hour))
	require.NoError(t, err)
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	return request
}

func TestSignUpThenLogInWithTheSamePassword(t *testing.T) {
	data := newMemoryStore()
	handler := New(temporalmocks.NewClient(t), data, "test-task-queue", "default", testSecret)

	signUp := httptest.NewRecorder()
	handler.ServeHTTP(signUp, httptest.NewRequest("POST", "/api/signup", bytes.NewBufferString(
		`{"username":"Alice","displayName":"Alice","password":"password123","passwordConfirmation":"password123"}`)))
	require.Equal(t, http.StatusCreated, signUp.Code, signUp.Body.String())
	require.Contains(t, signUp.Header().Get("Set-Cookie"), sessionCookieName+"=")
	stored, err := data.Player(context.Background(), "alice")
	require.NoError(t, err)
	require.NotEmpty(t, stored.PasswordHash)
	require.NotContains(t, signUp.Body.String(), stored.PasswordHash)

	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, httptest.NewRequest("POST", "/api/signup", bytes.NewBufferString(
		`{"username":"alice","displayName":"Alice","password":"password123","passwordConfirmation":"password123"}`)))
	require.Equal(t, http.StatusCreated, retry.Code, "a retried sign-up signs in")

	taken := httptest.NewRecorder()
	handler.ServeHTTP(taken, httptest.NewRequest("POST", "/api/signup", bytes.NewBufferString(
		`{"username":"alice","displayName":"Mallory","password":"different1","passwordConfirmation":"different1"}`)))
	require.Equal(t, http.StatusConflict, taken.Code)

	logIn := httptest.NewRecorder()
	handler.ServeHTTP(logIn, httptest.NewRequest("POST", "/api/login", bytes.NewBufferString(
		`{"username":"alice","password":"password123"}`)))
	require.Equal(t, http.StatusOK, logIn.Code, logIn.Body.String())
}

func TestLoginCredentialFailuresAreIndistinguishable(t *testing.T) {
	data := newMemoryStore()
	hash, err := passwordHash("alice", "password123")
	require.NoError(t, err)
	require.NoError(t, data.CreatePlayer(context.Background(), player.New("alice", "Alice#1", hash, time.Now())))
	server := &Server{temporal: temporalmocks.NewClient(t), store: data, sessionJWTSecret: []byte(testSecret)}

	for name, body := range map[string]string{
		"unknown username": `{"username":"bob","password":"password123"}`,
		"wrong password":   `{"username":"alice","password":"wrong-password"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.logIn(response, httptest.NewRequest("POST", "/api/login", bytes.NewBufferString(body)))

			require.Equal(t, 401, response.Code)
			require.JSONEq(t, `{"error":"Invalid username/password combination"}`, response.Body.String())
		})
	}
}

func TestCatalogComesFromTheStoreWithThePlayersProgress(t *testing.T) {
	data := newMemoryStore()
	data.campaigns = []catalog.Registration{{
		WorkflowID: "wordflow-campaign/campaign",
		Definition: campaign.Definition{
			ID: "campaign", Title: "Campaign", Unlock: campaign.UnlockPolicy{InitialLevels: 2},
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels: []game.Puzzle{{Level: 1, Title: "One"}, {Level: 2, Title: "Two"}},
	}}
	account := player.New("alice", "Alice#1", "hash", time.Now())
	account.Campaigns = []campaign.PlayerCampaignProgress{{CampaignID: "campaign", JoinedAt: time.Now(), NextLevel: 2, CompletedLevels: 1}}
	server := &Server{store: data}

	response, err := server.catalog(context.Background(), account)

	require.NoError(t, err)
	require.Len(t, response.Campaigns, 1)
	card := response.Campaigns[0]
	require.Equal(t, "Campaign", card.Title)
	require.Equal(t, "/workflows/?workflowId=wordflow-campaign%2Fcampaign", card.WorkflowURL)
	require.Equal(t, 2, card.NextLevel)
	require.Equal(t, campaign.LevelComplete, card.Levels[0].Status)
	require.Equal(t, campaign.LevelAvailable, card.Levels[1].Status)
}

func TestStartGameIsOneUpdateWithStartOfTheLevelWorkflow(t *testing.T) {
	handle := temporalmocks.NewWorkflowUpdateHandle(t)
	handle.On("Get", mock.Anything, mock.Anything).Run(func(arguments mock.Arguments) {
		*arguments.Get(1).(*workflows.StartLevelResult) = workflows.StartLevelResult{
			Player: game.PlayerView{DisplayName: "Alice", ActiveGame: &game.ActiveGame{CampaignID: "campaign", Level: 1}},
			Game:   game.GameView{CampaignID: "campaign", Level: 1, Title: "One", Letters: "ONE"},
		}
	}).Return(nil).Once()
	levelID := workflows.WordflowLevelWorkflowID("alice", "campaign", 1)
	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("NewWithStartWorkflowOperation", mock.MatchedBy(func(options client.StartWorkflowOptions) bool {
		return options.ID == levelID && options.TaskQueue == "wordflow" &&
			options.WorkflowIDConflictPolicy == enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING
	}), workflows.WordflowLevelWorkflowName, workflows.WordflowLevelWorkflowInput{
		PlayerID: "alice", CampaignID: "campaign", Level: 1,
	}).Return((client.WithStartWorkflowOperation)(nil)).Once()
	temporalClient.On("UpdateWithStartWorkflow", mock.Anything, mock.MatchedBy(func(options client.UpdateWithStartWorkflowOptions) bool {
		return options.UpdateOptions.UpdateName == workflows.UpdateStartLevel &&
			options.UpdateOptions.UpdateID == "start-test" &&
			options.UpdateOptions.WaitForStage == client.WorkflowUpdateStageCompleted
	})).Return(handle, nil).Once()

	request := signedInRequest(t, "POST", "/api/me/campaigns/campaign/levels", `{"requestId":"start-test","level":1}`, "alice")
	request.SetPathValue("campaign", "campaign")
	response := httptest.NewRecorder()
	server := &Server{temporal: temporalClient, store: newMemoryStore(), taskQueue: "wordflow", sessionJWTSecret: []byte(testSecret)}

	server.startGame(response, request)

	require.Equal(t, 200, response.Code, response.Body.String())
	require.JSONEq(t, `{
		"player":{"displayName":"Alice","currentStreak":0,"bestStreak":0,"points":0,
			"lifetimePointsEarned":0,"streakFreeze":false,"streakFreezeCost":0,
			"completedLevelCount":0,"activeGame":{"campaignId":"campaign","level":1}},
		"game":{"campaignId":"campaign","level":1,"title":"One","letters":"ONE","cells":null,
			"foundWords":0,"totalWords":0,"attempts":0,"rejectedWords":null,"speedBonuses":null,
			"hintBonus":0,"accuracyBonus":0,"hints":{"letters":0,"brushes":0,"words":0},
			"hintPrices":{"letter":0,"brush":0,"word":0},"complete":false}
	}`, response.Body.String())
}

func TestStartGameReportsARefusedClaimAsAConflict(t *testing.T) {
	handle := temporalmocks.NewWorkflowUpdateHandle(t)
	handle.On("Get", mock.Anything, mock.Anything).
		Return(temporal.NewApplicationError("level 2 is not next; expected level 1", "level_not_next")).Once()
	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("NewWithStartWorkflowOperation", mock.Anything, mock.Anything, mock.Anything).
		Return((client.WithStartWorkflowOperation)(nil)).Once()
	temporalClient.On("UpdateWithStartWorkflow", mock.Anything, mock.Anything).Return(handle, nil).Once()
	request := signedInRequest(t, "POST", "/api/me/campaigns/campaign/levels", `{"level":2}`, "alice")
	request.SetPathValue("campaign", "campaign")
	response := httptest.NewRecorder()
	server := &Server{temporal: temporalClient, store: newMemoryStore(), sessionJWTSecret: []byte(testSecret)}

	server.startGame(response, request)

	require.Equal(t, http.StatusConflict, response.Code)
	require.JSONEq(t, `{"error":"level 2 is not next; expected level 1"}`, response.Body.String())
}

func TestAuthenticatedPlayerNeedsAStoredAccount(t *testing.T) {
	data := newMemoryStore()
	require.NoError(t, data.CreatePlayer(context.Background(), player.New("alice", "Alice#1", "hash", time.Now())))
	server := &Server{store: data, sessionJWTSecret: []byte(testSecret)}

	account, err := server.authenticatedPlayer(signedInRequest(t, "GET", "/api/me", "", "alice"))
	require.NoError(t, err)
	require.Equal(t, "Alice#1", account.DisplayName)

	_, err = server.authenticatedPlayer(signedInRequest(t, "GET", "/api/me", "", "bob"))
	require.EqualError(t, err, "session is invalid or expired")
}

func TestBuyStreakFreezeRefusesWithoutEnoughPoints(t *testing.T) {
	data := newMemoryStore()
	require.NoError(t, data.CreatePlayer(context.Background(), player.New("alice", "Alice#1", "hash", time.Now())))
	server := &Server{store: data, sessionJWTSecret: []byte(testSecret)}
	response := httptest.NewRecorder()

	server.buyStreakFreeze(response, signedInRequest(t, "POST", "/api/me/streak-freeze", `{"requestId":"r"}`, "alice"))

	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), "not enough points")
}

func TestLeaderboardComesFromTheStore(t *testing.T) {
	data := newMemoryStore()
	for name, points := range map[string]int{"alice": 40, "bob": 90, "carol": 0} {
		account := player.New(name, name, "hash", time.Now())
		account.LifetimePointsEarned = points
		require.NoError(t, data.CreatePlayer(context.Background(), account))
	}
	server := &Server{store: data}
	response := httptest.NewRecorder()

	server.getLeaderboard(response, httptest.NewRequest("GET", "/api/leaderboard", nil))

	require.Equal(t, 200, response.Code)
	require.JSONEq(t, `{"entries":[
		{"displayName":"bob","lifetimePointsEarned":90,"completedLevels":0,"rank":1},
		{"displayName":"alice","lifetimePointsEarned":40,"completedLevels":0,"rank":2}
	]}`, response.Body.String())
}
