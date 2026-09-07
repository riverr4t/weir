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

// Store is the whole snapshot: one Cell per (app, kind). App packages add
// their typed cells here as they arrive.
type Store struct {
	Radarr       ArrCells
	RadarrMovies Cell[[]arr.Movie]
	Sonarr       ArrCells
	SonarrSeries Cell[[]arr.Series]
	Qbit         Cell[qbit.State]
}

// Arr returns the shared cells for a named arr app, or nil.
func (s *Store) Arr(app string) *ArrCells {
	switch app {
	case "radarr":
		return &s.Radarr
	case "sonarr":
		return &s.Sonarr
	}
	return nil
}
