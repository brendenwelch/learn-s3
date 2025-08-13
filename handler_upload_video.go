package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
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

	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
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

	tmpProcessingName, err := processVideoForFastStart(tmpVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save video to server", err)
		return
	}
	tmpProcessingFile, _ := os.Open(tmpProcessingName)
	defer tmpProcessingFile.Close()

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileName,
		Body:        tmpProcessingFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save video to server", err)
		return
	}

	videoURL := cfg.s3Bucket + "," + fileName
	video.VideoURL = &videoURL
	cfg.db.UpdateVideo(video)

	video, err = cfg.dbVideoToPresignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate presigned URL", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
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

func processVideoForFastStart(filePath string) (string, error) {
	outFilePath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outFilePath)
	err := cmd.Run()
	return outFilePath, err
}

func generatePresignedURL(s3Client *s3.Client, bucket string, key string, expireTime time.Duration) (string, error) {
	s3PresignClient := s3.NewPresignClient(s3Client)
	req, err := s3PresignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (cfg *apiConfig) dbVideoToPresignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	params := strings.Split(*video.VideoURL, ",")
	if len(params) < 2 {
		return video, nil
	}
	presignedURL, err := generatePresignedURL(cfg.s3Client, params[0], params[1], time.Minute)
	if err != nil {
		return video, err
	}
	video.VideoURL = &presignedURL
	return video, nil
}
