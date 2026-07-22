package prompts

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"testing"
)

func TestPersistUploadedImagesStoresValidatedBytesWithoutRetainingThem(t *testing.T) {
	files := uploadedFiles(t, []uploadedFile{{mimeType: "image/png", contents: []byte("png")}})
	images, cleanup, err := PersistUploadedImages(files, t.TempDir())
	if err != nil || len(images) != 1 {
		t.Fatalf("images = %#v, %v", images, err)
	}
	if images[0].MIMEType != "image/png" || images[0].Size != 3 || images[0].Path == "" {
		t.Fatalf("image = %#v", images[0])
	}
	if contents, readErr := os.ReadFile(images[0].Path); readErr != nil || string(contents) != "png" {
		t.Fatalf("persisted image = %q, %v", contents, readErr)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(images[0].Path); !os.IsNotExist(err) {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestPersistUploadedImagesEnforcesCountTypeAndSizeLimits(t *testing.T) {
	many := make([]uploadedFile, MaxImages+1)
	for index := range many {
		many[index] = uploadedFile{mimeType: "image/png", contents: []byte("png")}
	}
	tests := []struct {
		name, message string
		files         []uploadedFile
	}{
		{"count", "Too many images", many},
		{"type", "Only image uploads are supported", []uploadedFile{{mimeType: "text/plain", contents: []byte("text")}}},
		{"size", "Image upload is too large", []uploadedFile{{mimeType: "image/png", contents: bytes.Repeat([]byte{'x'}, MaxImageBytes+1)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := PersistUploadedImages(uploadedFiles(t, test.files), t.TempDir())
			if err == nil || err.Error() != test.message {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type uploadedFile struct {
	mimeType string
	contents []byte
}

func uploadedFiles(t *testing.T, files []uploadedFile) []*multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="images[]"; filename="image"`)
		header.Set("Content-Type", file.mimeType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "/", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	return request.MultipartForm.File["images[]"]
}
