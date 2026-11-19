package activities

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/dictionary"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/temporal"
)

const levelAnswerCount = 10

const campaignAgentInstructions = `You design levels for Wordflow, a word puzzle game. Respond only by calling tools, never with prose.

How a level plays: the player sees a bank of eight letters and an empty crossword. They spell words from the bank, using each letter no more often than it appears in the bank, and each correct answer fills in its place in the crossword. The level ends when the player has found all ten answers, so every answer must be a word a typical player would think of.

The crossword is laid out automatically. Each answer is placed across or down and must cross an answer already placed, on a shared letter. Answers can't touch side by side, and the cell before and after each answer must be empty. Answers that share letters with many others are easiest to connect.

How to build the campaign:
1. Pick an eight-letter anchor word for each level; the level's letter bank is exactly the anchor's letters, and the anchor is one of its answers. suggest_anchors lists common anchors whose letters differ from the levels you have already saved. Prefer those, so levels don't keep producing the same short words.
2. Call find_words with the letter bank. It returns every common word the bank can spell, leaving out answers from recent campaigns and showing which words a saved level already uses. Choose answers only from that list.
3. Save the level with set_level. It checks everything, including whether the crossword connects and whether an answer repeats another level, and lists every problem at once. A level is saved only when it passes. To change a saved level, set the same level number again.
4. When every level is saved, call submit_campaign with the campaign description.

You can make several tool calls in one turn; they run in the order you make them.`

// campaignSpec is everything that differs between the kinds of campaign the
// generator can produce. The tools and level checks are shared.
type campaignSpec struct {
	levelCount int
	prompt     func(workflows.CampaignRequest) string
	// level applies the kind's timing, scoring, and hint rules to a checked level.
	level      func(game.LevelDefinition) game.LevelDefinition
	definition func(input workflows.CampaignRequest, description string) campaign.Definition
}

func campaignSpecFor(kind campaign.Kind) (campaignSpec, error) {
	switch kind {
	case campaign.KindDailyChallenge:
		return dailyChallengeSpec, nil
	case campaign.KindMonthlyPack:
		return monthlyPackSpec, nil
	default:
		return campaignSpec{}, fmt.Errorf("unsupported campaign kind %q", kind)
	}
}

var dailyChallengeSpec = campaignSpec{
	levelCount: 3,
	prompt:     dailyChallengePrompt,
	level: func(level game.LevelDefinition) game.LevelDefinition {
		level.TimeLimitSeconds = 180
		level.BasePoints = 20
		level.HintPrices = game.HintPrices{Letter: 15, Brush: 30, Word: 45}
		return level
	},
	definition: func(input workflows.CampaignRequest, description string) campaign.Definition {
		return campaign.Definition{
			ID:            workflows.GeneratedCampaignID(campaign.KindDailyChallenge, input.Date),
			Kind:          campaign.KindDailyChallenge,
			SingleAttempt: true,
			Game:          wordflowGameSummary,
			Title:         "Daily Challenge · " + input.StartsAt.Format("January 2"),
			Description:   description,
			StartsAt:      &input.StartsAt,
			EndsAt:        input.EndsAt,
			Unlock:        campaign.UnlockPolicy{InitialLevels: 3},
		}
	},
}

// A monthly pack is a permanent campaign. It keeps the default scoring and
// has no time limit, so it reads as a relaxed counterpart to the daily challenge.
var monthlyPackSpec = campaignSpec{
	levelCount: 9,
	prompt:     monthlyPackPrompt,
	level:      func(level game.LevelDefinition) game.LevelDefinition { return level },
	definition: func(input workflows.CampaignRequest, description string) campaign.Definition {
		return campaign.Definition{
			ID:          workflows.GeneratedCampaignID(campaign.KindMonthlyPack, input.Date),
			Kind:        campaign.KindMonthlyPack,
			Game:        wordflowGameSummary,
			Title:       "Monthly Pack · " + input.StartsAt.Format("January 2006"),
			Description: description,
			StartsAt:    &input.StartsAt,
			Unlock:      campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour},
		}
	},
}

var wordflowGameSummary = campaign.GameSummary{
	ID: campaign.GameWordflow, Title: "Wordflow",
	Description: "Resolve connected words from a shared letter queue.",
}

