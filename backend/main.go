package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tus/tusd/v2/pkg/filestore"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	maxUploadSize   = int64(5 * 1024 * 1024 * 1024) // 5 GB
	tusUploadDir    = "./temp/uploads/tus"
	tempOutputsDir  = "./temp/outputs"
	cleanupInterval = 30 * time.Minute
	fileMaxAge      = 2 * time.Hour
	backendPort     = ":8081"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type VideoInfo struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Codec   string `json:"codec"`
	BitRate string `json:"bitRate,omitempty"`
}

type ClipInfo struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"downloadUrl"`
}

type Job struct {
	ID        string     `json:"jobId"`
	Status    string     `json:"status"`  // processing | completed | failed
	Message   string     `json:"message"`
	Clips     []ClipInfo `json:"clips"`
	InputInfo *VideoInfo `json:"inputInfo,omitempty"`
	CreatedAt time.Time  `json:"-"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Job Store
// ─────────────────────────────────────────────────────────────────────────────

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

var jobStore = &JobStore{jobs: make(map[string]*Job)}

func (s *JobStore) Set(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = j
}

func (s *JobStore) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) Update(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
	}
}

func (s *JobStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

func (s *JobStore) All() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	return list
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP helpers
// ─────────────────────────────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errorResponse(w http.ResponseWriter, code int, msg string) {
	jsonResponse(w, code, map[string]string{"error": msg})
}

// ─────────────────────────────────────────────────────────────────────────────
// CORS middleware  (covers both TUS /files/ and /api/ endpoints)
// ─────────────────────────────────────────────────────────────────────────────

// corsMiddleware handles CORS for ALL endpoints.
// We intercept all OPTIONS and return 204 ourselves so tusd never sees OPTIONS.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Authorization, Content-Type, Location, "+
				"Tus-Extension, Tus-Max-Size, Tus-Resumable, Tus-Version, "+
				"Upload-Concat, Upload-Defer-Length, Upload-Length, Upload-Metadata, Upload-Offset, "+
				"X-HTTP-Method-Override, X-Requested-With")
		w.Header().Set("Access-Control-Expose-Headers",
			"Location, Upload-Offset, Upload-Length, "+
				"Tus-Version, Tus-Resumable, Tus-Max-Size, Tus-Extension, "+
				"Upload-Metadata, Upload-Defer-Length, Upload-Concat")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ffprobe – get video stream info
// ─────────────────────────────────────────────────────────────────────────────

