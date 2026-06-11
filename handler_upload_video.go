package main

import (
	"net/http"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"crypto/rand"
	"io"
	"os"
	"mime"
	"fmt"
	"encoding/hex"
	"strings"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"context"
	"path"
	"os/exec"
	"encoding/json"
	"errors"
	"bytes"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

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
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not authorized to update this video", nil)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Missing Content-Type for thumbnail", nil)
		return
	}

	mediaParse, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing media type", err)
		return
	}
	if mediaParse != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Incorrect media type", nil)
		return
	}

	temp, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to create temp file", err)
		return
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	_, err = io.Copy(temp, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to copy file to temp file", err)
		return
	}
	_, err = temp.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "seeky starty faily", err)
		return
	}

	processed, err := processVideoForFastStart(temp.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to process video", err)
		return
	}
	defer os.Remove(processed)
	procFile, err := os.Open(processed)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open processed video file", err)
		return
	}

	directory := ""
	aspectRatio, err := getVideoAspectRatio(processed)
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

	bucket := cfg.s3Bucket
	randomBytes := make([]byte, 32)
	rand.Read(randomBytes)
	encoded := hex.EncodeToString(randomBytes)
	extension := strings.Split(mediaType, "/")[1]
	key := path.Join(directory, encoded + "." + extension)
	
	putOBJ := s3.PutObjectInput{
		Bucket:			&bucket,
		Key:			&key,
		Body:			procFile,
		ContentType:	&mediaType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &putOBJ)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error calling S3 kit", err)
		return
	}

	updatedURL := cfg.s3CfDistribution + key
	video.VideoURL = &updatedURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video metadata in database", err)
		return
	}


	respondWithJSON(w, http.StatusOK, video)
}



func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}

	var output struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
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


