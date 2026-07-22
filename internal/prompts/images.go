package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
)

const (
	MaxImages     = 5
	MaxImageBytes = 10 << 20
)

type UploadedImage struct {
	Path     string
	MIMEType string
	Size     int64
}

var imageExtensions = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp",
}

func ValidateUploadedImages(files []*multipart.FileHeader) error {
	if len(files) > MaxImages {
		return errors.New("Too many images")
	}
	for _, header := range files {
		if imageExtensions[header.Header.Get("Content-Type")] == "" {
			return errors.New("Only image uploads are supported")
		}
		if header.Size > MaxImageBytes {
			return errors.New("Image upload is too large")
		}
	}
	return nil
}

func PersistUploadedImages(files []*multipart.FileHeader, directory string) ([]UploadedImage, func() error, error) {
	cleanup := func() error { return nil }
	if err := ValidateUploadedImages(files); err != nil {
		return nil, cleanup, err
	}
	if len(files) == 0 {
		return nil, cleanup, nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, cleanup, err
	}
	created := []string{}
	cleanup = func() error {
		var result error
		for _, path := range created {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
		_ = os.Remove(directory)
		return result
	}
	images := make([]UploadedImage, 0, len(files))
	for _, header := range files {
		mimeType := header.Header.Get("Content-Type")
		extension := imageExtensions[mimeType]
		if extension == "" {
			_ = cleanup()
			return nil, func() error { return nil }, errors.New("Only image uploads are supported")
		}
		image, wasCreated, err := persistUploadedImage(header, directory, extension, mimeType)
		if err != nil {
			cleanupErr := cleanup()
			return nil, func() error { return nil }, errors.Join(err, cleanupErr)
		}
		if wasCreated {
			created = append(created, image.Path)
		}
		images = append(images, image)
	}
	return images, cleanup, nil
}

func persistUploadedImage(header *multipart.FileHeader, directory, extension, mimeType string) (UploadedImage, bool, error) {
	source, err := header.Open()
	if err != nil {
		return UploadedImage{}, false, errors.New("Only image uploads are supported")
	}
	defer source.Close()
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return UploadedImage{}, false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return UploadedImage{}, false, err
	}
	digest := sha256.New()
	size, copyErr := copyUploadedImage(temporary, digest, source)
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return UploadedImage{}, false, errors.Join(copyErr, closeErr)
	}
	if size > MaxImageBytes {
		return UploadedImage{}, false, errors.New("Image upload is too large")
	}
	path := filepath.Join(directory, hex.EncodeToString(digest.Sum(nil))+"."+extension)
	if err := os.Link(temporaryPath, path); errors.Is(err, os.ErrExist) {
		return UploadedImage{Path: path, MIMEType: mimeType, Size: size}, false, nil
	} else if err != nil {
		return UploadedImage{}, false, err
	}
	return UploadedImage{Path: path, MIMEType: mimeType, Size: size}, true, nil
}

func copyUploadedImage(destination io.Writer, digest hash.Hash, source io.Reader) (int64, error) {
	return io.Copy(io.MultiWriter(destination, digest), io.LimitReader(source, MaxImageBytes+1))
}
