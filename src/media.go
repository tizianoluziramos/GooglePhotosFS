package main

// Media keeps the library fields used by the mobile API and its SQLite index.
type Media struct {
	Key          string
	Name         string
	Dedup        string
	Canonical    bool
	Kind         int64
	Caption      string
	Collection   string
	Size         int64
	Quota        int64
	Origin       string
	Version      int64
	Taken        int64
	Uploaded     int64
	Zone         int64
	Width        int64
	Height       int64
	RemoteURL    string
	UploadStatus int64
	Trashed      int64
	Archived     bool
	Favorite     bool
	Locked       bool
	Original     bool
	Latitude     float64
	Longitude    float64
	Location     string
	LocationID   string
	Edited       bool
	Make         string
	Model        string
	Aperture     float64
	Shutter      float64
	ISO          int64
	Focal        float64
	Duration     int64
	CaptureFPS   float64
	EncodedFPS   float64
	Motion       bool
	MotionWidth  int64
	MotionHeight int64
	SHA1         string
}

var mediaColumns = []string{
	"media_key",
	"file_name",
	"dedup_key",
	"is_canonical",
	"type",
	"caption",
	"collection_id",
	"size_bytes",
	"quota_charged_bytes",
	"origin",
	"content_version",
	"utc_timestamp",
	"server_creation_timestamp",
	"timezone_offset",
	"width",
	"height",
	"remote_url",
	"upload_status",
	"trash_timestamp",
	"is_archived",
	"is_favorite",
	"is_locked",
	"is_original_quality",
	"latitude",
	"longitude",
	"location_name",
	"location_id",
	"is_edited",
	"make",
	"model",
	"aperture",
	"shutter_speed",
	"iso",
	"focal_length",
	"duration",
	"capture_frame_rate",
	"encoded_frame_rate",
	"is_micro_video",
	"micro_video_width",
	"micro_video_height",
	"sha1_hash",
}

func (m *Media) destinations() []any {
	return []any{
		&m.Key,
		&m.Name,
		&m.Dedup,
		&m.Canonical,
		&m.Kind,
		&m.Caption,
		&m.Collection,
		&m.Size,
		&m.Quota,
		&m.Origin,
		&m.Version,
		&m.Taken,
		&m.Uploaded,
		&m.Zone,
		&m.Width,
		&m.Height,
		&m.RemoteURL,
		&m.UploadStatus,
		&m.Trashed,
		&m.Archived,
		&m.Favorite,
		&m.Locked,
		&m.Original,
		&m.Latitude,
		&m.Longitude,
		&m.Location,
		&m.LocationID,
		&m.Edited,
		&m.Make,
		&m.Model,
		&m.Aperture,
		&m.Shutter,
		&m.ISO,
		&m.Focal,
		&m.Duration,
		&m.CaptureFPS,
		&m.EncodedFPS,
		&m.Motion,
		&m.MotionWidth,
		&m.MotionHeight,
		&m.SHA1,
	}
}

func (m Media) fields() []any {
	out := m.destinations()
	for i, d := range out {
		switch p := d.(type) {
		case *string:
			out[i] = *p
		case *int64:
			out[i] = *p
		case *float64:
			out[i] = *p
		case *bool:
			out[i] = *p
		}
	}
	return out
}
