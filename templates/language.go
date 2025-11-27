package templates

import "strings"

// LanguageNameToCode maps English language names to ISO 639-1 codes
var LanguageNameToCode = map[string]string{
	// English variants
	"english":                "en",
	"english (uk)":           "en-GB",
	"english (us)":           "en-US",
	"english (australia)":    "en-AU",
	"english (canadian)":     "en-CA",
	"english (new zealand)":  "en-NZ",
	"english (irish)":        "en-IE",
	"english (south africa)": "en-ZA",

	// Romance languages
	"french":     "fr",
	"spanish":    "es",
	"italian":    "it",
	"romanian":   "ro",
	"portuguese": "pt",

	// Germanic languages
	"german":    "de",
	"norwegian": "no",
	"swedish":   "sv",
	"danish":    "da",
	"dutch":     "nl",

	// Slavic languages
	"russian":    "ru",
	"ukrainian":  "uk",
	"belarusian": "be",
	"polish":     "pl",
	"czech":      "cs",
	"slovak":     "sk",
	"serbian":    "sr",
	"croatian":   "hr",
	"bosnian":    "bs",
	"slovenian":  "sl",
	"macedonian": "mk",
	"bulgarian":  "bg",

	// Baltic languages
	"lithuanian": "lt",
	"latvian":    "lv",
	"estonian":   "et",

	// Other
	"finnish":   "fi",
	"greek":     "el",
	"turkish":   "tr",
	"albanian":  "sq",
	"hungarian": "hu",
}

// NativeLanguageNameToCode maps native language names to ISO 639-1 codes
var NativeLanguageNameToCode = map[string]string{
	// Slavic languages
	"русский":     "ru",
	"українська":  "uk",
	"беларуская":  "be",
	"polski":      "pl",
	"čeština":     "cs",
	"slovenčina":  "sk",
	"српски":      "sr",
	"hrvatski":    "hr",
	"bosanski":    "bs",
	"slovenščina": "sl",
	"македонски":  "mk",
	"български":   "bg",

	// Germanic languages
	"deutsch":    "de",
	"norsk":      "no",
	"svenska":    "sv",
	"dansk":      "da",
	"nederlands": "nl",

	// Romance languages
	"français":  "fr",
	"español":   "es",
	"italiano":  "it",
	"română":    "ro",
	"português": "pt",

	// Baltic languages
	"lietuvių": "lt",
	"latviešu": "lv",
	"eesti":    "et",

	// Other
	"suomi":    "fi",
	"ελληνικά": "el",
	"türkçe":   "tr",
	"shqip":    "sq",
	"magyar":   "hu",
}

// ResolveLanguage converts a language parameter to an ISO code
// Accepts: ISO codes ("uk", "ru", "en-GB"), English names ("Ukrainian", "Russian"),
// or native names ("Українська", "Русский")
func ResolveLanguage(input string) string {
	if input == "" {
		return ""
	}

	// Normalize input: trim and lowercase for comparison
	normalized := strings.ToLower(strings.TrimSpace(input))

	// First check if it's already a valid ISO code (exact match)
	if _, exists := LanguageFallback[input]; exists {
		return input
	}

	// Try normalized ISO code
	if _, exists := LanguageFallback[normalized]; exists {
		return normalized
	}

	// Try English language name
	if code, exists := LanguageNameToCode[normalized]; exists {
		return code
	}

	// Try native language name (case-sensitive for Cyrillic/Greek)
	if code, exists := NativeLanguageNameToCode[strings.TrimSpace(input)]; exists {
		return code
	}

	// Try native language name lowercase
	if code, exists := NativeLanguageNameToCode[normalized]; exists {
		return code
	}

	// If nothing matches, return the original input (will use default fallback)
	return input
}