const levelRules = `For every level:
- the letter bank is exactly the letters of one common eight-letter anchor word, and the anchor is one of the ten answers; the anchor should be a standalone word, not a plural or a verb form such as an -ED or -ING word
- choose exactly ten answers of three to eight letters, all from find_words for that bank
- don't pad the answers with both a word and its simple plural or past tense
- no answer may appear in two levels
- keep every answer family-friendly
- give the level a short, energetic title`

func exclusionRule(excluded []string, scope string) string {
	if len(excluded) == 0 {
		return "There are no recent answers to exclude."
	}
	return "find_words already leaves out the answers from " + scope + ", and set_level rejects them."
}

func dailyChallengePrompt(input workflows.CampaignRequest) string {
	return fmt.Sprintf(`Create the Daily Wordflow Challenge for %s.

Build exactly three especially difficult but fair levels: favor longer and less obvious answers from find_words over the easiest three- and four-letter words. %s

%s

The campaign is one attempt only. All three levels unlock immediately but must be played sequentially. Each level has a hard 180-second limit, awards 20 base points, and prices letter, brush, and word hints at 15, 30, and 45 points. Its active window is %s through %s. Mention the high difficulty, time pressure, richer rewards, premium hints, and single attempt in one concise campaign description.

Finish by calling submit_campaign.`,
		input.Date,
		levelRules,
		exclusionRule(input.ExcludedWords, "the previous ten daily challenges"),
		input.StartsAt.Format("2006-01-02 15:04 MST"),
		input.EndsAt.Format("2006-01-02 15:04 MST"),
	)
}

func monthlyPackPrompt(input workflows.CampaignRequest) string {
	return fmt.Sprintf(`Create the Wordflow Monthly Pack for %s.

Build exactly nine levels that share one fun, family-friendly theme you choose for the month; level titles carry the theme. Order them from approachable to challenging, so the first three suit a newcomer and the last three need real vocabulary. %s

%s

The pack is permanent: players can work through it at their own pace with no time limit and the standard scoring. Mention the theme and the progression in one concise campaign description.

Finish by calling submit_campaign.`,
		input.StartsAt.Format("January 2006"),
		levelRules,
		exclusionRule(input.ExcludedWords, "the previous monthly packs"),
	)
}

// takenAnswers maps every answer a level may not use to where it was used:
// recent campaigns, and each saved level other than skipLevel.
func takenAnswers(request workflows.CampaignRequest, draft workflows.CampaignDraft, skipLevel int) map[string]string {
	taken := make(map[string]string, len(request.ExcludedWords))
	for _, answer := range request.ExcludedWords {
		taken[strings.ToUpper(strings.TrimSpace(answer))] = "a recent campaign"
	}
	for number, level := range draft {
		if number == skipLevel {
			continue
		}
		for _, answer := range level.Words {
			taken[answer] = fmt.Sprintf("level %d", number)
		}
	}
	return taken
}

// checkLevel normalizes a level and lists everything wrong with it, in a form
// the agent can act on. The crossword layout is checked only when nothing else
// is wrong, because it depends on the final answers.
func checkLevel(words *dictionary.Dictionary, level workflows.CampaignLevel, taken map[string]string) (workflows.CampaignLevel, []string) {
	level.Title = strings.TrimSpace(level.Title)
	level.Letters = strings.ToUpper(strings.TrimSpace(level.Letters))
	answers := make([]string, len(level.Words))
	for index, value := range level.Words {
		answers[index] = strings.ToUpper(strings.TrimSpace(value))
	}
	level.Words = answers

	var problems []string
	if level.Title == "" {
		problems = append(problems, "the level needs a title")
	}
	if len(level.Letters) != game.MaxWordflowLetters || !isUppercaseLetters(level.Letters) {
		// Without a valid bank nothing else can be judged.
		return level, append(problems, fmt.Sprintf("letters must be exactly %d letters A-Z", game.MaxWordflowLetters))
	}
	if len(answers) != levelAnswerCount {
		problems = append(problems, fmt.Sprintf("the level needs exactly %d answers, not %d", levelAnswerCount, len(answers)))
	}

	seen := map[string]bool{}
	hasAnchor := false
	for _, answer := range answers {
		switch {
		case len(answer) < 3 || len(answer) > game.MaxWordflowLetters || !isUppercaseLetters(answer):
			problems = append(problems, fmt.Sprintf("%q must be 3 to %d letters A-Z", answer, game.MaxWordflowLetters))
			continue
		case seen[answer]:
			problems = append(problems, fmt.Sprintf("%s appears twice", answer))
			continue
		}
		seen[answer] = true
		switch {
		case !dictionary.CanSpell(answer, level.Letters):
			problems = append(problems, fmt.Sprintf("%s cannot be spelled from %s", answer, level.Letters))
		case !words.IsEnglish(answer):
			problems = append(problems, fmt.Sprintf("%s is not an English word", answer))
		case !words.IsCommon(answer):
			problems = append(problems, fmt.Sprintf("%s is too obscure for players to find; use a common word from find_words", answer))
		case taken[answer] != "":
			problems = append(problems, fmt.Sprintf("%s is already used in %s", answer, taken[answer]))
		}
		hasAnchor = hasAnchor || len(answer) == game.MaxWordflowLetters
	}
	if !hasAnchor {
		problems = append(problems, fmt.Sprintf("no answer uses all %d letters; include the anchor word", game.MaxWordflowLetters))
	}
	for _, answer := range answers {
		if seen[answer+"S"] || seen[answer+"ES"] {
			problems = append(problems, fmt.Sprintf("%s and its plural are both answers; drop one", answer))
		}
		pastTense := answer + "ED"
		if strings.HasSuffix(answer, "E") {
			pastTense = answer + "D"
		}
		if seen[pastTense] {
			problems = append(problems, fmt.Sprintf("%s and its past tense are both answers; drop one", answer))
		}
	}

	if len(problems) == 0 {
		if _, err := game.BuildPuzzle(1, levelDefinition(level)); err != nil {
			problems = append(problems, "the answers cannot be laid out as one connected crossword; swap one or two answers for words that share more letters with the others")
		}
	}
	return level, problems
}

