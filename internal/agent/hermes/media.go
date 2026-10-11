package hermes

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// mediaTag is how Hermes shares a file: MEDIA: and the file's absolute path, which Hermes's messaging platforms and
// desktop app send as an attachment. Models often wrap the tag in Markdown code or emphasis.
var mediaTag = regexp.MustCompile("[`*_]{0,3}MEDIA:[ \t]*(`[^`\n]+`|\"[^\"\n]+\"|\\S+)")

// mediaImage is an image file that a reply shares.
type mediaImage struct{ path, mimeType string }

// mediaImages takes the tags of the images that the browser can show out of a reply's text. A tag of another file
// stays as Hermes wrote it.
func mediaImages(text string) (string, []mediaImage) {
	var images []mediaImage
	text = mediaTag.ReplaceAllStringFunc(text, func(tag string) string {
		path := strings.TrimRight(strings.Trim(mediaTag.FindStringSubmatch(tag)[1], "`\""), "`*_.,;:!?)")
		mimeType, ok := imageType(path)
		if !ok {
			return tag
		}
		images = append(images, mediaImage{path, mimeType})
		return ""
	})
	return text, images
}

// sharedImageBytes is the largest image that Gripi shows from a reply, as large as an image it attaches to a prompt.
const sharedImageBytes = 10 << 20

// imageType returns the type of an image file that the browser can show.
func imageType(path string) (string, bool) {
	// A file that isn't regular, such as a pipe, could hold the read.
	if info, err := os.Stat(path); !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Size() > sharedImageBytes {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 512)
	count, _ := io.ReadFull(file, head)
	mimeType := http.DetectContentType(head[:count])
	return mimeType, slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mimeType)
}

func readImage(path string) ([]byte, string, error) {
	mimeType, ok := imageType(path)
	if !ok {
		return nil, "", fs.ErrNotExist
	}
	data, err := os.ReadFile(path)
	return data, mimeType, err
}
