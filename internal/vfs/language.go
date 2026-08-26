package vfs

import (
	"path"
	"strings"
)

// defaultLanguage is what the editor falls back to when nothing matches.
const defaultLanguage = "plaintext"

// languageByExt maps a lower case file extension, dot included, to a Monaco
// editor language id. Ids that Monaco does not ship are avoided on purpose:
// an unknown id would silently degrade to plain text anyway, so a close
// relative is a better answer than an invented one.
var languageByExt = map[string]string{
	// Go.
	".go": "go",

	// JavaScript and TypeScript.
	".js":     "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".jsx":    "javascript",
	".ts":     "typescript",
	".mts":    "typescript",
	".cts":    "typescript",
	".tsx":    "tsx",
	".coffee": "coffeescript",

	// Python.
	".py":  "python",
	".pyw": "python",
	".pyi": "python",

	// Shells.
	".sh":   "shell",
	".bash": "shell",
	".zsh":  "shell",
	".ksh":  "shell",
	".fish": "shell",
	".ps1":  "powershell",
	".psm1": "powershell",
	".psd1": "powershell",
	".bat":  "bat",
	".cmd":  "bat",

	// Data and configuration.
	".yaml":        "yaml",
	".yml":         "yaml",
	".json":        "json",
	".jsonc":       "json",
	".json5":       "json",
	".geojson":     "json",
	".webmanifest": "json",
	".toml":        "ini",
	".ini":         "ini",
	".cfg":         "ini",
	".conf":        "ini",
	".properties":  "ini",
	".service":     "ini",
	".socket":      "ini",
	".timer":       "ini",
	".desktop":     "ini",

	// Markup and documents.
	".md":         "markdown",
	".markdown":   "markdown",
	".mdx":        "markdown",
	".rst":        "restructuredtext",
	".xml":        "xml",
	".xsd":        "xml",
	".xsl":        "xml",
	".xslt":       "xml",
	".svg":        "xml",
	".plist":      "xml",
	".csproj":     "xml",
	".rss":        "xml",
	".atom":       "xml",
	".html":       "html",
	".htm":        "html",
	".xhtml":      "html",
	".vue":        "html",
	".svelte":     "html",
	".hbs":        "handlebars",
	".handlebars": "handlebars",
	".pug":        "pug",
	".jade":       "pug",
	".twig":       "twig",
	".liquid":     "liquid",

	// Style sheets.
	".css":  "css",
	".scss": "scss",
	".sass": "scss",
	".less": "less",

	// Databases.
	".sql":   "sql",
	".pgsql": "pgsql",
	".mysql": "mysql",

	// Compiled and systems languages.
	".c":     "c",
	".h":     "c",
	".cpp":   "cpp",
	".cxx":   "cpp",
	".cc":    "cpp",
	".hpp":   "cpp",
	".hxx":   "cpp",
	".hh":    "cpp",
	".ino":   "cpp",
	".cs":    "csharp",
	".rs":    "rust",
	".java":  "java",
	".kt":    "kotlin",
	".kts":   "kotlin",
	".scala": "scala",
	".sc":    "scala",
	".swift": "swift",
	".dart":  "dart",
	".m":     "objective-c",
	".mm":    "objective-c",
	".pas":   "pascal",
	".vb":    "vb",
	".fs":    "fsharp",
	".fsx":   "fsharp",

	// Scripting languages.
	".php":     "php",
	".phtml":   "php",
	".rb":      "ruby",
	".rake":    "ruby",
	".gemspec": "ruby",
	".lua":     "lua",
	".pl":      "perl",
	".pm":      "perl",
	".r":       "r",
	".jl":      "julia",
	".ex":      "elixir",
	".exs":     "elixir",
	".clj":     "clojure",
	".cljs":    "clojure",
	".cljc":    "clojure",
	".edn":     "clojure",
	".tcl":     "tcl",
	// Groovy is not a Monaco language. Its syntax is close enough to Java that
	// Java highlighting reads correctly for build scripts and pipelines.
	".groovy": "java",
	".gradle": "java",

	// Infrastructure and interface definitions.
	".tf":            "hcl",
	".tfvars":        "hcl",
	".hcl":           "hcl",
	".proto":         "proto",
	".graphql":       "graphql",
	".gql":           "graphql",
	".sol":           "sol",
	".wgsl":          "wgsl",
	".dockerfile":    "dockerfile",
	".containerfile": "dockerfile",
}

// languageByName maps a whole lower case file name for the well known files
// that carry no extension, plus the dot files whose name is the only hint.
var languageByName = map[string]string{
	"dockerfile":    "dockerfile",
	"containerfile": "dockerfile",
	"makefile":      "shell",
	"gnumakefile":   "shell",
	"justfile":      "shell",
	"caddyfile":     "ini",
	"nginx.conf":    "ini",
	"vagrantfile":   "ruby",
	"gemfile":       "ruby",
	"rakefile":      "ruby",
	"podfile":       "ruby",
	"brewfile":      "ruby",
	"jenkinsfile":   "java",
	"go.mod":        defaultLanguage,
	"go.sum":        defaultLanguage,
	"cargo.lock":    "ini",
	"yarn.lock":     defaultLanguage,

	".env":           "ini",
	".editorconfig":  "ini",
	".gitconfig":     "ini",
	".gitignore":     "ini",
	".gitattributes": "ini",
	".gitmodules":    "ini",
	".dockerignore":  "ini",
	".npmignore":     "ini",
	".npmrc":         "ini",
	".yarnrc":        "ini",
	".htaccess":      "ini",
	".bashrc":        "shell",
	".bash_profile":  "shell",
	".bash_aliases":  "shell",
	".bash_logout":   "shell",
	".zshrc":         "shell",
	".zshenv":        "shell",
	".zprofile":      "shell",
	".profile":       "shell",
	".babelrc":       "json",
	".eslintrc":      "json",
	".prettierrc":    "json",
}

// LanguageFor maps a file name to the Monaco editor language id the built in
// editor should load. Unrecognized names fall back to "plaintext".
func LanguageFor(name string) string {
	base := strings.ToLower(path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")))
	if base == "" || base == "." || base == ".." || base == "/" {
		return defaultLanguage
	}
	if lang, ok := languageByName[base]; ok {
		return lang
	}
	if lang, ok := languageByExt[path.Ext(base)]; ok {
		return lang
	}
	// Names such as "Dockerfile.dev", "Makefile.local" or ".env.production"
	// keep their meaning in the leading token, so try that before giving up.
	if strings.HasPrefix(base, ".") {
		if rest := base[1:]; rest != "" {
			if i := strings.Index(rest, "."); i > 0 {
				if lang, ok := languageByName["."+rest[:i]]; ok {
					return lang
				}
			}
		}
		return defaultLanguage
	}
	if i := strings.Index(base, "."); i > 0 {
		if lang, ok := languageByName[base[:i]]; ok {
			return lang
		}
	}
	return defaultLanguage
}
