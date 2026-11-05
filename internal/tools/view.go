package tools

import (
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	blockEnd  = regexp.MustCompile(`(?i)</(p|li|div)>|<br\s*/?>`)
	tag       = regexp.MustCompile(`<[^>]*>`)
	fileToken = regexp.MustCompile(`\s*/root/#file:\S+`)
	spaces    = regexp.MustCompile(`[ \t\x{a0}]+`)
	lines     = regexp.MustCompile(`\s*\n\s*`)
	fileRef   = regexp.MustCompile(`/root/#file:(\S+)|(?i:<(?:img|a)\b[^>]*?(?:src|href)="([^"]+)")`)
)

// plain turns the HTML of a card description into text, cut at limit characters.
func plain(markup string, limit int) string {
	return tidy(html.UnescapeString(tag.ReplaceAllString(blockEnd.ReplaceAllString(markup, "\n"), " ")), limit)
}

// tidy drops the file tokens of a text, squeezes spaces and empty lines, and cuts it at limit characters.
// Chat messages without textHtml, such as those with files, come as such plain text.
func tidy(text string, limit int) string {
	text = fileToken.ReplaceAllString(spaces.ReplaceAllString(text, " "), "")
	text = strings.TrimSpace(lines.ReplaceAllString(text, "\n"))
	if r := []rune(text); len(r) > limit {
		return string(r[:limit]) + fmt.Sprintf("... [truncated, %d chars]", len(r))
	}
	return text
}

// paragraphs turns plain text into the HTML YouGile keeps descriptions in: a blank line starts a
// paragraph, a line break stays one.
func paragraphs(text string) string {
	var b strings.Builder
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if p = strings.Trim(p, "\n"); p != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(p), "\n", "<br>") + "</p>")
		}
	}
	return b.String()
}

// attachments lists the file links found in the sources, each once.
func attachments(sources ...string) []string {
	found := []string{}
	for _, source := range sources {
		for _, m := range fileRef.FindAllStringSubmatch(source, -1) {
			ref := html.UnescapeString(m[1] + m[2])
			if !slices.Contains(found, ref) {
				found = append(found, ref)
			}
		}
	}
	return found
}

func iso(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z")
}