// LanguageFallback defines the fallback chain for each supported language
var LanguageFallback = map[string][]string{
	// English variants (UK preferred in fallback)
	"en-GB": {"en-GB", "en-US", "en-AU", "en-CA", "en-NZ", "en-IE", "en-ZA", "en"},
	"en-US": {"en-US", "en-GB", "en-AU", "en-CA", "en"},
	"en-AU": {"en-AU", "en-GB", "en-US", "en"},
	"en-CA": {"en-CA", "en-GB", "en-US", "en"},
	"en-NZ": {"en-NZ", "en-GB", "en-AU", "en"},
	"en-IE": {"en-IE", "en-GB", "en"},
	"en-ZA": {"en-ZA", "en-GB", "en"},
	"en":    {"en", "en-GB"},

	// Romance languages
	"fr": {"fr", "en-GB", "en"},
	"es": {"es", "fr", "it", "en-GB", "en"},
	"it": {"it", "fr", "es", "en-GB", "en"},
	"ro": {"ro", "it", "fr", "en-GB", "en"},
	"pt": {"pt", "es", "fr", "en-GB", "en"},

	// Germanic languages
	"de": {"de", "nl", "en-GB", "en"},
	"no": {"no", "sv", "da", "en-GB", "en"},
	"sv": {"sv", "no", "da", "en-GB", "en"},
	"da": {"da", "no", "sv", "en-GB", "en"},
	"nl": {"nl", "de", "en-GB", "en"},

	// East Slavic
	"ru": {"ru", "uk", "be", "en-GB", "en"},
	"uk": {"uk", "ru", "be", "en-GB", "en"},
	"be": {"be", "ru", "uk", "en-GB", "en"},

	// West Slavic
	"pl": {"pl", "cs", "sk", "en-GB", "en"},
	"cs": {"cs", "sk", "pl", "en-GB", "en"},
	"sk": {"sk", "cs", "pl", "en-GB", "en"},

	// South Slavic (Balkan)
	"sr": {"sr", "hr", "bs", "sl", "mk", "bg", "en-GB", "en"},
	"hr": {"hr", "sr", "bs", "sl", "en-GB", "en"},
	"bs": {"bs", "sr", "hr", "sl", "en-GB", "en"},
	"sl": {"sl", "hr", "sr", "bs", "en-GB", "en"},
	"mk": {"mk", "sr", "bg", "en-GB", "en"},
	"bg": {"bg", "mk", "sr", "en-GB", "en"},

	// Baltic
	"lt": {"lt", "lv", "et", "en-GB", "en"},
	"lv": {"lv", "lt", "et", "en-GB", "en"},
	"et": {"et", "lv", "lt", "fi", "en-GB", "en"},

	// Other NATO/Western
	"fi": {"fi", "sv", "en-GB", "en"},
	"el": {"el", "en-GB", "en"}, // Greek
	"tr": {"tr", "en-GB", "en"},
	"sq": {"sq", "en-GB", "en"}, // Albanian
	"hu": {"hu", "en-GB", "en"}, // Hungarian
}

// GetLanguageFallbackChain returns the fallback chain for a language
func GetLanguageFallbackChain(lang string) []string {
	if chain, exists := LanguageFallback[lang]; exists {
		return chain
	}

	// Default fallback: try the language itself, then English (UK), then generic English
	return []string{lang, "en-GB", "en"}
}

// GetFormatString retrieves the best format string for a language
func GetFormatString(template EventTemplate, lang string) string {
	// If no language specified, use default format
	if lang == "" {
		if template.Format != "" {
			return template.Format
		}
		return ""
	}

	// Get fallback chain for this language
	chain := GetLanguageFallbackChain(lang)

	// Try each language in the fallback chain
	for _, fallbackLang := range chain {
		if template.Formats != nil {
			if format, exists := template.Formats[fallbackLang]; exists && format != "" {
				return format
			}
		}

		// Check if this is the default format language (en or en-GB)
		if (fallbackLang == "en" || fallbackLang == "en-GB") && template.Format != "" {
			return template.Format
		}
	}

	// Last resort: use generic format if exists
	if template.Format != "" {
		return template.Format
	}

	return ""
}
