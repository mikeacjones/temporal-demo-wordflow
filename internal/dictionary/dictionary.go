// Package dictionary says which strings are English words, and which of those
// are common enough that a typical player would think of them.
//
// English words come from ENABLE, a public-domain word-game list. Common words
// are the ENABLE words that are also in SCOWL size 50, the list a standard
// spell checker uses; the intersection drops SCOWL's abbreviations and keeps
// out ENABLE's obscure entries such as AIRT and ADIT. data/README.md has the
// sources and the SCOWL copyright notice.
package dictionary

import (
	"bufio"
	"compress/gzip"
	"embed"
	"fmt"
	"strings"
	"sync"
)

//go:embed data/enable1.txt.gz data/scowl-50-us.txt.gz
var data embed.FS

// Dictionary is read-only after Load, so it is safe to share across goroutines.
type Dictionary struct {
	english map[string]bool
	common  map[string]bool
	// commonWords holds each common word with its letter counts, so finding the
	// words a letter bank can spell is a scan without allocations.
	commonWords []countedWord
}

type countedWord struct {
	word   string
	counts letterCounts
}

type letterCounts [26]uint8

// Load returns the dictionary, reading the embedded lists on the first call.
var Load = sync.OnceValues(load)

func load() (*Dictionary, error) {
	english, err := readWords("data/enable1.txt.gz", false)
	if err != nil {
		return nil, err
	}
	scowl, err := readWords("data/scowl-50-us.txt.gz", true)
	if err != nil {
		return nil, err
	}

	dictionary := &Dictionary{english: map[string]bool{}, common: map[string]bool{}}
	for _, word := range english {
		dictionary.english[word] = true
	}
	for _, word := range scowl {
		if !dictionary.english[word] || dictionary.common[word] {
			continue
		}
		dictionary.common[word] = true
		dictionary.commonWords = append(dictionary.commonWords, countedWord{word: word, counts: countLetters(word)})
	}
	return dictionary, nil
}

// readWords returns the all-lowercase alphabetic entries of a gzipped list as
// uppercase words. SCOWL files start with a notice that ends at a "---" line;
// its capitalized entries are proper nouns, which is why only lowercase lines count.
func readWords(name string, skipHeader bool) ([]string, error) {
	file, err := data.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer file.Close()
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}

	var words []string
	inHeader := skipHeader
	scanner := bufio.NewScanner(unzipped)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if inHeader {
			inHeader = line != "---"
			continue
		}
		if isLowercaseWord(line) {
			words = append(words, strings.ToUpper(line))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if inHeader || len(words) == 0 {
		return nil, fmt.Errorf("read %s: no words found", name)
	}
	return words, nil
}

func isLowercaseWord(line string) bool {
	for _, letter := range line {
		if letter < 'a' || letter > 'z' {
			return false
		}
	}
	return line != ""
}

// IsEnglish reports whether word is in ENABLE. Words are uppercase A-Z.
func (d *Dictionary) IsEnglish(word string) bool {
	return d.english[word]
}

// IsCommon reports whether word is an English word a typical player knows.
func (d *Dictionary) IsCommon(word string) bool {
	return d.common[word]
}

// CommonWordsFrom returns, in no particular order, every common word of at
// least minLength letters that letters can spell without reusing a letter more
// often than it appears.
func (d *Dictionary) CommonWordsFrom(letters string, minLength int) []string {
	available, ok := countLettersChecked(letters)
	if !ok {
		return nil
	}
	var words []string
	for _, candidate := range d.commonWords {
		if len(candidate.word) >= minLength && len(candidate.word) <= len(letters) && candidate.counts.fitsIn(available) {
			words = append(words, candidate.word)
		}
	}
	return words
}

// CommonWordsOfLength returns every common word with exactly length letters.
func (d *Dictionary) CommonWordsOfLength(length int) []string {
	var words []string
	for _, candidate := range d.commonWords {
		if len(candidate.word) == length {
			words = append(words, candidate.word)
		}
	}
	return words
}

// CanSpell reports whether word uses no letter more often than letters has it.
// Both are uppercase A-Z; anything else cannot be spelled.
func CanSpell(word, letters string) bool {
	needed, ok := countLettersChecked(word)
	if !ok {
		return false
	}
	available, ok := countLettersChecked(letters)
	return ok && needed.fitsIn(available)
}

func countLetters(word string) letterCounts {
	counts, _ := countLettersChecked(word) // dictionary words are always A-Z
	return counts
}

func countLettersChecked(word string) (letterCounts, bool) {
	var counts letterCounts
	for _, letter := range word {
		if letter < 'A' || letter > 'Z' {
			return letterCounts{}, false
		}
		counts[letter-'A']++
	}
	return counts, true
}

func (needed letterCounts) fitsIn(available letterCounts) bool {
	for index, count := range needed {
		if count > available[index] {
			return false
		}
	}
	return true
}