func getVideoInfo(filePath string) (*VideoInfo, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "v:0",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("ffprobe parse error: %w", err)
	}
	if len(result.Streams) == 0 {
		return nil, fmt.Errorf("no video stream found in %s", filepath.Base(filePath))
	}
	s := result.Streams[0]
	return &VideoInfo{
		Width:   s.Width,
		Height:  s.Height,
		Codec:   s.CodecName,
		BitRate: s.BitRate,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/start  – called by frontend after TUS upload completes
// ─────────────────────────────────────────────────────────────────────────────

type StartRequest struct {
	UploadID string  `json:"uploadId"`
	Duration float64 `json:"duration"` // minutes
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Request tidak valid: "+err.Error())
		return
	}
	if req.UploadID == "" {
		errorResponse(w, http.StatusBadRequest, "uploadId wajib diisi")
		return
	}
	if req.Duration < 1 || req.Duration > 60 {
		errorResponse(w, http.StatusBadRequest, "Duration harus antara 1–60 menit")
		return
	}

	// Sanitize – prevent path traversal
	uploadID := filepath.Base(req.UploadID)

	// Locate the file tusd stored
	tusFile := filepath.Join(tusUploadDir, uploadID)
	if _, err := os.Stat(tusFile); err != nil {
		errorResponse(w, http.StatusNotFound, "File upload tidak ditemukan (uploadId: "+uploadID+")")
		return
	}

	// Create job
	jobID := uuid.New().String()
	outputDir := filepath.Join(tempOutputsDir, jobID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		errorResponse(w, http.StatusInternalServerError, "Gagal membuat direktori output")
		return
	}

	job := &Job{
		ID:        jobID,
		Status:    "processing",
		Message:   "Memproses video dengan FFmpeg (stream copy mode)...",
		Clips:     []ClipInfo{},
		CreatedAt: time.Now(),
	}
	jobStore.Set(job)

	segmentSeconds := int(req.Duration * 60)
	go runFFmpeg(jobID, tusFile, uploadID, outputDir, segmentSeconds)

	jsonResponse(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// ─────────────────────────────────────────────────────────────────────────────
// FFmpeg processing (stream copy – no re-encode, quality 100% original)
// ─────────────────────────────────────────────────────────────────────────────

func runFFmpeg(jobID, inputFile, tusID, outputDir string, segmentSeconds int) {
	// ── 1. ffprobe input ─────────────────────────────────────────────────────
	inputInfo, err := getVideoInfo(inputFile)
	if err != nil {
		log.Printf("[job %s] ffprobe input error: %v", jobID, err)
	} else {
		log.Printf("[job %s] Input video: %dx%d codec=%s bitrate=%s",
			jobID, inputInfo.Width, inputInfo.Height, inputInfo.Codec, inputInfo.BitRate)
		jobStore.Update(jobID, func(j *Job) { j.InputInfo = inputInfo })
	}

	// ── 2. FFmpeg – stream copy, no re-encode ─────────────────────────────────
	outputPattern := filepath.Join(outputDir, "clip_%03d.mp4")
	args := []string{
		"-y",
		"-i", inputFile,
		"-map", "0",
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentSeconds),
		"-reset_timestamps", "1",
		"-segment_format", "mp4",
		"-segment_format_options", "movflags=+faststart",
		outputPattern,
	}

	log.Printf("[job %s] FFmpeg: ffmpeg %s", jobID, strings.Join(args, " "))

	var stderrBuf bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &stderrBuf
	runErr := cmd.Run()

	// ── 3. Always delete TUS source files after FFmpeg ─────────────────────────
	_ = os.Remove(inputFile)
	_ = os.Remove(inputFile + ".info")
	_ = os.Remove(inputFile + ".lock")
	log.Printf("[job %s] Input file deleted", jobID)

	if runErr != nil {
		log.Printf("[job %s] FFmpeg error: %v\n--- stderr ---\n%s", jobID, runErr, stderrBuf.String())
		jobStore.Update(jobID, func(j *Job) {
			j.Status = "failed"
			j.Message = "FFmpeg gagal: " + runErr.Error()
		})
		return
	}

	// ── 4. Read output clips ──────────────────────────────────────────────────
	entries, readErr := os.ReadDir(outputDir)
	if readErr != nil {
		jobStore.Update(jobID, func(j *Job) {
			j.Status = "failed"
			j.Message = "Gagal membaca hasil potongan"
		})
		return
	}

	var clips []ClipInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi == nil {
			continue
		}
		clips = append(clips, ClipInfo{
			Name:        e.Name(),
			Size:        fi.Size(),
			DownloadURL: fmt.Sprintf("/api/jobs/%s/clips/%s", jobID, e.Name()),
		})
	}

	// ── 5. ffprobe quality check on first clip ────────────────────────────────
	finalMsg := fmt.Sprintf("Video berhasil dipotong menjadi %d clip (stream copy, kualitas original)", len(clips))

	if inputInfo != nil && len(clips) > 0 {
		firstClipPath := filepath.Join(outputDir, clips[0].Name)
		outInfo, err := getVideoInfo(firstClipPath)
		if err != nil {
			log.Printf("[job %s] ffprobe output warning: %v", jobID, err)
		} else {
			log.Printf("[job %s] Output clip: %dx%d codec=%s", jobID, outInfo.Width, outInfo.Height, outInfo.Codec)

			if outInfo.Width != inputInfo.Width || outInfo.Height != inputInfo.Height {
				// This should NOT happen with -c copy
				warnMsg := fmt.Sprintf(
					"⚠️ PERINGATAN: Resolusi output (%dx%d) berbeda dari input (%dx%d)! "+
						"Proses mungkin tidak menggunakan original quality mode. Periksa konfigurasi FFmpeg.",
					outInfo.Width, outInfo.Height, inputInfo.Width, inputInfo.Height,
				)
				log.Printf("[job %s] %s", jobID, warnMsg)
				finalMsg = warnMsg
			} else {
				log.Printf("[job %s] ✅ Quality OK: input=%dx%d output=%dx%d codec=%s (stream copy mode)",
					jobID, inputInfo.Width, inputInfo.Height, outInfo.Width, outInfo.Height, outInfo.Codec)
			}
		}
	}

	jobStore.Update(jobID, func(j *Job) {
		j.Status = "completed"
		j.Message = finalMsg
		j.Clips = clips
	})
	log.Printf("[job %s] Completed: %d clips", jobID, len(clips))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/jobs/{jobId}
// ─────────────────────────────────────────────────────────────────────────────

func handleGetJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	jobID := extractJobID(r.URL.Path)
	job, ok := jobStore.Get(jobID)
	if !ok {
		errorResponse(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	jsonResponse(w, http.StatusOK, job)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/jobs/{jobId}/clips/{filename}
// ─────────────────────────────────────────────────────────────────────────────

func handleDownloadClip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.SplitN(path, "/clips/", 2)
	if len(parts) != 2 {
		errorResponse(w, http.StatusBadRequest, "Path tidak valid")
		return
	}

	jobID := filepath.Base(parts[0])
	filename := filepath.Base(parts[1])

	if _, ok := jobStore.Get(jobID); !ok {
		errorResponse(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}

	filePath := filepath.Join(tempOutputsDir, jobID, filename)
	absPath, _ := filepath.Abs(filePath)
	absBase, _ := filepath.Abs(filepath.Join(tempOutputsDir, jobID))
	if !strings.HasPrefix(absPath, absBase+string(filepath.Separator)) {
		errorResponse(w, http.StatusForbidden, "Akses ditolak")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, filePath)
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /api/jobs/{jobId}
// ─────────────────────────────────────────────────────────────────────────────

func handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	jobID := extractJobID(r.URL.Path)
	if _, ok := jobStore.Get(jobID); !ok {
		errorResponse(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	_ = os.RemoveAll(filepath.Join(tempOutputsDir, jobID))
	jobStore.Delete(jobID)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Job berhasil dihapus"})
}

// ─────────────────────────────────────────────────────────────────────────────
// Cleanup goroutine
// ─────────────────────────────────────────────────────────────────────────────

func startCleanup() {
	go func() {
		for range time.NewTicker(cleanupInterval).C {
			now := time.Now()
			for _, job := range jobStore.All() {
				if now.Sub(job.CreatedAt) > fileMaxAge {
					log.Printf("[cleanup] Menghapus job lama: %s", job.ID)
					_ = os.RemoveAll(filepath.Join(tempOutputsDir, job.ID))
					jobStore.Delete(job.ID)
				}
			}
			// Clean orphaned TUS files
			cleanOrphanedTUS()
		}
	}()
}

func cleanOrphanedTUS() {
	entries, err := os.ReadDir(tusUploadDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Skip .info and .lock files
		if strings.HasSuffix(e.Name(), ".info") || strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) > fileMaxAge {
			id := e.Name()
			log.Printf("[cleanup] Menghapus TUS file lama: %s", id)
			_ = os.Remove(filepath.Join(tusUploadDir, id))
			_ = os.Remove(filepath.Join(tusUploadDir, id+".info"))
			_ = os.Remove(filepath.Join(tusUploadDir, id+".lock"))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Utility
// ─────────────────────────────────────────────────────────────────────────────

func extractJobID(path string) string {
	id := strings.TrimPrefix(path, "/api/jobs/")
	if idx := strings.Index(id, "/"); idx != -1 {
		id = id[:idx]
	}
	return filepath.Base(id)
}

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	// ── Check tools ───────────────────────────────────────────────────────────
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Fatal("❌ FFmpeg tidak ditemukan di PATH.\n   Windows: winget install ffmpeg")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		log.Fatal("❌ ffprobe tidak ditemukan (seharusnya ikut dengan FFmpeg)")
	}
	log.Println("✅ FFmpeg & ffprobe ditemukan")

	// ── Create temp dirs ──────────────────────────────────────────────────────
	for _, dir := range []string{tusUploadDir, tempOutputsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Gagal membuat direktori %s: %v", dir, err)
		}
	}

	// ── TUS unrouted handler – explicit per-method routing to avoid mux issues ─
	tusFileStore := filestore.New(tusUploadDir)
	composer := tusd.NewStoreComposer()
	tusFileStore.UseIn(composer)

	tusUnrouted, err := tusd.NewUnroutedHandler(tusd.Config{
		BasePath:      "/files/",
		StoreComposer: composer,
		MaxSize:       maxUploadSize,
	})
	if err != nil {
		log.Fatalf("Gagal membuat TUS handler: %v", err)
	}

	// ── Start background cleanup ──────────────────────────────────────────────
	startCleanup()

	// ── Router ────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// TUS resumable upload endpoint – manual routing bypasses tusd internal mux
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			tusUnrouted.PostFile(w, r)
		case http.MethodHead:
			tusUnrouted.HeadFile(w, r)
		case http.MethodPatch:
			tusUnrouted.PatchFile(w, r)
		case http.MethodDelete:
			tusUnrouted.DelFile(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// Start processing after TUS upload completes
	mux.HandleFunc("/api/start", handleStart)

	// Job management
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/clips/") {
			handleDownloadClip(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleGetJob(w, r)
		case http.MethodDelete:
			handleDeleteJob(w, r)
		default:
			errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	})

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	log.Printf("🚀 Server berjalan di http://localhost%s", backendPort)
	log.Println("   TUS upload endpoint : /files/")
	log.Println("   API endpoint        : /api/")
	log.Printf("   Max upload size     : 5 GB")
	log.Printf("   Cleanup setiap      : %v (file dihapus setelah %v)", cleanupInterval, fileMaxAge)

	if err := http.ListenAndServe(backendPort, corsMiddleware(mux)); err != nil {
		log.Fatalf("Server gagal: %v", err)
	}
}
