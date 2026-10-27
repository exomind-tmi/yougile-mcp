package tools

import (
	"fmt"
	"html"
	"regexp"
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

// plain turns the HTML of a card or a chat message into text, cut at limit characters.
func plain(markup string, limit int) string {
	text := tag.ReplaceAllString(blockEnd.ReplaceAllString(markup, "\n"), " ")
	text = fileToken.ReplaceAllString(spaces.ReplaceAllString(html.UnescapeString(text), " "), "")
	text = strings.TrimSpace(lines.ReplaceAllString(text, "\n"))
	if r := []rune(text); len(r) > limit {
		return string(r[:limit]) + fmt.Sprintf("... [truncated, %d chars]", len(r))
	}
	return text
}

// attachments lists the file links found in the sources, each once.
func attachments(sources ...string) []string {
	found := []string{}
	for _, source := range sources {
		for _, m := range fileRef.FindAllStringSubmatch(source, -1) {
			ref := html.UnescapeString(m[1] + m[2])
			if !contains(found, ref) {
				found = append(found, ref)
			}
		}
	}
	return found
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func iso(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z")
}
