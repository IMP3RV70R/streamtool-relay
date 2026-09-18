package application

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"time"
)

const maxFallbackBytes = 50 << 20

func (a *API) userFallback(w http.ResponseWriter, r *http.Request, source string) {
	ctx := r.Context()
	if r.Method == http.MethodGet {
		var kind string
		var size, generation int64
		err := a.Store.Pool.QueryRow(ctx, `SELECT kind,octet_length(content),generation FROM source_fallback_assets WHERE source_id=?1`, source).Scan(&kind, &size, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, 200, nil)
			return
		}
		if err != nil {
			problem(w, 503, "fallback unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"kind": kind, "bytes": size, "generation": generation})
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		problem(w, 405, "method not allowed")
		return
	}
	generation, err := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
	if err != nil || generation < 0 {
		problem(w, 400, "invalid generation")
		return
	}
	var content []byte
	var kind string
	if r.Method == http.MethodPut {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
		content, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxFallbackBytes))
		if err != nil {
			problem(w, 413, "fallback exceeds 50 MB")
			return
		}
		kind, content, err = validateFallback(content, r.Header.Get("Content-Type"))
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "fallback unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM streams WHERE id=?1`, source); err != nil {
		problem(w, 503, "fallback unavailable")
		return
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stream_sessions WHERE stream_id=?1 AND phase NOT IN ('ENDED','FAILED')) OR EXISTS(SELECT 1 FROM ingest_connections WHERE stream_id=?1 AND status='CONNECTED')`, source).Scan(&active); err != nil {
		problem(w, 503, "fallback unavailable")
		return
	}
	if active {
		problem(w, 409, "stop and disconnect source before changing fallback")
		return
	}
	var current int64
	err = tx.QueryRow(ctx, `SELECT generation FROM source_fallback_assets WHERE source_id=?1`, source).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "fallback unavailable")
		return
	}
	if current != generation {
		problem(w, 409, "fallback changed; reload")
		return
	}
	if r.Method == http.MethodDelete {
		_, err = tx.Exec(ctx, `UPDATE source_fallback_assets SET kind='default',content=X'',digest='',generation=generation+1 WHERE source_id=?1`, source)
	} else {
		digest := sha256.Sum256(content)
		_, err = tx.Exec(ctx, `INSERT INTO source_fallback_assets(source_id,kind,content,digest,generation) VALUES(?1,?2,?3,?4,?5) ON CONFLICT(source_id) DO UPDATE SET kind=EXCLUDED.kind,content=EXCLUDED.content,digest=EXCLUDED.digest,generation=EXCLUDED.generation`, source, kind, content, hex.EncodeToString(digest[:]), generation+1)
	}
	if err != nil {
		problem(w, 503, "fallback unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_records(account_id,action,resource_type,resource_id) SELECT account_id,'UPDATE_FALLBACK','stream',id FROM streams WHERE id=?1`, source); err != nil || tx.Commit(ctx) != nil {
		problem(w, 503, "fallback unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateFallback(data []byte, mime string) (string, []byte, error) {
	if mime == "image/png" || mime == "image/jpeg" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 2073600 {
			return "", nil, errors.New("image must be PNG/JPEG with at most 2073600 pixels")
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", nil, errors.New("invalid image")
		}
		var out bytes.Buffer
		if err = png.Encode(&out, img); err != nil {
			return "", nil, err
		}
		return "image", out.Bytes(), nil
	}
	if mime != "video/mp4" {
		return "", nil, errors.New("use PNG, JPEG or MP4 with H.264 video")
	}
	if err := validateFallbackMP4(data); err != nil {
		return "", nil, err
	}
	return "video", data, nil
}

