package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid or missing header in request", err)
		return
	}
	defer file.Close()

	mediaType := header.Header.Get("Content-Type")

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	var fileExt string
	if mediaType == "image/png" {
		fileExt = "png"
	}

	pathForUrl := make([]byte, 32)
	if _, err := rand.Read(pathForUrl); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating thumbnail filename", err)
		return
	}

	thumbnailKey := base64.RawURLEncoding.EncodeToString(pathForUrl)
	fileName := fmt.Sprintf("%v.%v", thumbnailKey, fileExt)
	diskPath := filepath.Join(cfg.assetsRoot, fileName)
	fullTnUrl := fmt.Sprintf("http://localhost:%v/assets/%v", cfg.port, fileName)

	tnDst, err := os.Create(diskPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating image file", err)
		return
	}
	defer tnDst.Close()

	_, err = io.Copy(tnDst, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying image to file", err)
		return
	}

	video.UpdatedAt = time.Now()
	video.ThumbnailURL = &fullTnUrl

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
