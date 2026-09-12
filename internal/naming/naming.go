// Package naming turns human task titles into the identifiers dispatch,
// git and herdr each need.
package naming

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// MaxAgentNameLen is herdr's limit: [a-z][a-z0-9_-]{0,31}.
const MaxAgentNameLen = 32

// Slug renders a title as a readable, filesystem- and git-safe identifier.
func Slug(title string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII, unicode.IsDigit(r) && r < unicode.MaxASCII:
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "task"
	}
	return slug
}

// TruncateSlug shortens a slug on a word boundary where possible.
func TruncateSlug(slug string, max int) string {
	if max <= 0 || len(slug) <= max {
		return strings.Trim(slug, "-")
	}
	cut := slug[:max]
	if i := strings.LastIndexByte(cut, '-'); i > max/2 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}

// TaskID returns a sortable, collision-resistant task identifier.
//
// Time-prefixed so `ls` and the database order naturally, with random suffix
// so two dispatches in the same second never collide.
func TaskID(now time.Time) string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A failed CSPRNG read is not worth failing a dispatch over; the
		// timestamp alone still distinguishes tasks in practice.
		return fmt.Sprintf("task_%s", now.UTC().Format("20060102T150405.000000"))
	}
	return fmt.Sprintf("task_%s_%s", now.UTC().Format("20060102T150405"), hex.EncodeToString(buf[:]))
}

// BranchName builds the branch for a worktree-isolated task.
func BranchName(prefix, slug string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix != "" && !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "-") {
		prefix += "/"
	}
	return prefix + TruncateSlug(slug, 60)
}

// AgentName builds a herdr agent name, which must match [a-z][a-z0-9_-]{0,31}
// and be unique among live agents. taken reports names already in use.
func AgentName(prefix, slug string, taken func(string) bool) string {
	base := slug
	if prefix != "" {
		base = Slug(prefix) + "-" + slug
	}
	base = strings.TrimLeft(base, "-0123456789")
	if base == "" {
		base = "agent"
	}
	base = TruncateSlug(base, MaxAgentNameLen)
	if base == "" {
		base = "agent"
	}
	if taken == nil || !taken(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		suffix := fmt.Sprintf("-%d", i)
		candidate := TruncateSlug(base, MaxAgentNameLen-len(suffix)) + suffix
		if !taken(candidate) {
			return candidate
		}
	}
	// Fall back to something guaranteed distinct rather than looping forever.
	var buf [3]byte
	_, _ = rand.Read(buf[:])
	suffix := "-" + hex.EncodeToString(buf[:])
	return TruncateSlug(base, MaxAgentNameLen-len(suffix)) + suffix
}

// ValidAgentName reports whether a name satisfies herdr's constraints.
func ValidAgentName(name string) bool {
	if name == "" || len(name) > MaxAgentNameLen {
		return false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name[1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
