package rules

import (
	"strconv"
	"strings"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/jellyfin"
	"github.com/riverr4t/weir/internal/apps/jellyseerr"
	"github.com/riverr4t/weir/internal/apps/lidarr"
	"github.com/riverr4t/weir/internal/apps/readarr"
	"github.com/riverr4t/weir/internal/snapshot"
)

// Sources is what Collect reads; nil cells mean the app is not configured.
type Sources struct {
	Snap       *snapshot.Store
	Lidarr     *lidarr.Cells
	Readarr    *readarr.Cells
	Jellyfin   *jellyfin.Cells
	Jellyseerr *jellyseerr.Cells
}

type playIndex struct {
	byTmdb, byTvdb map[string][]PlayRecord
	inTmdb, inTvdb map[string]bool
}

func buildPlayIndex(j *jellyfin.Cells) playIndex {
	idx := playIndex{byTmdb: map[string][]PlayRecord{}, byTvdb: map[string][]PlayRecord{}, inTmdb: map[string]bool{}, inTvdb: map[string]bool{}}
	if j == nil {
		return idx
	}
	ps := j.PlayState.Get().Data
	names := map[string]string{}
	for _, u := range ps.Users {
		names[u.ID] = u.Name
	}
	for uid, items := range ps.Items {
		for _, it := range items {
			rec := PlayRecord{UserID: uid, UserName: names[uid], Played: it.UserData.Played, PlayCount: it.UserData.PlayCount, LastPlayed: it.UserData.LastPlayedDate}
			if k := it.ProviderIds["Tmdb"]; k != "" && it.Type == "Movie" {
				idx.inTmdb[k] = true
				idx.byTmdb[k] = append(idx.byTmdb[k], rec)
			}
			if k := it.ProviderIds["Tvdb"]; k != "" && it.Type == "Series" {
				idx.inTvdb[k] = true
				idx.byTvdb[k] = append(idx.byTvdb[k], rec)
			}
		}
	}
	return idx
}

// requesters maps tmdb/tvdb ids to the Jellyfin user id of the requester.
func requesters(j *jellyseerr.Cells) (movie, tv map[int64]string) {
	movie, tv = map[int64]string{}, map[int64]string{}
	if j == nil {
		return
	}
	for _, r := range j.Recent.Get().Data {
		if r.RequestedBy.JellyfinUserID == "" {
			continue
		}
		if r.Type == "movie" {
			movie[r.Media.TmdbID] = r.RequestedBy.JellyfinUserID
		} else {
			tv[r.Media.TvdbID] = r.RequestedBy.JellyfinUserID
		}
	}
	return
}

func tagNames(all []arr.Tag, ids []int64) []string {
	var out []string
	for _, id := range ids {
		for _, t := range all {
			if t.ID == id {
				out = append(out, t.Label)
			}
		}
	}
	return out
}

// Collect builds the scope's items from the snapshot.
func Collect(src Sources, scope Scope) []Item {
	s := src.Snap
	idx := buildPlayIndex(src.Jellyfin)
	reqMovie, reqTV := requesters(src.Jellyseerr)
	var out []Item
	switch scope {
	case Movie:
		tags := s.Radarr.Tags.Get().Data
		for _, m := range s.RadarrMovies.Get().Data {
			k := strconv.FormatInt(m.TmdbID, 10)
			out = append(out, Item{ID: m.ID, Title: m.Title, Path: m.Path, Size: m.SizeOnDisk, Added: m.Added, Monitored: m.Monitored,
				HasFile: m.HasFile, Tags: tagNames(tags, m.Tags), InJellyfin: idx.inTmdb[k], Play: idx.byTmdb[k], RequestedBy: reqMovie[m.TmdbID]})
		}
	case Series:
		tags := s.Sonarr.Tags.Get().Data
		for _, sr := range s.SonarrSeries.Get().Data {
			k := strconv.FormatInt(sr.TvdbID, 10)
			complete := sr.Statistics.EpisodeCount > 0 && sr.Statistics.EpisodeFileCount >= sr.Statistics.EpisodeCount
			out = append(out, Item{ID: sr.ID, Title: sr.Title, Path: sr.Path, Size: sr.Statistics.SizeOnDisk, Added: sr.Added, Monitored: sr.Monitored,
				HasFile: complete, Ended: sr.Ended || strings.EqualFold(sr.Status, "ended"), Tags: tagNames(tags, sr.Tags), InJellyfin: idx.inTvdb[k], Play: idx.byTvdb[k], RequestedBy: reqTV[sr.TvdbID]})
		}
	case Album:
		if src.Lidarr == nil {
			return nil
		}
		tags := s.Lidarr.Tags.Get().Data
		for _, a := range src.Lidarr.Artists.Get().Data {
			out = append(out, Item{ID: a.ID, Title: a.Name, Path: a.Path, Size: a.Statistics.SizeOnDisk, Added: a.Added, Monitored: a.Monitored,
				HasFile: a.Statistics.TrackFileCount > 0, Tags: tagNames(tags, a.Tags)})
		}
	case Book:
		if src.Readarr == nil {
			return nil
		}
		tags := s.Readarr.Tags.Get().Data
		authorTags := map[int64][]int64{}
		for _, a := range src.Readarr.Authors.Get().Data {
			authorTags[a.ID] = a.Tags
		}
		for _, b := range src.Readarr.Books.Get().Data {
			out = append(out, Item{ID: b.ID, Title: b.Title + " — " + b.AuthorTitle, Size: b.Statistics.SizeOnDisk, Added: b.Added, Monitored: b.Monitored,
				HasFile: b.Statistics.BookFileCount > 0, Tags: tagNames(tags, authorTags[b.AuthorID])})
		}
	}
	return out
}
