package dictionarymining

// EnglishFormForTest exposes englishForm to the external test package.
func EnglishFormForTest(word string, words map[string]bool) bool { return englishForm(word, words) }
