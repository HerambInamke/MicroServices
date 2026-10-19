package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/mux"
)

const maxUploadBytes = 10 << 20 // 10 MiB

// Allowed types are sniffed from the file bytes, never trusted from the client.
var allowedTypes = map[string]struct{ ext, kind string }{
	"image/jpeg":      {".jpg", "image"},
	"image/png":       {".png", "image"},
	"image/webp":      {".webp", "image"},
	"application/pdf": {".pdf", "document"},
}

type Server struct {
	meta    MetaStore
	objects ObjectStore
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/media/upload", s.authed(s.upload)).Methods("POST")
	r.HandleFunc("/media/{id}", s.get).Methods("GET")
	r.HandleFunc("/media/{id}/content", s.content).Methods("GET")
	r.HandleFunc("/media/{id}", s.authed(s.delete)).Methods("DELETE")
	return r
}

// respond writes the {data, error, meta} envelope from api-architecture.md.
func respond(w http.ResponseWriter, status int, data any, code, msg string) {
	var apiErr any
	if code != "" {
		apiErr = map[string]string{"code": code, "message": msg}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data, "error": apiErr, "meta": map[string]any{}})
}

func fail(w http.ResponseWriter, status int, code, msg string) { respond(w, status, nil, code, msg) }

func internal(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	fail(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
}

// authed requires the identity header the gateway sets after verifying the JWT.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-Id")
		if userID == "" {
			fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}
		h(w, r, userID)
	}
}

func view(f MediaFile) map[string]any {
	return map[string]any{
		"id":          f.ID,
		"ownerId":     f.OwnerID,
		"url":         "/api/v1/media/" + f.ID + "/content",
		"contentType": f.ContentType,
		"sizeBytes":   f.SizeBytes,
		"type":        f.Kind,
		"uploadedAt":  f.UploadedAt,
	}
}

func randomKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, userID string) {
	// Allow some headroom over the file limit for the multipart envelope itself.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "files must be 10 MiB or smaller")
			return
		}
		fail(w, http.StatusBadRequest, "INVALID_UPLOAD", "send the file as multipart/form-data in a field named \"file\"")
		return
	}
	defer file.Close()
	if header.Size > maxUploadBytes {
		fail(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "files must be 10 MiB or smaller")
		return
	}
	if header.Size == 0 {
		fail(w, http.StatusBadRequest, "INVALID_UPLOAD", "file is empty")
		return
	}

	sniff := make([]byte, 512)
	n, err := io.ReadFull(file, sniff)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		internal(w, err)
		return
	}
	contentType := http.DetectContentType(sniff[:n])
	kind, ok := allowedTypes[contentType]
	if !ok {
		fail(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_TYPE", "only JPEG, PNG, WebP and PDF files are allowed")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		internal(w, err)
		return
	}

	key := userID + "/" + randomKey() + kind.ext
	if err := s.objects.Put(r.Context(), key, file, header.Size, contentType); err != nil {
		internal(w, err)
		return
	}
	f, err := s.meta.Create(r.Context(), MediaFile{OwnerID: userID, ObjectKey: key, ContentType: contentType, SizeBytes: header.Size, Kind: kind.kind})
	if err != nil {
		// Don't leave an orphaned object behind.
		if rmErr := s.objects.Remove(r.Context(), key); rmErr != nil {
			log.Printf("orphaned object %s: %v", key, rmErr)
		}
		internal(w, err)
		return
	}
	respond(w, http.StatusCreated, view(f), "", "")
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (MediaFile, bool) {
	f, err := s.meta.Get(r.Context(), mux.Vars(r)["id"])
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "MEDIA_NOT_FOUND", "media not found")
		return f, false
	}
	if err != nil {
		internal(w, err)
		return f, false
	}
	return f, true
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.lookup(w, r); ok {
		respond(w, http.StatusOK, view(f), "", "")
	}
}

func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	f, ok := s.lookup(w, r)
	if !ok {
		return
	}
	obj, err := s.objects.Open(r.Context(), f.ObjectKey)
	if err != nil {
		internal(w, err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("streaming %s: %v", f.ID, err)
	}
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, userID string) {
	f, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if f.OwnerID != userID {
		fail(w, http.StatusForbidden, "FORBIDDEN", "you can only delete your own files")
		return
	}
	if err := s.meta.Delete(r.Context(), f.ID); err != nil {
		internal(w, err)
		return
	}
	// The row is gone, so the file is unreachable either way; a failed remove only leaks storage.
	if err := s.objects.Remove(r.Context(), f.ObjectKey); err != nil {
		log.Printf("orphaned object %s: %v", f.ObjectKey, err)
	}
	respond(w, http.StatusOK, map[string]any{"id": f.ID, "deleted": true}, "", "")
}
