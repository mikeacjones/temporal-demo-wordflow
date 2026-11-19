package dictionary

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnglishAndCommonWords(t *testing.T) {
	words, err := Load()
	require.NoError(t, err)

	for _, word := range []string{"REACTION", "ALIGN", "HURDLE", "TRUDGE", "LEGIT", "SPECTRAL", "ICY"} {
		require.True(t, words.IsEnglish(word), word)
		require.True(t, words.IsCommon(word), word)
	}
	// Scrambled anchors a model invented in a failed generation run.
	for _, word := range []string{"MOLARPET", "LARGESTI", "HEADRICS", "TAILGRED", "OAID", "STER", "reaction", "RE-ACT"} {
		require.False(t, words.IsEnglish(word), word)
		require.False(t, words.IsCommon(word), word)
	}
	// Real but obscure: valid Scrabble words no casual player would find.
	for _, word := range []string{"AIRT", "ADIT", "AGER", "NITE", "URANITES", "AROINT"} {
		require.True(t, words.IsEnglish(word), word)
		require.False(t, words.IsCommon(word), word)
	}
	// SCOWL abbreviations are not in ENABLE.
	require.False(t, words.IsCommon("ABBR"))
}

func TestCommonWordsFromSpellsOnlyWithTheBank(t *testing.T) {
	words, err := Load()
	require.NoError(t, err)

	found := words.CommonWordsFrom("ACEINORT", 3)
	require.Contains(t, found, "REACTION")
	require.Contains(t, found, "RATIO")
	require.NotContains(t, found, "AIRT")
	require.NotContains(t, found, "TEETH", "needs letters the bank does not have")
	require.NotContains(t, found, "ROOT", "needs two Os and the bank has one")
	for _, word := range found {
		require.GreaterOrEqual(t, len(word), 3)
		require.True(t, CanSpell(word, "ACEINORT"), word)
		require.True(t, words.IsCommon(word), word)
	}
	require.Nil(t, words.CommonWordsFrom("acei", 3), "lowercase is not a letter bank")
}

func TestCommonWordsOfLength(t *testing.T) {
	words, err := Load()
	require.NoError(t, err)

	eights := words.CommonWordsOfLength(8)
	require.Greater(t, len(eights), 5000)
	require.True(t, slices.Contains(eights, "SPECTRAL"))
	for _, word := range eights {
		require.Len(t, word, 8)
	}
}

func TestCanSpell(t *testing.T) {
	require.True(t, CanSpell("ROLL", "ABCKLLOR"))
	require.False(t, CanSpell("ROLLL", "ABCKLLOR"))
	require.False(t, CanSpell("roll", "ABCKLLOR"))
	require.False(t, CanSpell("ROLL", "abckllor"))
}