func levelDefinition(level workflows.CampaignLevel) game.LevelDefinition {
	return game.LevelDefinition{Title: level.Title, Letters: level.Letters, Words: level.Words}
}

// buildCampaign checks the whole draft again and turns it into the campaign to
// start. Problems are the agent's to fix; an error is a request the agent
// cannot fix.
func buildCampaign(words *dictionary.Dictionary, spec campaignSpec, request workflows.CampaignRequest, draft workflows.CampaignDraft, description string) (workflows.WordflowCampaignWorkflowInput, []string, error) {
	if request.Date == "" || request.StartsAt.IsZero() || (request.EndsAt != nil && !request.EndsAt.After(request.StartsAt)) {
		return workflows.WordflowCampaignWorkflowInput{}, nil, temporal.NewNonRetryableApplicationError(
			"campaign date and a valid active window are required", "invalid_campaign_input", nil,
		)
	}

	var problems []string
	description = strings.TrimSpace(description)
	if description == "" {
		problems = append(problems, "the campaign needs a description")
	}
	if missing := missingLevels(spec, draft); len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("levels %v are not saved yet; save them with set_level", missing))
	}
	if len(problems) > 0 {
		return workflows.WordflowCampaignWorkflowInput{}, problems, nil
	}

	// Each level is checked against recent campaigns and the levels before it,
	// so an answer used twice is reported once, on its later level.
	taken := takenAnswers(request, nil, 0)
	levels := make([]game.Puzzle, 0, spec.levelCount)
	for number := 1; number <= spec.levelCount; number++ {
		level, levelProblems := checkLevel(words, draft[number], taken)
		for _, problem := range levelProblems {
			problems = append(problems, fmt.Sprintf("level %d: %s", number, problem))
		}
		for _, answer := range level.Words {
			taken[answer] = fmt.Sprintf("level %d", number)
		}
		if len(levelProblems) > 0 {
			continue
		}
		puzzle, err := game.BuildPuzzle(number, spec.level(levelDefinition(level)))
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, nil, fmt.Errorf("build checked level %d: %w", number, err)
		}
		levels = append(levels, puzzle)
	}
	if len(problems) > 0 {
		return workflows.WordflowCampaignWorkflowInput{}, problems, nil
	}

	return workflows.WordflowCampaignWorkflowInput{
		Definition: spec.definition(request, description),
		Levels:     levels,
	}, nil, nil
}

func savedLevels(draft workflows.CampaignDraft) []int {
	numbers := make([]int, 0, len(draft))
	for number := range draft {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	return numbers
}

func missingLevels(spec campaignSpec, draft workflows.CampaignDraft) []int {
	missing := []int{}
	for number := 1; number <= spec.levelCount; number++ {
		if _, ok := draft[number]; !ok {
			missing = append(missing, number)
		}
	}
	return missing
}

func isUppercaseLetters(value string) bool {
	for _, letter := range value {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return value != ""
}
