package web

import (
	"sort"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
)

type Row struct {
	App       string  `json:"app"`
	QueueID   int64   `json:"queueId"`
	Title     string  `json:"title"`
	Hash      string  `json:"hash"`
	State     string  `json:"state"`
	Progress  float64 `json:"progress"`
	Speed     int64   `json:"speed"`
	ETA       int64   `json:"eta"`
	Strikes   int     `json:"strikes"`
	Completed bool    `json:"completed"`
	InTrouble bool    `json:"inTrouble"`
	Paused    bool    `json:"paused"`
	Message   string  `json:"message"`
	Items     int     `json:"items"` // arr queue items sharing this torrent (a season pack is many episodes)
}

var arrOrder = []string{"radarr", "sonarr", "lidarr", "readarr"}

func rows(s *snapshot.Store, strikes []store.Strike) []Row {
	q := s.Qbit.Get().Data
	byHash := map[string]int{}
	for _, st := range strikes {
		byHash[st.DownloadID] += st.Count
	}
	var out []Row
	index := map[string]int{} // app+hash -> row
	for _, app := range arrOrder {
		cells := s.Arr(app)
		if cells == nil {
			continue
		}
		for _, it := range cells.Queue.Get().Data {
			if i, ok := index[app+"/"+it.DownloadID]; ok && it.DownloadID != "" {
				out[i].Items++
				if out[i].Message == "" && len(it.StatusMessages) > 0 && len(it.StatusMessages[0].Messages) > 0 {
					out[i].Message = it.StatusMessages[0].Messages[0]
				}
				continue
			}
			r := Row{App: app, QueueID: it.ID, Title: it.Title, Hash: it.DownloadID, Strikes: byHash[it.DownloadID], ETA: -1, Items: 1}
			if it.Size > 0 {
				r.Progress = 1 - it.SizeLeft/it.Size
			}
			r.Completed = it.Completed()
			if len(it.StatusMessages) > 0 && len(it.StatusMessages[0].Messages) > 0 {
				r.Message = it.StatusMessages[0].Messages[0]
			} else if it.ErrorMessage != "" {
				r.Message = it.ErrorMessage
			}
			if t, ok := q.Torrents[it.DownloadID]; ok {
				r.State, r.Speed, r.Progress = t.State, t.DLSpeed, t.Progress
				if looksLikeHash(r.Title) && t.Name != "" {
					r.Title = t.Name // a magnet the arr has not resolved yet
				}
				if t.ETA > 0 && t.ETA < 8640000 {
					r.ETA = t.ETA
				}
				r.Paused = t.State == "pausedDL" || t.State == "stoppedDL"
			} else {
				r.State = it.TrackedDownloadState
				if it.Protocol == "torrent" && !r.Completed {
					r.InTrouble = true
				}
			}
			switch it.TrackedDownloadState {
			case "importBlocked", "importFailed":
				r.InTrouble = true
			}
			if r.Strikes > 0 {
				r.InTrouble = true
			}
			index[app+"/"+it.DownloadID] = len(out)
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].InTrouble != out[j].InTrouble {
			return out[i].InTrouble
		}
		return out[i].Speed > out[j].Speed
	})
	return out
}

// ownerOf finds the arr queue item for a torrent hash, for the blocklist route.
func ownerOf(s *snapshot.Store, hash string) (app string, item arr.QueueItem, ok bool) {
	for _, app := range arrOrder {
		cells := s.Arr(app)
		if cells == nil {
			continue
		}
		for _, it := range cells.Queue.Get().Data {
			if it.DownloadID == hash {
				return app, it, true
			}
		}
	}
	return "", arr.QueueItem{}, false
}

func speeds(s *snapshot.Store) (dl, up int64, at time.Time) {
	r := s.Qbit.Get()
	return r.Data.DLSpeed, r.Data.UPSpeed, r.FetchedAt
}

// flow maps a download rate to 0..1 for the crest animation (1 ≈ 10 MiB/s).
func flow(dl int64) float64 {
	f := float64(dl) / (10 << 20)
	if f > 1 {
		f = 1
	}
	return f
}

func looksLikeHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
