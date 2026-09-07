package arr

import (
	"strconv"
	"time"
)

type StatusMessage struct {
	Title    string   `json:"title"`
	Messages []string `json:"messages"`
}

type QueueItem struct {
	ID                    int64           `json:"id"`
	Title                 string          `json:"title"`
	Status                string          `json:"status"`
	TrackedDownloadStatus string          `json:"trackedDownloadStatus"`
	TrackedDownloadState  string          `json:"trackedDownloadState"`
	DownloadID            string          `json:"downloadId"`
	Size                  float64         `json:"size"`
	SizeLeft              float64         `json:"sizeleft"`
	Protocol              string          `json:"protocol"`
	Indexer               string          `json:"indexer"`
	ErrorMessage          string          `json:"errorMessage"`
	MovieID               int64           `json:"movieId"`
	SeriesID              int64           `json:"seriesId"`
	EpisodeID             int64           `json:"episodeId"`
	AlbumID               int64           `json:"albumId"`
	BookID                int64           `json:"bookId"`
	Added                 time.Time       `json:"added"`
	StatusMessages        []StatusMessage `json:"statusMessages"`
}

// Completed reports whether every byte has arrived; such items are never
// touched by the cleaner (spec §7).
func (q QueueItem) Completed() bool { return q.Size > 0 && q.SizeLeft <= 0 }

// TitleKey identifies the media item the download is for (per-title cap).
func (q QueueItem) TitleKey() string {
	switch {
	case q.EpisodeID != 0:
		return "episode:" + strconv.FormatInt(q.EpisodeID, 10)
	case q.MovieID != 0:
		return "movie:" + strconv.FormatInt(q.MovieID, 10)
	case q.AlbumID != 0:
		return "album:" + strconv.FormatInt(q.AlbumID, 10)
	case q.BookID != 0:
		return "book:" + strconv.FormatInt(q.BookID, 10)
	}
	return "download:" + q.DownloadID
}

type HealthItem struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

type SystemStatus struct {
	AppName   string    `json:"appName"`
	Version   string    `json:"version"`
	Branch    string    `json:"branch"`
	StartTime time.Time `json:"startTime"`
}

type DiskSpace struct {
	Path       string `json:"path"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
}

type CalendarItem struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	AirDate       string `json:"airDate"`
	ReleaseDate   string `json:"releaseDate"`
	HasFile       bool   `json:"hasFile"`
	Monitored     bool   `json:"monitored"`
	SeriesID      int64  `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	SeriesTitle   string `json:"-"`
	Year          int    `json:"year"`
}

type Image struct {
	CoverType string `json:"coverType"`
	RemoteURL string `json:"remoteUrl"`
}

type Movie struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Year       int       `json:"year"`
	TmdbID     int64     `json:"tmdbId"`
	ImdbID     string    `json:"imdbId"`
	Added      time.Time `json:"added"`
	HasFile    bool      `json:"hasFile"`
	Monitored  bool      `json:"monitored"`
	SizeOnDisk int64     `json:"sizeOnDisk"`
	Tags       []int64   `json:"tags"`
	Path       string    `json:"path"`
	Images     []Image   `json:"images"`
	Overview   string    `json:"overview"`
}

type Series struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Year       int       `json:"year"`
	TvdbID     int64     `json:"tvdbId"`
	Added      time.Time `json:"added"`
	Ended      bool      `json:"ended"`
	Status     string    `json:"status"`
	Monitored  bool      `json:"monitored"`
	Tags       []int64   `json:"tags"`
	Path       string    `json:"path"`
	Images     []Image   `json:"images"`
	Overview   string    `json:"overview"`
	Statistics struct {
		SizeOnDisk        int64   `json:"sizeOnDisk"`
		EpisodeFileCount  int     `json:"episodeFileCount"`
		EpisodeCount      int     `json:"episodeCount"`
		TotalEpisodeCount int     `json:"totalEpisodeCount"`
		PercentOfEpisodes float64 `json:"percentOfEpisodes"`
	} `json:"statistics"`
}

type Tag struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type Episode struct {
	ID            int64  `json:"id"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title"`
	AirDate       string `json:"airDate"`
	HasFile       bool   `json:"hasFile"`
	Monitored     bool   `json:"monitored"`
}

// Wanted is one page of wanted/missing or wanted/cutoff.
type Wanted struct {
	Page         int          `json:"page"`
	PageSize     int          `json:"pageSize"`
	TotalRecords int          `json:"totalRecords"`
	Records      []WantedItem `json:"records"`
}

type WantedItem struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	SeriesID      int64  `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	AirDate       string `json:"airDate"`
	Series        *struct {
		Title string `json:"title"`
	} `json:"series"`
}
