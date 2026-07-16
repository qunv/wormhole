package workspace

import (
	"path"
	"regexp"
	"strings"
)

type regexLike interface {
	MatchString(string) bool
}

func compileRegex(pattern string) (regexLike, error) {
	return regexp.Compile("(?i)" + pattern)
}

func globMatch(pattern, target string) bool {
	pattern = filepathSlash(pattern)
	target = filepathSlash(target)
	if strings.Contains(pattern, "**") {
		var out strings.Builder
		out.WriteString("^")
		for i := 0; i < len(pattern); i++ {
			switch pattern[i] {
			case '*':
				if i+1 < len(pattern) && pattern[i+1] == '*' {
					out.WriteString(".*")
					i++
				} else {
					out.WriteString("[^/]*")
				}
			case '?':
				out.WriteString(".")
			default:
				out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			}
		}
		out.WriteString("$")
		return regexp.MustCompile("(?i)" + out.String()).MatchString(target)
	}
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(target))
	return err == nil && ok
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}
