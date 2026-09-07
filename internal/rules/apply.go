package rules

import (
	"context"
	"fmt"
	"strings"

	"github.com/riverr4t/weir/internal/apps/arr"
)

// editor endpoints per scope: the arr's bulk edit, and the id field it wants.
var editors = map[Scope][2]string{
	Movie:  {"/movie/editor", "movieIds"},
	Series: {"/series/editor", "seriesIds"},
	Album:  {"/artist/editor", "artistIds"}, // album scope items are artists
}

// TagLabel is the arr tag written for a rule tag. The arrs accept only
// [a-z0-9-] in labels, so "Stale Movies" becomes "weir-stale-movies".
func TagLabel(tag string) string {
	var b strings.Builder
	b.WriteString("weir-")
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(tag)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// EnsureTag returns the id of the arr tag with this label, creating it if needed.
func EnsureTag(ctx context.Context, c *arr.Client, label string) (int64, error) {
	tags, err := c.Tags(ctx)
	if err != nil {
		return 0, err
	}
	for _, t := range tags {
		if strings.EqualFold(t.Label, label) {
			return t.ID, nil
		}
	}
	var created arr.Tag
	if err := c.Post(ctx, "/tag", map[string]any{"label": label}, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// ApplyTag adds the tag to matched items and removes it from items that no
// longer match. This is the only arr write the rule engine makes.
func ApplyTag(ctx context.Context, c *arr.Client, scope Scope, label string, all []Item, matched []Match) (int, error) {
	ed, ok := editors[scope]
	if !ok {
		return 0, fmt.Errorf("tags are not supported for %s (Readarr keeps tags on authors)", scope)
	}
	id, err := EnsureTag(ctx, c, label)
	if err != nil {
		return 0, err
	}
	want := map[int64]bool{}
	for _, m := range matched {
		want[m.Item.ID] = true
	}
	var add, remove []int64
	for _, it := range all {
		has := it.hasTag(label)
		switch {
		case want[it.ID] && !has:
			add = append(add, it.ID)
		case !want[it.ID] && has:
			remove = append(remove, it.ID)
		}
	}
	if len(add) > 0 {
		if err := c.Put(ctx, ed[0], map[string]any{ed[1]: add, "tags": []int64{id}, "applyTags": "add"}, nil); err != nil {
			return 0, err
		}
	}
	if len(remove) > 0 {
		if err := c.Put(ctx, ed[0], map[string]any{ed[1]: remove, "tags": []int64{id}, "applyTags": "remove"}, nil); err != nil {
			return len(add), err
		}
	}
	return len(add), nil
}
