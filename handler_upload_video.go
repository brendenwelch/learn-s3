package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video", err)
		return
	}

	if userID != video.UserID {
		respondWithError(w, http.StatusUnauthorized, "User doesn't own this video", fmt.Errorf("User ID %v does not match owner's user ID %v", userID, video.UserID))
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	const maxMemory = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	fileType := header.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(fileType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", fmt.Errorf("Media type %v unsupported", mediaType))
		return
	}

	tmpVideoFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save video to server", err)
		return
	}
	defer os.Remove(tmpVideoFile.Name())
	defer tmpVideoFile.Close()

	_, err = io.Copy(tmpVideoFile, file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save video to server", err)
		return
	}
	tmpVideoFile.Seek(0, io.SeekStart)

	prefix, _ := getVideoAspectRatio(tmpVideoFile.Name())
	randomData := make([]byte, 32)
	rand.Read(randomData)
	fileName := prefix + base64.RawURLEncoding.EncodeToString(randomData) + ".mp4"

	tmpVideoFile.Seek(0, io.SeekStart)
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileName,
		Body:        tmpVideoFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save video to server", err)
		return
	}

	videoURL := "https://" + cfg.s3Bucket + ".s3." + cfg.s3Region + ".amazonaws.com/" + fileName
	video.VideoURL = &videoURL
	cfg.db.UpdateVideo(video)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		return "other", err
	}

	var params struct {
		Streams []struct {
			AspectRatio string `json:"display_aspect_ratio"`
		} `json:"streams"`
	}
	err = json.Unmarshal(out.Bytes(), &params)
	if err != nil {
		return "other", err
	}

	prefix := ""
	switch params.Streams[0].AspectRatio {
	case "16:9":
		prefix = "landscape/"
	case "9:16":
		prefix = "portrait/"
	default:
		prefix = "other/"
	}

	return prefix, nil
}
