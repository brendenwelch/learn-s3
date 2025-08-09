package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video", err)
		return
	}

	if userID != video.UserID {
		respondWithError(w, http.StatusUnauthorized, "User doesn't own this video", fmt.Errorf("User ID %v does not match owner's user ID %v", userID, video.UserID))
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	fileType := header.Header.Get("Content-Type")
	fileExts, err := mime.ExtensionsByType(fileType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", err)
		return
	}
	if fileExts[0] != ".jpg" && fileExts[0] != ".jpeg" && fileExts[0] != ".png" {
		respondWithError(w, http.StatusBadRequest, "File type not allowed", fmt.Errorf("File extension of %v is not allowed", fileExts[0]))
		return
	}
	fileName := videoIDString + fileExts[0]

	thumbnail, err := os.Create(filepath.Join(cfg.assetsRoot, fileName))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save thumbnail to server", err)
		return
	}
	defer thumbnail.Close()

	_, err = io.Copy(thumbnail, file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to save thumbnail to server", err)
		return
	}

	thumbnailURL := "http://localhost:8091/assets/" + fileName
	video.ThumbnailURL = &thumbnailURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusNotModified, "Failed to update video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
