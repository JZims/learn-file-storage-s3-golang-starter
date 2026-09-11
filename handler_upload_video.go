package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	videoID, err := uuid.Parse(r.PathValue("videoID"))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to parse videoID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid credentials", err)
		return
	}

	reqUserId, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusForbidden, "Invalid Auth Token", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "No video found", err)
		return
	}

	if video.UserID != reqUserId {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized to upload video", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to parse file from form", err)
		return
	}
	defer file.Close()

	ext, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Media Parameter", err)
		return
	}
	if ext != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Incorrect file type", fmt.Errorf("incorrect file type"))
		return
	}

	tmpDst, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temp file", err)
		return
	}
	defer os.Remove(tmpDst.Name())
	defer tmpDst.Close()

	_, err = io.Copy(tmpDst, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying image file", err)
		return
	}

	_, err = tmpDst.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error tracking file read", err)
		return
	}

	directory := ""
	aspectRatio, err := getVideoAspectRatio(tmpDst.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error determining aspect ratio", err)
		return
	}
	switch aspectRatio {
	case "16:9":
		directory = "landscape"
	case "9:16":
		directory = "portrait"
	default:
		directory = "other"
	}

	pathForUrl := make([]byte, 32)
	if _, err := rand.Read(pathForUrl); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating video filename", err)
		return
	}
	// Upload to S3 Bucket
	videoKey := base64.RawURLEncoding.EncodeToString(pathForUrl)
	strExtension, _ := mime.ExtensionsByType(ext)
	formattedKey := videoKey + strExtension[0]
	formattedKey = path.Join(directory, formattedKey)

	processedFilePath, err := processVideoForFastStart(tmpDst.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating process file for fastStart", err)
		return
	}

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening process file for fastStart", err)
		return
	}
	defer os.Remove(processedFilePath)
	defer processedFile.Close()

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &formattedKey,
		Body:        processedFile,
		ContentType: &ext,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video file", err)
		return
	}

	videoURL, err := url.JoinPath(cfg.s3CfDistribution, formattedKey)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating video url", err)
		return
	}
	log.Println(videoURL)

	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video file", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)

}

func getVideoAspectRatio(filepath string) (string, error) {

	var output struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filepath)
	var stdOut bytes.Buffer
	cmd.Stdout = &stdOut

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}

	if err := json.Unmarshal(stdOut.Bytes(), &output); err != nil {
		return "", fmt.Errorf("could not parse ffprobe output: %v", err)
	}

	if len(output.Streams) == 0 {
		return "", errors.New("no video streams found")
	}

	width := output.Streams[0].Width
	height := output.Streams[0].Height

	if width == 16*height/9 {
		return "16:9", nil
	} else if height == 16*width/9 {
		return "9:16", nil
	}
	return "other", nil

}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"

	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %v", err)
	}

	return outputPath, nil
}