// Only bounded, ordinary MP4/H.264 clips are accepted. Container metadata is
// checked before storage; decoding is additionally isolated by worker limits and
// bounded decoder resources. Audio is ignored and replaced with generated silence.
func validateFallbackMP4(data []byte) error {
	bad := errors.New("MP4 must contain one 8-bit 4:2:0 H.264 video (Baseline/Main/High), at most 1080p and 30 seconds")
	var duration bool
	var videos int
	var boxes int
	var walk func([]byte, int) error
	walk = func(d []byte, depth int) error {
		if depth > 6 {
			return bad
		}
		for len(d) > 0 {
			boxes++
			if boxes > 10000 || len(d) < 8 {
				return bad
			}
			size := uint64(binary.BigEndian.Uint32(d))
			header := 8
			if size == 1 {
				if len(d) < 16 {
					return bad
				}
				size = binary.BigEndian.Uint64(d[8:])
				header = 16
			}
			if size == 0 {
				size = uint64(len(d))
			}
			if size < uint64(header) || size > uint64(len(d)) {
				return bad
			}
			tag := string(d[4:8])
			body := d[header:int(size)]
			switch tag {
			case "moov", "trak", "mdia", "minf", "stbl":
				if err := walk(body, depth+1); err != nil {
					return err
				}
			case "hdlr":
				// Caption/text handlers can invoke independent native parsers even
				// when a crafted sample description claims to be AVC. The fallback
				// contract admits video and optional audio, not ancillary tracks.
				if len(body) < 24 || body[0] != 0 {
					return bad
				}
				handler := string(body[8:12])
				if handler != "vide" && handler != "soun" {
					return bad
				}
			case "mvhd":
				if len(body) < 20 {
					return bad
				}
				var scale uint32
				var length uint64
				if body[0] == 0 {
					scale = binary.BigEndian.Uint32(body[12:])
					length = uint64(binary.BigEndian.Uint32(body[16:]))
				} else if body[0] == 1 && len(body) >= 32 {
					scale = binary.BigEndian.Uint32(body[20:])
					length = binary.BigEndian.Uint64(body[24:])
				} else {
					return bad
				}
				if scale == 0 || length == 0 || length > uint64(scale)*30 {
					return bad
				}
				duration = true
			case "stsd":
				if len(body) < 8 || binary.BigEndian.Uint32(body[4:]) != 1 {
					return bad
				}
				entry := body[8:]
				if len(entry) < 8 {
					return bad
				}
				codec := string(entry[4:8])
				if codec == "avc1" || codec == "avc3" {
					if len(entry) < 86 || int(binary.BigEndian.Uint32(entry)) != len(entry) {
						return bad
					}
					w, h := int(binary.BigEndian.Uint16(entry[32:])), int(binary.BigEndian.Uint16(entry[34:]))
					if w <= 0 || h <= 0 || w > 1920 || h > 1920 || w*h > 2073600 {
						return bad
					}
					if !supportedAVCConfiguration(entry[86:]) {
						return bad
					}
					videos++
				} else if codec != "mp4a" {
					return bad
				}
			}
			d = d[int(size):]
		}
		return nil
	}
	if err := walk(data, 0); err != nil {
		return err
	}
	if !duration || videos != 1 {
		return bad
	}
	return nil
}

// OpenH264 supports ordinary 8-bit 4:2:0 profiles used by Twitch. Reject
// High 10/4:2:2/4:4:4 clips before they can fail an on-air worker.
func supportedAVCConfiguration(children []byte) bool {
	found := false
	for len(children) > 0 {
		if len(children) < 8 {
			return false
		}
		size := int(binary.BigEndian.Uint32(children))
		if size < 8 || size > len(children) {
			return false
		}
		if string(children[4:8]) == "avcC" {
			if found {
				return false
			}
			found = true
			config := children[8:size]
			if len(config) < 7 || config[0] != 1 || (config[1] != 66 && config[1] != 77 && config[1] != 100) {
				return false
			}
			profile := config[1]
			count := int(config[5] & 31)
			if count == 0 {
				return false
			}
			config = config[6:]
			for i := 0; i < count; i++ {
				if len(config) < 2 {
					return false
				}
				n := int(binary.BigEndian.Uint16(config))
				config = config[2:]
				if n < 4 || n > len(config) || config[0]&31 != 7 || config[1] != profile {
					return false
				}
				config = config[n:]
			}
			if len(config) < 1 || config[0] == 0 {
				return false
			}
			count = int(config[0])
			config = config[1:]
			for i := 0; i < count; i++ {
				if len(config) < 2 {
					return false
				}
				n := int(binary.BigEndian.Uint16(config))
				config = config[2:]
				if n < 1 || n > len(config) || config[0]&31 != 8 {
					return false
				}
				config = config[n:]
			}
		}
		children = children[size:]
	}
	return found
}
