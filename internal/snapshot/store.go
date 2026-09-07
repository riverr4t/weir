package snapshot

import (
	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/qbit"
)

// ArrCells is the set of cells every arr app fills.
type ArrCells struct {
	Queue    Cell[[]arr.QueueItem]
	Health   Cell[[]arr.HealthItem]
	Status   Cell[arr.SystemStatus]
	Disk     Cell[[]arr.DiskSpace]
	Calendar Cell[[]arr.CalendarItem]
	Tags     Cell[[]arr.Tag]
	Missing  Cell[arr.Wanted] // first page + total
	Cutoff   Cell[arr.Wanted]
}

// Store is the whole snapshot: one Cell per (app, kind). Apps whose types
// live in their own package (lidarr, readarr, prowlarr, bazarr, jellyfin,
// jellyseerr) keep their cells there and hang them off Extra to avoid an
// import cycle; main wires them.
type Store struct {
	Radarr       ArrCells
	RadarrMovies Cell[[]arr.Movie]
	Sonarr       ArrCells
	SonarrSeries Cell[[]arr.Series]
	Lidarr       ArrCells
	Readarr      ArrCells
	Qbit         Cell[qbit.State]
	Extra        map[string]any // "lidarr" -> *lidarr.Cells etc.; set once at startup, read-only after
}

// Arr returns the shared cells for a named arr app, or nil.
func (s *Store) Arr(app string) *ArrCells {
	switch app {
	case "radarr":
		return &s.Radarr
	case "sonarr":
		return &s.Sonarr
	case "lidarr":
		return &s.Lidarr
	case "readarr":
		return &s.Readarr
	}
	return nil
}
